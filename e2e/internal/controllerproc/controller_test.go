package controllerproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

func TestStart(t *testing.T) {
	t.Parallel()

	t.Run("starts controller process with inherited env overrides and logs streams", func(t *testing.T) {
		t.Parallel()

		controllerPath := writeControllerStub(t, strings.Join([]string{
			"#!/bin/sh",
			"echo \"stdout KUBECONFIG=${KUBECONFIG}\"",
			"echo \"stdout OCI_CONFIG_FILE=${OCI_CONFIG_FILE}\"",
			"echo \"stdout APP_K8SAPI_NOOP=${APP_K8SAPI_NOOP}\"",
			"echo \"stderr APP_OCIAPI_NOOP=${APP_OCIAPI_NOOP}\" >&2",
			"echo \"stderr OCI_CLI_PROFILE=${OCI_CLI_PROFILE}\" >&2",
			"trap 'echo \"stdout received SIGTERM\"; exit 0' TERM INT",
			"while :; do sleep 1; done",
		}, "\n")+"\n")

		logSink := &fakeTestLogSink{}
		controllerLogPath := filepath.Join(t.TempDir(), "controller.log")
		cfg := config.Config{
			Kubernetes: config.KubernetesConfig{
				KubeconfigPath: "/tmp/controllerproc-kubeconfig",
			},
			OCI: config.OCIConfig{
				ConfigFile:    "/tmp/controllerproc-oci-config",
				ConfigProfile: "TEAM",
			},
			Controller: config.ControllerConfig{
				BinPath: controllerPath,
			},
		}

		proc, err := Start(logSink, cfg, NewStartOptions().WithEnviron([]string{
			"PATH=" + os.Getenv("PATH"),
			"OCI_CLI_CONFIG_FILE=/tmp/wrong-config",
			"OCI_CLI_CONFIG_PROFILE=WRONG",
			"SOME_OTHER_ENV=present",
		}).WithLogFilePath(controllerLogPath))
		require.NoError(t, err)
		require.False(t, proc.Skipped())
		require.NotZero(t, proc.PID())
		require.Equal(t, 1, logSink.CleanupCount())

		waitForControllerLog(t, proc, "controller stdout: stdout KUBECONFIG=/tmp/controllerproc-kubeconfig")
		waitForControllerLog(t, proc, "controller stdout: stdout OCI_CONFIG_FILE=/tmp/controllerproc-oci-config")
		waitForControllerLog(t, proc, "controller stdout: stdout APP_K8SAPI_NOOP=false")
		waitForControllerLog(t, proc, "controller stderr: stderr APP_OCIAPI_NOOP=false")
		waitForControllerLog(t, proc, "controller stderr: stderr OCI_CLI_PROFILE=TEAM")

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
		assertControllerLogContains(
			t,
			controllerLogPath,
			"controller stdout: stdout KUBECONFIG=/tmp/controllerproc-kubeconfig",
		)
		assertControllerLogContains(
			t,
			controllerLogPath,
			"controller stdout: stdout OCI_CONFIG_FILE=/tmp/controllerproc-oci-config",
		)
		assertControllerLogContains(
			t,
			controllerLogPath,
			"controller stderr: stderr APP_OCIAPI_NOOP=false",
		)
		assertControllerLogContains(
			t,
			controllerLogPath,
			"controller stderr: stderr OCI_CLI_PROFILE=TEAM",
		)
		assertControllerLogContains(
			t,
			controllerLogPath,
			"controller stdout: stdout received SIGTERM",
		)
		assert.True(t, logSink.Contains("controller process pid="))
	})

	t.Run("skips controller startup when configured", func(t *testing.T) {
		t.Parallel()

		logSink := &fakeTestLogSink{}
		cfg := config.Config{
			Kubernetes: config.KubernetesConfig{
				KubeconfigPath: "/tmp/controllerproc-kubeconfig",
			},
			Controller: config.ControllerConfig{
				BinPath:   "/tmp/missing-controller",
				SkipStart: true,
			},
		}

		proc, err := Start(logSink, cfg, nil)
		require.NoError(t, err)
		require.True(t, proc.Skipped())
		require.Zero(t, proc.PID())
		require.NoError(t, proc.Stop())
		assert.True(t, logSink.Contains("controller start skipped because "+envSkipController+"=true"))
	})

	t.Run("returns clear error for missing controller binary", func(t *testing.T) {
		t.Parallel()

		logSink := &fakeTestLogSink{}
		cfg := config.Config{
			Kubernetes: config.KubernetesConfig{
				KubeconfigPath: "/tmp/controllerproc-kubeconfig",
			},
			Controller: config.ControllerConfig{
				BinPath: filepath.Join(t.TempDir(), "missing-controller"),
			},
		}

		_, err := Start(logSink, cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), envControllerBin+" points to missing file")
	})

	t.Run("shapes kubeconfig for the requested Kubernetes context", func(t *testing.T) {
		controllerPath := writeControllerStub(t, strings.Join([]string{
			"#!/bin/sh",
			"echo \"stdout current-context=$(awk '/^current-context:/ {print $2}' \"$KUBECONFIG\")\"",
			"echo \"stdout kubeconfig-path=${KUBECONFIG}\"",
			"trap 'exit 0' TERM INT",
			"while :; do sleep 1; done",
		}, "\n")+"\n")

		sourceKubeconfigPath := writeKubeconfig(t, &clientcmdapi.Config{
			CurrentContext: "ctx-a",
			Clusters: map[string]*clientcmdapi.Cluster{
				"cluster-a": {Server: "https://cluster-a.example.com"},
				"cluster-b": {Server: "https://cluster-b.example.com"},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				"user-a": {Token: "token-a"},
				"user-b": {Token: "token-b"},
			},
			Contexts: map[string]*clientcmdapi.Context{
				"ctx-a": {Cluster: "cluster-a", AuthInfo: "user-a"},
				"ctx-b": {Cluster: "cluster-b", AuthInfo: "user-b"},
			},
		})

		logSink := &fakeTestLogSink{}
		controllerLogPath := filepath.Join(t.TempDir(), "controller.log")
		cfg := config.Config{
			Kubernetes: config.KubernetesConfig{
				KubeconfigPath: sourceKubeconfigPath,
				Context:        "ctx-b",
			},
			Controller: config.ControllerConfig{
				BinPath: controllerPath,
			},
		}

		proc, err := Start(logSink, cfg, NewStartOptions().WithEnviron([]string{
			"PATH=" + os.Getenv("PATH"),
		}).WithLogFilePath(controllerLogPath))
		require.NoError(t, err)
		require.NotNil(t, proc)

		waitForControllerLog(t, proc, "controller stdout: stdout current-context=ctx-b")
		waitForControllerLog(t, proc, "controller stdout: stdout kubeconfig-path=")
		assertControllerLogNotContains(
			t,
			controllerLogPath,
			"controller stdout: stdout kubeconfig-path="+sourceKubeconfigPath,
		)

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
	})

	t.Run("allows empty kubeconfig so the controller can use default loading", func(t *testing.T) {
		controllerPath := writeControllerStub(t, strings.Join([]string{
			"#!/bin/sh",
			"echo \"stdout default kubeconfig path allowed\"",
		}, "\n")+"\n")

		logSink := &fakeTestLogSink{}
		controllerLogPath := filepath.Join(t.TempDir(), "controller.log")
		cfg := config.Config{
			Controller: config.ControllerConfig{
				BinPath: controllerPath,
			},
		}

		proc, err := Start(logSink, cfg, NewStartOptions().WithEnviron([]string{
			"PATH=" + os.Getenv("PATH"),
		}).WithLogFilePath(controllerLogPath))
		require.NoError(t, err)
		require.False(t, proc.Skipped())
		require.NotZero(t, proc.PID())
		waitForControllerLog(t, proc, "controller stdout: stdout default kubeconfig path allowed")

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
		assertControllerLogContains(t, controllerLogPath, "controller stdout: stdout default kubeconfig path allowed")
	})

	t.Run("forced stop accepts the expected signaled wait result after kill", func(t *testing.T) {
		controllerPath := writeControllerStub(t, strings.Join([]string{
			"#!/usr/bin/env python3",
			"import signal",
			"import time",
			"signal.signal(signal.SIGTERM, signal.SIG_IGN)",
			"print('ready', flush=True)",
			"while True:",
			"    time.sleep(1)",
		}, "\n")+"\n")

		logSink := &fakeTestLogSink{}
		controllerLogPath := filepath.Join(t.TempDir(), "controller.log")
		cfg := config.Config{
			Kubernetes: config.KubernetesConfig{
				KubeconfigPath: "/tmp/controllerproc-kubeconfig",
			},
			Controller: config.ControllerConfig{
				BinPath: controllerPath,
			},
		}

		proc, err := Start(logSink, cfg, NewStartOptions().
			WithEnviron([]string{"PATH=" + os.Getenv("PATH")}).
			WithLogFilePath(controllerLogPath).
			WithStopTimeout(100*time.Millisecond))
		require.NoError(t, err)
		waitForControllerLog(t, proc, "controller stdout: ready")

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
		assert.True(t, logSink.Contains("did not stop within 100ms; killing"))
		require.NoError(t, proc.Stop())
	})
}

