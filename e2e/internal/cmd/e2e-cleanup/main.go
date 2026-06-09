package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
)

const (
	defaultCleanupTimeout = 20 * time.Minute
	envSkipController     = "OKE_E2E_SKIP_CONTROLLER_START"
)

func main() {
	logger := diag.SetupRootLogger(
		diag.NewRootLoggerOpts().
			WithLogLevel(slog.LevelInfo),
	)

	ctx, cancel := context.WithTimeout(context.Background(), defaultCleanupTimeout)
	err := run(ctx, logger)
	cancel()
	if err != nil {
		logger.Error("cleanup failed", diag.ErrAttr(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadCleanupConfig(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load cleanup configuration: %w", err)
	}

	kubeClient, err := e2ek8s.NewClient(cfg.Kubernetes, nil)
	if err != nil {
		return fmt.Errorf("create Kubernetes cleanup client: %w", err)
	}

	deletedNamespaces, err := e2ek8s.DeleteNamespacesWithPrefix(ctx, kubeClient.Client, cfg.NamespacePrefix)
	if err != nil {
		return fmt.Errorf("delete e2e namespaces: %w", err)
	}

	err = e2ek8s.WaitForNamespacesDeleted(ctx, kubeClient.Client, deletedNamespaces, nil)
	if err != nil {
		return fmt.Errorf("wait for e2e namespace cleanup: %w", err)
	}

	logger.InfoContext(
		ctx,
		"e2e namespace cleanup completed",
		slog.String("namespacePrefix", cfg.NamespacePrefix),
		slog.Any("deletedNamespaces", deletedNamespaces),
	)

	loadBalancerClient, err := e2eoci.NewLoadBalancerClient(cfg.OCI, nil)
	if err != nil {
		return fmt.Errorf("create OCI load balancer client: %w", err)
	}

	cleaner := e2eoci.NewLoadBalancerCleaner(loadBalancerClient, logger, nil)
	result, err := cleaner.Cleanup(ctx, cfg.OCI.LoadBalancerID)
	if err != nil {
		return fmt.Errorf("reset disposable load balancer: %w", err)
	}

	logger.InfoContext(
		ctx,
		"disposable load balancer cleanup completed",
		slog.String("publicIP", result.PublicIP),
		slog.Any("deletedListeners", result.DeletedListeners),
		slog.Any("deletedRoutingPolicies", result.DeletedRoutingPolicies),
		slog.Any("deletedBackendSets", result.DeletedBackendSets),
	)

	return nil
}

func loadCleanupConfig(lookupEnv func(string) (string, bool)) (*config.Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	return config.LoadFromEnv(
		config.NewLoadOptions().WithLookupEnv(func(key string) (string, bool) {
			if key == envSkipController {
				return "true", true
			}

			return lookupEnv(key)
		}),
	)
}
