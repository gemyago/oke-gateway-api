package e2ek8s

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

const defaultClientUserAgent = "oke-gateway-api-e2e"

type ClientFactoryOptions struct {
	buildConfig func(config.KubernetesConfig) (*rest.Config, error)
	newClient   func(*rest.Config, ctrlclient.Options) (*RuntimeClient, error)
	newScheme   func() (*runtime.Scheme, error)
}

type RuntimeClient struct {
	ctrlclient.WithWatch

	Client ctrlclient.WithWatch

	Scheme *runtime.Scheme
}

func NewClientFactoryOptions() *ClientFactoryOptions {
	return &ClientFactoryOptions{
		buildConfig: buildRESTConfig,
		newClient:   newControllerRuntimeClient,
		newScheme:   NewScheme,
	}
}

func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	for _, addToScheme := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		discoveryv1.AddToScheme,
		gatewayv1.Install,
	} {
		if err := addToScheme(scheme); err != nil {
			return nil, fmt.Errorf("register Kubernetes scheme: %w", err)
		}
	}

	return scheme, nil
}

func NewClient(cfg config.KubernetesConfig, opts *ClientFactoryOptions) (*RuntimeClient, error) {
	if opts == nil {
		opts = NewClientFactoryOptions()
	}

	scheme, err := opts.newScheme()
	if err != nil {
		return nil, fmt.Errorf("build scheme: %w", err)
	}

	restCfg, err := opts.buildConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf(
			"build Kubernetes REST config from kubeconfig %q with context %q: %w",
			cfg.KubeconfigPath,
			cfg.Context,
			err,
		)
	}

	restCfg.UserAgent = defaultClientUserAgent

	client, err := opts.newClient(restCfg, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}

	return client, nil
}

func buildRESTConfig(cfg config.KubernetesConfig) (*rest.Config, error) {
	loader := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.KubeconfigPath != "" {
		loader.ExplicitPath = cfg.KubeconfigPath
	}

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loader,
		&clientcmd.ConfigOverrides{
			CurrentContext: cfg.Context,
		},
	).ClientConfig()
}

func newControllerRuntimeClient(
	cfg *rest.Config,
	options ctrlclient.Options,
) (*RuntimeClient, error) {
	client, err := ctrlclient.NewWithWatch(cfg, options)
	if err != nil {
		return nil, err
	}

	return &RuntimeClient{
		WithWatch: client,
		Client:    client,
		Scheme:    options.Scheme,
	}, nil
}
