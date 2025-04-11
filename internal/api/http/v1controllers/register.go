package v1controllers

import (
	"github.com/gemyago/golang-backend-boilerplate/internal/di"
	"go.uber.org/dig"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		newEchoController,
		di.ProvideValue(&HealthController{}),
	)
}
