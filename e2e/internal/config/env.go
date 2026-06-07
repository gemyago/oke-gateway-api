package config

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

const (
	defaultNamespacePrefix  = "oke-gw-e2e-"
	defaultGatewayClassName = "oke-gateway-api-e2e"
	defaultHTTPPort         = 80
	defaultControllerBin    = "../dist/bin/controller"
)

const (
	envLoadBalancerID      = "OKE_E2E_LOAD_BALANCER_ID"
	envKubeconfig          = "KUBECONFIG"
	envNamespacePrefix     = "OKE_E2E_NAMESPACE_PREFIX"
	envGatewayClassName    = "OKE_E2E_GATEWAY_CLASS_NAME"
	envHTTPPort            = "OKE_E2E_HTTP_PORT"
	envControllerBin       = "OKE_E2E_CONTROLLER_BIN"
	envSkipController      = "OKE_E2E_SKIP_CONTROLLER_START"
	envOCIConfigFile       = "OCI_CONFIG_FILE"
	envOCIConfigFileAlt    = "OCI_CLI_CONFIG_FILE"
	envOCIConfigProfile    = "OCI_CLI_PROFILE"
	envOCIConfigProfileAlt = "OCI_CLI_CONFIG_PROFILE"
)

type Config struct {
	NamespacePrefix  string
	GatewayClassName string
	HTTPPort         int
	Kubernetes       KubernetesConfig
	OCI              OCIConfig
	Controller       ControllerConfig
}

type KubernetesConfig struct {
	KubeconfigPath string
}

type OCIConfig struct {
	LoadBalancerID string
	ConfigFile     string
	ConfigProfile  string
}

type ControllerConfig struct {
	BinPath   string
	SkipStart bool
}

func (cfg Config) LogAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("namespacePrefix", cfg.NamespacePrefix),
		slog.String("gatewayClassName", cfg.GatewayClassName),
		slog.Int("httpPort", cfg.HTTPPort),
		slog.String("controllerBin", cfg.Controller.BinPath),
		slog.Bool("skipControllerStart", cfg.Controller.SkipStart),
		slog.Bool("kubeconfigSet", cfg.Kubernetes.KubeconfigPath != ""),
		slog.Bool("loadBalancerIDSet", cfg.OCI.LoadBalancerID != ""),
		slog.Bool("ociConfigFileSet", cfg.OCI.ConfigFile != ""),
		slog.Bool("ociConfigProfileSet", cfg.OCI.ConfigProfile != ""),
	}
}

type LoadOptions struct {
	lookupEnv func(string) (string, bool)
	stat      func(string) (fs.FileInfo, error)
}

func NewLoadOptions() *LoadOptions {
	return &LoadOptions{
		lookupEnv: os.LookupEnv,
		stat:      os.Stat,
	}
}

func (opts *LoadOptions) WithLookupEnv(fn func(string) (string, bool)) *LoadOptions {
	opts.lookupEnv = fn
	return opts
}

func (opts *LoadOptions) WithStat(fn func(string) (fs.FileInfo, error)) *LoadOptions {
	opts.stat = fn
	return opts
}

func LoadFromEnv(opts *LoadOptions) (*Config, error) {
	if opts == nil {
		opts = NewLoadOptions()
	}

	cfg := &Config{
		NamespacePrefix:  defaultNamespacePrefix,
		GatewayClassName: defaultGatewayClassName,
		HTTPPort:         defaultHTTPPort,
		Kubernetes:       KubernetesConfig{},
		OCI:              OCIConfig{},
		Controller: ControllerConfig{
			BinPath: defaultControllerBin,
		},
	}

	problems := validationProblems{}

	cfg.OCI.LoadBalancerID = requiredEnv(opts.lookupEnv, envLoadBalancerID, &problems)
	cfg.Kubernetes.KubeconfigPath = firstEnv(opts.lookupEnv, envKubeconfig)

	if value, ok := optionalEnv(opts.lookupEnv, envNamespacePrefix); ok {
		cfg.NamespacePrefix = value
	}

	if value, ok := optionalEnv(opts.lookupEnv, envGatewayClassName); ok {
		cfg.GatewayClassName = value
	}

	if value, ok := optionalEnv(opts.lookupEnv, envHTTPPort); ok {
		httpPort, err := strconv.Atoi(value)
		switch {
		case err != nil:
			problems.addf("%s must be a valid integer: %v", envHTTPPort, err)
		case httpPort <= 0:
			problems.addf("%s must be greater than zero", envHTTPPort)
		default:
			cfg.HTTPPort = httpPort
		}
	}

	if value, ok := optionalEnv(opts.lookupEnv, envControllerBin); ok {
		cfg.Controller.BinPath = value
	}

	if value, ok := optionalEnv(opts.lookupEnv, envSkipController); ok {
		skipStart, err := strconv.ParseBool(value)
		switch {
		case err != nil:
			problems.addf("%s must be a valid boolean: %v", envSkipController, err)
		default:
			cfg.Controller.SkipStart = skipStart
		}
	}

	cfg.OCI.ConfigFile = firstEnv(opts.lookupEnv, envOCIConfigFile, envOCIConfigFileAlt)
	cfg.OCI.ConfigProfile = firstEnv(opts.lookupEnv, envOCIConfigProfile, envOCIConfigProfileAlt)

	if !cfg.Controller.SkipStart {
		if err := validateControllerBin(cfg.Controller.BinPath, opts.stat); err != nil {
			problems.addf("%v", err)
		}
	}

	if err := problems.err(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validateControllerBin(controllerBin string, stat func(string) (fs.FileInfo, error)) error {
	info, err := stat(controllerBin)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s must point to a file, got directory %q", envControllerBin, controllerBin)
		}

		return nil
	}

	if os.IsNotExist(err) {
		return fmt.Errorf(
			"%s points to missing file %q; build the controller binary first with `direnv exec . make dist/bin`",
			envControllerBin,
			controllerBin,
		)
	}

	return fmt.Errorf("stat %s at %q: %w", envControllerBin, controllerBin, err)
}

func requiredEnv(lookupEnv func(string) (string, bool), key string, problems *validationProblems) string {
	value, ok := optionalEnv(lookupEnv, key)
	if !ok {
		problems.addf("%s is required", key)
		return ""
	}

	return value
}

func optionalEnv(lookupEnv func(string) (string, bool), key string) (string, bool) {
	value, ok := lookupEnv(key)
	if !ok {
		return "", false
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	return value, true
}

func firstEnv(lookupEnv func(string) (string, bool), keys ...string) string {
	for _, key := range keys {
		if value, ok := optionalEnv(lookupEnv, key); ok {
			return value
		}
	}

	return ""
}

type validationProblems struct {
	problems []string
}

func (v *validationProblems) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validationProblems) err() error {
	if len(v.problems) == 0 {
		return nil
	}

	return fmt.Errorf("invalid e2e environment: %s", strings.Join(v.problems, "; "))
}
