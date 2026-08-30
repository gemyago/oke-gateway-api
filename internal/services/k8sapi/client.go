package k8sapi

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

type controllerManager struct {
	manager.Manager
}

type controllerClient struct {
	client.Client
}

type ConfigDeps struct {
	dig.In

	RootLogger *slog.Logger

	// This can be set via APP_K8SAPI_NOOP env variable
	Noop      bool `name:"config.k8sapi.noop"`
	InCluster bool `name:"config.k8sapi.inCluster"`
}

func newConfig(deps ConfigDeps) (*rest.Config, error) {
	if deps.Noop {
		deps.RootLogger.Warn("Kubernetes API client is in noop mode")
		return &rest.Config{}, nil
	}

	if deps.InCluster {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
		}
		return cfg, nil
	}

	kubeconfig := os.Getenv("KUBECONFIG")

	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func newManager(config *rest.Config) (*controllerManager, error) {
	return newManagerWithSchemeInstallers(config, []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		types.AddKnownTypes,
		gatewayv1.Install,
		gatewayv1beta1.Install,
	})
}

func newManagerWithSchemeInstallers(
	config *rest.Config,
	installers []func(*runtime.Scheme) error,
) (*controllerManager, error) {
	scheme := runtime.NewScheme()
	errorMessages := []string{
		"failed to add kubernetes scheme",
		"failed to add gateway api scheme",
		"failed to add gateway api scheme",
		"failed to add gateway api v1beta1 scheme",
	}

	for idx, installer := range installers {
		if err := installer(scheme); err != nil {
			return nil, fmt.Errorf("%s: %w", errorMessages[idx], err)
		}
	}

	mgr, err := manager.New(config, manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}
	return &controllerManager{Manager: mgr}, nil
}

func newClient(manager *controllerManager) *controllerClient {
	return &controllerClient{Client: manager.GetClient()}
}
