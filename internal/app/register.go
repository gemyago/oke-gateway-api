package app

import (
	"github.com/gemyago/oke-gateway-api/internal/di"
	"go.uber.org/dig"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		NewEchoService,
		NewGatewayClassController,
	)
}
