package services

import (
	"time"

	"go.uber.org/dig"

	"github.com/gemyago/oke-gateway-api/internal/di"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		di.ConstructorWithOpts{
			Constructor: NewTimeProvider,
			Options:     []dig.ProvideOption{dig.As(new(TimeProvider))},
		},
		di.ProvideValue(time.NewTicker),
		NewShutdownHooks,
	)
}
