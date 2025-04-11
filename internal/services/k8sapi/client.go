package k8sapi

import (
	"flag"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	return client.New(config, client.Options{})
}
