package http

import (
	"errors"

	"github.com/gemyago/oke-gateway-api/internal/di"
	"go.uber.org/dig"
)

func Register(container *dig.Container) error {
	return errors.Join(
		di.ProvideAll(container,
			NewRootHandler,
		),
	)
}
