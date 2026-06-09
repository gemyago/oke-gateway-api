package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
)

const (
	defaultCheckTimeout = 2 * time.Minute

	gatewayConfigCRDName = "gateway-configs.oke-gateway-api.gemyago.github.io"
)

type kubernetesReader interface {
	Get(context.Context, ctrlclient.ObjectKey, ctrlclient.Object, ...ctrlclient.GetOption) error
	List(context.Context, ctrlclient.ObjectList, ...ctrlclient.ListOption) error
}

type loadBalancerInspector interface {
	Inspect(context.Context, string) (*e2eoci.DisposableLoadBalancer, error)
}

type runtimeDeps struct {
	loadConfig               func() (*config.Config, error)
	newKubernetesClient      func(config.KubernetesConfig) (kubernetesReader, error)
	newLoadBalancerInspector func(config.OCIConfig, *slog.Logger) (loadBalancerInspector, error)
}

func main() {
	logger := diag.SetupRootLogger(
		diag.NewRootLoggerOpts().
			WithLogLevel(slog.LevelInfo),
	)

	ctx, cancel := context.WithTimeout(context.Background(), defaultCheckTimeout)
	err := run(ctx, logger, newRuntimeDeps())
	cancel()
	if err != nil {
		logger.Error("e2e preflight failed", diag.ErrAttr(err))
		os.Exit(1)
	}
}

func newRuntimeDeps() runtimeDeps {
	return runtimeDeps{
		loadConfig: func() (*config.Config, error) {
			return loadCheckConfig(os.LookupEnv, os.Stat)
		},
		newKubernetesClient: func(cfg config.KubernetesConfig) (kubernetesReader, error) {
			client, err := e2ek8s.NewClient(cfg, nil)
			if err != nil {
				return nil, err
			}

			return client.Client, nil
		},
		newLoadBalancerInspector: func(
			cfg config.OCIConfig,
			logger *slog.Logger,
		) (loadBalancerInspector, error) {
			client, err := e2eoci.NewLoadBalancerClient(cfg, nil)
			if err != nil {
				return nil, err
			}

			return e2eoci.NewLoadBalancerCleaner(client, logger, nil), nil
		},
	}
}

func run(ctx context.Context, logger *slog.Logger, deps runtimeDeps) error {
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load preflight configuration: %w", err)
	}

	logger.InfoContext(ctx, "starting e2e preflight checks", slogAttrsToAny(cfg.LogAttrs())...)

	kubernetesErr := runKubernetesChecks(
		ctx,
		logger.WithGroup("kubernetes"),
		cfg.Kubernetes,
		deps.newKubernetesClient,
	)
	if kubernetesErr != nil {
		return kubernetesErr
	}

	ociErr := runOCIChecks(
		ctx,
		logger.WithGroup("oci"),
		cfg.OCI,
		deps.newLoadBalancerInspector,
	)
	if ociErr != nil {
		return ociErr
	}

	logger.InfoContext(
		ctx,
		"e2e preflight checks passed",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
	)

	return nil
}

func loadCheckConfig(
	lookupEnv func(string) (string, bool),
	stat func(string) (fs.FileInfo, error),
) (*config.Config, error) {
	opts := config.NewLoadOptions()
	if lookupEnv != nil {
		opts.WithLookupEnv(lookupEnv)
	}

	if stat != nil {
		opts.WithStat(stat)
	}

	return config.LoadFromEnv(opts)
}

func runKubernetesChecks(
	ctx context.Context,
	logger *slog.Logger,
	cfg config.KubernetesConfig,
	newClient func(config.KubernetesConfig) (kubernetesReader, error),
) error {
	client, err := newClient(cfg)
	if err != nil {
		return fmt.Errorf("create Kubernetes preflight client: %w", err)
	}

	checkErr := checkKubernetesReadAccess(ctx, client, cfg)
	if checkErr != nil {
		return checkErr
	}

	logger.InfoContext(
		ctx,
		"Kubernetes preflight checks passed",
		slog.String("kubeContext", cfg.Context),
		slog.Bool("kubeconfigSet", cfg.KubeconfigPath != ""),
		slog.String("gatewayConfigCRD", gatewayConfigCRDName),
	)

	return nil
}

func checkKubernetesReadAccess(
	ctx context.Context,
	client kubernetesReader,
	cfg config.KubernetesConfig,
) error {
	var namespaces corev1.NamespaceList
	if err := client.List(ctx, &namespaces, ctrlclient.Limit(1)); err != nil {
		return fmt.Errorf("verify Kubernetes namespace read access for context %q: %w", cfg.Context, err)
	}

	gatewayConfigCRD := &unstructured.Unstructured{}
	gatewayConfigCRD.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})

	if err := client.Get(
		ctx,
		ctrlclient.ObjectKey{Name: gatewayConfigCRDName},
		gatewayConfigCRD,
	); err != nil {
		return fmt.Errorf(
			"verify GatewayConfig CRD %q in context %q: %w",
			gatewayConfigCRDName,
			cfg.Context,
			err,
		)
	}

	return nil
}

func runOCIChecks(
	ctx context.Context,
	logger *slog.Logger,
	cfg config.OCIConfig,
	newInspector func(config.OCIConfig, *slog.Logger) (loadBalancerInspector, error),
) error {
	inspector, err := newInspector(cfg, logger)
	if err != nil {
		return fmt.Errorf("create OCI preflight client: %w", err)
	}

	loadBalancer, err := inspector.Inspect(ctx, cfg.LoadBalancerID)
	if err != nil {
		return fmt.Errorf("inspect OCI load balancer %q: %w", cfg.LoadBalancerID, err)
	}

	logger.InfoContext(
		ctx,
		"OCI preflight checks passed",
		slog.String("loadBalancerID", loadBalancer.ID),
		slog.String("publicIP", loadBalancer.PublicIP),
		slog.String("lifecycleState", string(loadBalancer.LifecycleState)),
	)

	return nil
}

func slogAttrsToAny(attrs []slog.Attr) []any {
	res := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		res = append(res, attr)
	}

	return res
}
