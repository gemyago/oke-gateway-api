// coverage-ignore

package k8sapi

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type ConfigDeps struct {
	dig.In

	RootLogger *slog.Logger

	// This can be set via APP_K8SAPI_NOOP env variable
	Noop bool `name:"config.k8sapi.noop"`
}

func newConfig(deps ConfigDeps) (*rest.Config, error) {
	if deps.Noop {
		deps.RootLogger.Warn("Kubernetes API client is in noop mode")
		return &rest.Config{}, nil
	}

	kubeconfig := os.Getenv("KUBECONFIG")

	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func newManager(config *rest.Config) (manager.Manager, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add kubernetes scheme: %w", err)
	}

	if err := types.AddKnownTypes(scheme); err != nil {
		return nil, fmt.Errorf("failed to add gateway api scheme: %w", err)
	}

	if err := gatewayv1.Install(scheme); err != nil {
		return nil, fmt.Errorf("failed to add gateway api scheme: %w", err)
	}

	return manager.New(config, manager.Options{
		Scheme: scheme,
	})
}

func newClient(manager manager.Manager) (client.Client, error) {
	return manager.GetClient(), nil
}
