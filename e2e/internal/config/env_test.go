package config

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromEnv(t *testing.T) {
	t.Parallel()

	t.Run("loads required and optional values", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadFromEnv(
			NewLoadOptions().
				WithLookupEnv(lookupEnvFromMap(map[string]string{
					envLoadBalancerID:      "ocid1.loadbalancer.oc1..example",
					envKubeconfig:          "/tmp/kubeconfig",
					envNamespacePrefix:     "suite-",
					envGatewayClassName:    "custom-class",
					envHTTPPort:            "8080",
					envControllerBin:       "/tmp/controller",
					envSkipController:      "true",
					envOCIConfigFileAlt:    "/tmp/oci-config",
					envOCIConfigProfileAlt: "DEFAULT",
				})).
				WithStat(func(name string) (fs.FileInfo, error) {
					assertEqual(t, "/tmp/controller", name)
					return fakeFileInfo{name: filepath.Base(name)}, nil
				}),
		)
		assertNoError(t, err)
		assertEqual(t, "suite-", cfg.NamespacePrefix)
		assertEqual(t, "custom-class", cfg.GatewayClassName)
		assertEqual(t, 8080, cfg.HTTPPort)
		assertEqual(t, "/tmp/kubeconfig", cfg.Kubernetes.KubeconfigPath)
		assertEqual(t, "ocid1.loadbalancer.oc1..example", cfg.OCI.LoadBalancerID)
		assertEqual(t, "/tmp/oci-config", cfg.OCI.ConfigFile)
		assertEqual(t, "DEFAULT", cfg.OCI.ConfigProfile)
		assertEqual(t, "/tmp/controller", cfg.Controller.BinPath)
		assertTrue(t, cfg.Controller.SkipStart)
	})

	t.Run("uses defaults and preferred oci env names", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadFromEnv(
			NewLoadOptions().
				WithLookupEnv(lookupEnvFromMap(map[string]string{
					envLoadBalancerID:      "ocid1.loadbalancer.oc1..example",
					envKubeconfig:          "/tmp/kubeconfig",
					envOCIConfigFile:       "/tmp/primary-config",
					envOCIConfigFileAlt:    "/tmp/fallback-config",
					envOCIConfigProfile:    "PRIMARY",
					envOCIConfigProfileAlt: "FALLBACK",
				})).
				WithStat(func(name string) (fs.FileInfo, error) {
					assertEqual(t, defaultControllerBin, name)
					return fakeFileInfo{name: filepath.Base(name)}, nil
				}),
		)
		assertNoError(t, err)
		assertEqual(t, defaultNamespacePrefix, cfg.NamespacePrefix)
		assertEqual(t, defaultGatewayClassName, cfg.GatewayClassName)
		assertEqual(t, defaultHTTPPort, cfg.HTTPPort)
		assertEqual(t, defaultControllerBin, cfg.Controller.BinPath)
		assertFalse(t, cfg.Controller.SkipStart)
		assertEqual(t, "/tmp/primary-config", cfg.OCI.ConfigFile)
		assertEqual(t, "PRIMARY", cfg.OCI.ConfigProfile)
	})

	t.Run("returns clear validation errors", func(t *testing.T) {
		t.Parallel()

		_, err := LoadFromEnv(
			NewLoadOptions().
				WithLookupEnv(lookupEnvFromMap(map[string]string{
					envHTTPPort:       "abc",
					envSkipController: "sometimes",
				})).
				WithStat(func(_ string) (fs.FileInfo, error) {
					return nil, fs.ErrNotExist
				}),
		)
		assertErrorContains(t, err, envLoadBalancerID+" is required")
		assertErrorContains(t, err, envKubeconfig+" is required")
		assertErrorContains(t, err, envHTTPPort+" must be a valid integer")
		assertErrorContains(t, err, envSkipController+" must be a valid boolean")
		assertErrorContains(t, err, envControllerBin+" points to missing file")
	})

	t.Run("skips controller binary validation when skip start is enabled", func(t *testing.T) {
		t.Parallel()

		cfg, err := LoadFromEnv(
			NewLoadOptions().
				WithLookupEnv(lookupEnvFromMap(map[string]string{
					envLoadBalancerID: "ocid1.loadbalancer.oc1..example",
					envKubeconfig:     "/tmp/kubeconfig",
					envControllerBin:  "/tmp/missing-controller",
					envSkipController: "true",
				})).
				WithStat(func(name string) (fs.FileInfo, error) {
					assertEqual(t, "/tmp/missing-controller", name)
					return nil, fs.ErrNotExist
				}),
		)
		assertNoError(t, err)
		assertEqual(t, "/tmp/missing-controller", cfg.Controller.BinPath)
		assertTrue(t, cfg.Controller.SkipStart)
	})

	t.Run("rejects non-positive ports", func(t *testing.T) {
		t.Parallel()

		_, err := LoadFromEnv(
			NewLoadOptions().
				WithLookupEnv(lookupEnvFromMap(map[string]string{
					envLoadBalancerID: "ocid1.loadbalancer.oc1..example",
					envKubeconfig:     "/tmp/kubeconfig",
					envHTTPPort:       "0",
				})).
				WithStat(func(name string) (fs.FileInfo, error) {
					return fakeFileInfo{name: filepath.Base(name)}, nil
				}),
		)
		assertErrorContains(t, err, envHTTPPort+" must be greater than zero")
	})
}