func TestProcessWaitForLog(t *testing.T) {
	t.Parallel()

	t.Run("returns when the requested log line is observed", func(t *testing.T) {
		t.Parallel()

		controllerPath := writeControllerStub(t, strings.Join([]string{
			"#!/bin/sh",
			"echo \"Starting controller manager\"",
			"trap 'exit 0' TERM INT",
			"while :; do sleep 1; done",
		}, "\n")+"\n")

		logSink := &fakeTestLogSink{}
		controllerLogPath := filepath.Join(t.TempDir(), "controller.log")
		proc, err := Start(logSink, config.Config{
			Controller: config.ControllerConfig{BinPath: controllerPath},
		}, NewStartOptions().WithEnviron([]string{
			"PATH=" + os.Getenv("PATH"),
		}).WithLogFilePath(controllerLogPath))
		require.NoError(t, err)

		waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		require.NoError(t, proc.WaitForLog(waitCtx, "Starting controller manager"))
		assertControllerLogContains(t, controllerLogPath, "controller stdout: Starting controller manager")

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
	})

	t.Run("returns early when the process exits before the requested log line", func(t *testing.T) {
		t.Parallel()

		controllerPath := writeControllerStub(t, strings.Join([]string{
			"#!/bin/sh",
			"echo \"different log line\"",
			"exit 0",
		}, "\n")+"\n")

		logSink := &fakeTestLogSink{}
		controllerLogPath := filepath.Join(t.TempDir(), "controller.log")
		proc, err := Start(logSink, config.Config{
			Controller: config.ControllerConfig{BinPath: controllerPath},
		}, NewStartOptions().WithEnviron([]string{
			"PATH=" + os.Getenv("PATH"),
		}).WithLogFilePath(controllerLogPath))
		require.NoError(t, err)

		waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		err = proc.WaitForLog(waitCtx, "Starting controller manager")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Starting controller manager")
		assert.Contains(t, err.Error(), "exited before log")
		assertControllerLogContains(t, controllerLogPath, "controller stdout: different log line")

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
	})

	t.Run("does not miss concurrent log wake ups", func(t *testing.T) {
		t.Parallel()

		const (
			iterations = 100
			waiters    = 4
		)

		for range iterations {
			proc := &Process{
				exitDone:    make(chan struct{}),
				streamsDone: closedSignalChannel(),
				logWait:     make(chan struct{}),
			}

			start := make(chan struct{})
			errs := make(chan error, waiters)
			var waitersDone sync.WaitGroup
			waitersDone.Add(waiters)

			for range waiters {
				go func() {
					defer waitersDone.Done()

					<-start

					waitCtx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()

					errs <- proc.WaitForLog(waitCtx, "Starting controller manager")
				}()
			}

			close(start)
			runtime.Gosched()
			go proc.appendLogLine("controller stdout: Starting controller manager")

			waitersDone.Wait()
			close(errs)

			for err := range errs {
				require.NoError(t, err)
			}
		}
	})
}

