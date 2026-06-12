package k8sapi

import (
	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/gemyago/oke-gateway-api/internal/di"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		newConfig,
		newManager,
		di.ProvideAs[*controllerManager, manager.Manager],
		newClient,
		di.ProvideAs[*controllerClient, client.Client],
	)
}
