package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
)

const (
	envLoadBalancerID      = "OKE_E2E_LOAD_BALANCER_ID"
	envOCIConfigFile       = "OCI_CONFIG_FILE"
	envOCIConfigFileAlt    = "OCI_CLI_CONFIG_FILE"
	envOCIConfigProfile    = "OCI_CLI_PROFILE"
	envOCIConfigProfileAlt = "OCI_CLI_CONFIG_PROFILE"
)

func main() {
	logger := diag.SetupRootLogger(
		diag.NewRootLoggerOpts().
			WithLogLevel(slog.LevelInfo),
	)

	ociConfig, err := loadCleanupOCIConfig()
	if err != nil {
		logger.Error("failed to load OCI cleanup configuration", diag.ErrAttr(err))
		os.Exit(1)
	}

	client, err := e2eoci.NewLoadBalancerClient(ociConfig, nil)
	if err != nil {
		logger.Error("failed to create OCI load balancer client", diag.ErrAttr(err))
		os.Exit(1)
	}

	cleaner := e2eoci.NewLoadBalancerCleaner(client, logger, nil)
	result, err := cleaner.Cleanup(context.Background(), ociConfig.LoadBalancerID)
	if err != nil {
		logger.Error("failed to reset disposable load balancer", diag.ErrAttr(err))
		os.Exit(1)
	}

	logger.Info(
		"disposable load balancer cleanup completed",
		slog.String("publicIP", result.PublicIP),
		slog.Any("deletedListeners", result.DeletedListeners),
		slog.Any("deletedRoutingPolicies", result.DeletedRoutingPolicies),
		slog.Any("deletedBackendSets", result.DeletedBackendSets),
	)
}

func loadCleanupOCIConfig() (config.OCIConfig, error) {
	loadBalancerID, ok := optionalEnv(envLoadBalancerID)
	if !ok {
		return config.OCIConfig{}, fmt.Errorf("%s is required", envLoadBalancerID)
	}

	return config.OCIConfig{
		LoadBalancerID: loadBalancerID,
		ConfigFile:     firstEnv(envOCIConfigFile, envOCIConfigFileAlt),
		ConfigProfile:  firstEnv(envOCIConfigProfile, envOCIConfigProfileAlt),
	}, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value, ok := optionalEnv(key); ok {
			return value
		}
	}

	return ""
}

func optionalEnv(key string) (string, bool) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	return value, true
}
