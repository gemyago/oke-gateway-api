package controllerproc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		}))
		require.NoError(t, err)
		require.False(t, proc.Skipped())
		require.NotZero(t, proc.PID())
		require.Equal(t, 1, logSink.CleanupCount())

		require.Eventually(t, func() bool {
			return logSink.Contains("controller stdout: stdout KUBECONFIG=/tmp/controllerproc-kubeconfig") &&
				logSink.Contains("controller stdout: stdout OCI_CONFIG_FILE=/tmp/controllerproc-oci-config") &&
				logSink.Contains("controller stdout: stdout APP_K8SAPI_NOOP=false") &&
				logSink.Contains("controller stderr: stderr APP_OCIAPI_NOOP=false") &&
				logSink.Contains("controller stderr: stderr OCI_CLI_PROFILE=TEAM")
		}, 5*time.Second, 50*time.Millisecond)

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
		assert.True(t, logSink.Contains("controller stdout: stdout received SIGTERM"))
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

	t.Run("forced stop accepts the expected signaled wait result after kill", func(t *testing.T) {
		t.Parallel()

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
			WithStopTimeout(100*time.Millisecond))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return logSink.Contains("controller stdout: ready")
		}, 5*time.Second, 50*time.Millisecond)

		logSink.RunCleanups()
		assert.Empty(t, logSink.Errors())
		assert.True(t, logSink.Contains("did not stop within 100ms; killing"))
		require.NoError(t, proc.Stop())
	})
}

func TestBuildControllerEnv(t *testing.T) {
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
	})

	assertEnvValue(t, env, envKubeconfig, "/tmp/test-kubeconfig")
	assertEnvValue(t, env, envOCIConfigFile, "/tmp/test-oci-config")
	assertEnvValue(t, env, envOCIConfigFileAlt, "/tmp/test-oci-config")
	assertEnvValue(t, env, envOCIConfigProfile, "PRIMARY")
	assertEnvValue(t, env, envOCIConfigProfileAlt, "PRIMARY")
	assertEnvValue(t, env, envK8sAPINoop, "false")
	assertEnvValue(t, env, envOCIAPINoop, "false")
}

func writeControllerStub(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "controller-stub.sh")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
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