func TestConfigLogAttrs(t *testing.T) {
	t.Parallel()

	cfg := Config{
		NamespacePrefix:  "suite-",
		GatewayClassName: "gw-class",
		HTTPPort:         8080,
		Kubernetes: KubernetesConfig{
			KubeconfigPath: "/tmp/kubeconfig",
		},
		OCI: OCIConfig{
			LoadBalancerID: "ocid1.loadbalancer.oc1..secret",
			ConfigFile:     "/tmp/oci-config",
			ConfigProfile:  "DEFAULT",
		},
		Controller: ControllerConfig{
			BinPath:   "/tmp/controller",
			SkipStart: true,
		},
	}

	attrs := attrsByKey(cfg.LogAttrs())
	assertEqual(t, "suite-", attrs["namespacePrefix"].Value.String())
	assertEqual(t, "gw-class", attrs["gatewayClassName"].Value.String())
	assertEqual(t, int64(8080), attrs["httpPort"].Value.Int64())
	assertEqual(t, "/tmp/controller", attrs["controllerBin"].Value.String())
	assertTrue(t, attrs["skipControllerStart"].Value.Bool())
	assertTrue(t, attrs["kubeconfigSet"].Value.Bool())
	assertTrue(t, attrs["loadBalancerIDSet"].Value.Bool())
	assertTrue(t, attrs["ociConfigFileSet"].Value.Bool())
	assertTrue(t, attrs["ociConfigProfileSet"].Value.Bool())
	_, ok := attrs["loadBalancerID"]
	assertFalse(t, ok)
	_, ok = attrs["kubeconfig"]
	assertFalse(t, ok)
	_, ok = attrs["ociConfigFile"]
	assertFalse(t, ok)
	_, ok = attrs["ociConfigProfile"]
	assertFalse(t, ok)
}

func TestValidateControllerBin(t *testing.T) {
	t.Parallel()

	t.Run("accepts existing file", func(t *testing.T) {
		t.Parallel()

		err := validateControllerBin("/tmp/controller", func(name string) (fs.FileInfo, error) {
			return fakeFileInfo{name: filepath.Base(name)}, nil
		})
		assertNoError(t, err)
	})

	t.Run("rejects directories", func(t *testing.T) {
		t.Parallel()

		err := validateControllerBin("/tmp/controller", func(name string) (fs.FileInfo, error) {
			return fakeDirInfo{name: filepath.Base(name)}, nil
		})
		assertErrorContains(t, err, envControllerBin+" must point to a file")
	})

	t.Run("wraps unexpected stat errors", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		err := validateControllerBin("/tmp/controller", func(_ string) (fs.FileInfo, error) {
			return nil, wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped error %v, got %v", wantErr, err)
		}
		assertErrorContains(t, err, "stat "+envControllerBin)
	})
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string     { return f.name }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

type fakeDirInfo struct {
	name string
}

func (f fakeDirInfo) Name() string     { return f.name }
func (fakeDirInfo) Size() int64        { return 0 }
func (fakeDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (fakeDirInfo) IsDir() bool        { return true }
func (fakeDirInfo) Sys() any           { return nil }

func attrsByKey(attrs []slog.Attr) map[string]slog.Attr {
	res := make(map[string]slog.Attr, len(attrs))
	for _, attr := range attrs {
		res[attr.Key] = attr
	}

	return res
}

func lookupEnvFromMap(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func assertEqual[T comparable](t *testing.T, want T, got T) {
	t.Helper()

	if got != want {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func assertTrue(t *testing.T, got bool) {
	t.Helper()

	if !got {
		t.Fatal("expected true")
	}
}

func assertFalse(t *testing.T, got bool) {
	t.Helper()

	if got {
		t.Fatal("expected false")
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}

	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error %q to contain %q", err.Error(), want)
	}
}
