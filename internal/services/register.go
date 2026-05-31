package services

import (
	"time"

	"go.uber.org/dig"

	"github.com/gemyago/oke-gateway-api/internal/di"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		NewTimeProvider,
		di.ProvideAs[TimeProviderFn, TimeProvider],
		di.ProvideValue(time.NewTicker),
		NewShutdownHooks,
	)
}
