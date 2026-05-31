package http

import (
	"errors"

	"go.uber.org/dig"

	"github.com/gemyago/oke-gateway-api/internal/di"
)

func Register(container *dig.Container) error {
	return errors.Join(
		di.ProvideAll(container,
			NewRootHandler,
		),
	)
}
