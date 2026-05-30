package k8sapi

import (
	"go.uber.org/dig"

	"github.com/gemyago/oke-gateway-api/internal/di"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		newConfig,
		newManager,
		newClient,
	)
}