func TestBuildControllerEnv(t *testing.T) {
	t.Parallel()

	t.Run("overrides kubeconfig and oci env when explicitly configured", func(t *testing.T) {
		t.Parallel()

		cfg := config.Config{
			Kubernetes: config.KubernetesConfig{
				KubeconfigPath: "/tmp/test-kubeconfig",
			},
			OCI: config.OCIConfig{
				ConfigFile:    "/tmp/test-oci-config",
				ConfigProfile: "PRIMARY",
			},
		}

		env := buildControllerEnv(cfg, []string{
			"PATH=/usr/bin",
			envKubeconfig + "=/tmp/original-kubeconfig",
			envOCIConfigFileAlt + "=/tmp/original-config",
			envOCIConfigProfileAlt + "=FALLBACK",
			envK8sAPINoop + "=true",
			envOCIAPINoop + "=true",
		}, "/tmp/test-kubeconfig")

		assertEnvValue(t, env, envKubeconfig, "/tmp/test-kubeconfig")
		assertEnvValue(t, env, envOCIConfigFile, "/tmp/test-oci-config")
		assertEnvValue(t, env, envOCIConfigFileAlt, "/tmp/test-oci-config")
		assertEnvValue(t, env, envOCIConfigProfile, "PRIMARY")
		assertEnvValue(t, env, envOCIConfigProfileAlt, "PRIMARY")
		assertEnvValue(t, env, envK8sAPINoop, "false")
		assertEnvValue(t, env, envOCIAPINoop, "false")
	})

	t.Run("preserves inherited kubeconfig when no explicit path is configured", func(t *testing.T) {
		t.Parallel()

		env := buildControllerEnv(config.Config{}, []string{
			"PATH=/usr/bin",
			envKubeconfig + "=/tmp/original-kubeconfig",
		}, "")

		assertEnvValue(t, env, envKubeconfig, "/tmp/original-kubeconfig")
		assertEnvValue(t, env, envK8sAPINoop, "false")
		assertEnvValue(t, env, envOCIAPINoop, "false")
	})
}

