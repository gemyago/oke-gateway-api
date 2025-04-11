package k8sapi

import (
	"flag"
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func newConfig() (*rest.Config, error) {
	// create new config
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String(
			"kubeconfig",
			filepath.Join(home, ".kube", "config"),
			"(optional) absolute path to the kubeconfig file",
		)
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	return clientcmd.BuildConfigFromFlags("", *kubeconfig)
}

func newClient(config *rest.Config) (client.Client, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add kubernetes scheme: %w", err)
	}

	if err := gatewayv1.Install(scheme); err != nil {
		return nil, fmt.Errorf("failed to add gateway api scheme: %w", err)
	}

	return client.New(config, client.Options{
		Scheme: scheme,
	})
}
