package di

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/dig"
)

func TestDig(t *testing.T) {
	type DepA struct{}
	type DepB struct{}

	t.Run("ProvideAll", func(t *testing.T) {
		t.Run("should provide constructors", func(t *testing.T) {
			container := dig.New()
			if err := ProvideAll(container,
				func() DepA { return DepA{} },
				func() DepB { return DepB{} },
			); !assert.NoError(t, err) {
				return
			}

			if err := container.Invoke(func(a DepA, b DepB) {
				assert.NotNil(t, a)
				assert.NotNil(t, b)
			}); !assert.NoError(t, err) {
				return
			}
		})

		t.Run("should provide values", func(t *testing.T) {
			container := dig.New()
			val1 := DepA{}
			val2 := DepB{}
			if err := ProvideAll(container,
				ProvideValue(val1),
				ProvideValue(val2),
			); !assert.NoError(t, err) {
				return
			}

			if err := container.Invoke(func(a DepA, b DepB) {
				assert.Equal(t, val1, a)
				assert.Equal(t, val2, b)
			}); !assert.NoError(t, err) {
				return
			}
		})

		t.Run("should handle errors", func(t *testing.T) {
			container := dig.New()
			if err := ProvideAll(container,
				func() DepA { return DepA{} },
				func() DepA { return DepA{} },
			); !assert.Error(t, err) {
				return
			}

			if err := ProvideAll(container,
				ProvideValue(DepA{}),
				ProvideValue(DepA{}),
			); !assert.Error(t, err) {
				return
			}
		})
	})

	t.Run("ProvideWithArgErr", func(t *testing.T) {
		container := dig.New()
		constructor := func(_ context.Context, _ DepA) (DepB, error) {
			return DepB{}, nil
		}
		ctx := t.Context()
		if err := ProvideAll(container,
			ProvideValue(DepA{}),
			ProvideWithArgErr(ctx, constructor),
		); !assert.NoError(t, err) {
			return
		}

		if err := container.Invoke(func(b DepB) {
			assert.NotNil(t, b)
		}); !assert.NoError(t, err) {
			return
		}
	})

	t.Run("ProvideWithArg", func(t *testing.T) {
		container := dig.New()
		constructor := func(_ context.Context, _ DepA) DepB {
			return DepB{}
		}
		ctx := t.Context()
		if err := ProvideAll(container,
			ProvideValue(DepA{}),
			ProvideWithArg(ctx, constructor),
		); !assert.NoError(t, err) {
			return
		}

		if err := container.Invoke(func(b DepB) {
			assert.NotNil(t, b)
		}); !assert.NoError(t, err) {
			return
		}
	})

	t.Run("ProvideAs", func(t *testing.T) {
		t.Run("should provide one type as another", func(t *testing.T) {
			container := dig.New()
			type DepA fmt.Stringer
			type DepB fmt.Stringer

			var dep1Val DepA = time.Now()

			err := ProvideAll(container,
				func() DepA { return dep1Val },
				ProvideAs[DepA, DepB],
			)
			require.NoError(t, err)

			if err = container.Invoke(func(b DepB) {
				assert.Equal(t, dep1Val, b)
			}); !assert.NoError(t, err) {
				return
			}
		})

		t.Run("should fail if types are not compatible", func(t *testing.T) {
			container := dig.New()
			type DepA fmt.Stringer
			type DepB http.Handler

			var dep1Val DepA = time.Now()

			err := ProvideAll(container,
				func() DepA { return dep1Val },
				ProvideAs[DepA, DepB],
			)
			require.NoError(t, err)

			err = container.Invoke(func(_ DepB) {
				assert.Fail(t, "should not be called")
			})
			require.Error(t, err)
		})
	})
}