func writeControllerStub(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "controller-stub.sh")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

func writeKubeconfig(t *testing.T, cfg *clientcmdapi.Config) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NotNil(t, cfg)
	require.NoError(t, clientcmd.WriteToFile(*cfg, path))
	return path
}

func waitForControllerLog(t *testing.T, proc *Process, fragment string) {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	require.NoError(t, proc.WaitForLog(waitCtx, fragment))
}

func assertEnvValue(t *testing.T, environ []string, key string, want string) {
	t.Helper()

	for _, entry := range environ {
		gotKey, gotValue, ok := strings.Cut(entry, "=")
		if ok && gotKey == key {
			assert.Equal(t, want, gotValue)
			return
		}
	}

	t.Fatalf("expected env key %q to exist", key)
}

func assertControllerLogContains(t *testing.T, path string, fragment string) {
	t.Helper()

	require.Eventually(t, func() bool {
		content, err := os.ReadFile(path)
		if err != nil {
			return false
		}

		return strings.Contains(string(content), fragment)
	}, 5*time.Second, 50*time.Millisecond)
}

func assertControllerLogNotContains(t *testing.T, path string, fragment string) {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(content), fragment)
}

type fakeTestLogSink struct {
	mu       sync.Mutex
	logs     []string
	errs     []string
	cleanups []func()
}

func (f *fakeTestLogSink) Helper() {}

func (f *fakeTestLogSink) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cleanups = append(f.cleanups, fn)
}

func (f *fakeTestLogSink) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fakeTestLogSink) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

func (f *fakeTestLogSink) Contains(fragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, entry := range f.logs {
		if strings.Contains(entry, fragment) {
			return true
		}
	}

	return false
}

func (f *fakeTestLogSink) Errors() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.errs...)
}

func (f *fakeTestLogSink) CleanupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.cleanups)
}

func (f *fakeTestLogSink) RunCleanups() {
	f.mu.Lock()
	cleanups := append([]func(){}, f.cleanups...)
	f.cleanups = nil
	f.mu.Unlock()

	for idx := range cleanups {
		cleanups[len(cleanups)-1-idx]()
	}
}
