package services

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type mockShutdownHook struct {
	mock.Mock

	name string
}

func (m *mockShutdownHook) shutdown(ctx context.Context) error {
	ret := m.MethodCalled("shutdown", ctx)
	return ret.Error(0)
}

func (m *mockShutdownHook) shutdownNoCtx() error {
	ret := m.MethodCalled("shutdownNoCtx")
	return ret.Error(0)
}

func TestShutdownHooks(t *testing.T) {
	makeMockDeps := func() ShutdownHooksRegistryDeps {
		return ShutdownHooksRegistryDeps{
			RootLogger:              diag.RootTestLogger(),
			GracefulShutdownTimeout: time.Duration(10+rand.IntN(1000)) * time.Second,
		}
	}

	t.Run("HasHook", func(t *testing.T) {
		t.Run("should return true if such hook has been registered", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)
			hookName := fake.Lorem().Word()
			fn := func(_ context.Context) error { return nil }
			assert.False(t, registry.HasHook(hookName, fn))
			registry.Register(hookName, fn)
			require.True(t, registry.HasHook(hookName, fn))
			assert.False(t, registry.HasHook(fake.Lorem().Word(), func(_ context.Context) error { return nil }))
		})
	})

	t.Run("PerformShutdown", func(t *testing.T) {
		t.Run("should call all hooks", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)

			hooks := []*mockShutdownHook{
				{name: fake.Lorem().Word()},
				{name: fake.Lorem().Word()},
				{name: fake.Lorem().Word()},
			}

			ctx := t.Context()

			for _, hook := range hooks {
				hook.On("shutdown", mock.AnythingOfType("*context.timerCtx")).Return(nil)
				registry.Register(hook.name, hook.shutdown)
			}

			err := registry.PerformShutdown(ctx)
			require.NoError(t, err)

			for _, hook := range hooks {
				hook.AssertExpectations(t)
			}
		})

		t.Run("should call hooks without context", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)

			hooks := []*mockShutdownHook{
				{name: fake.Lorem().Word()},
				{name: fake.Lorem().Word()},
				{name: fake.Lorem().Word()},
			}

			ctx := t.Context()

			for _, hook := range hooks {
				hook.On("shutdownNoCtx").Return(nil)
				registry.RegisterNoCtx(hook.name, hook.shutdownNoCtx)
			}

			err := registry.PerformShutdown(ctx)
			require.NoError(t, err)

			for _, hook := range hooks {
				hook.AssertExpectations(t)
			}
		})

		t.Run("should return error if any hook fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)

			hooks := []*mockShutdownHook{
				{name: fake.Lorem().Word()},
				{name: fake.Lorem().Word()},
				{name: "should-fail-" + fake.Lorem().Word()},
			}

			ctx := t.Context()

			wantErr := errors.New(fake.Lorem().Sentence(10))
			lastHook := hooks[len(hooks)-1]
			lastHook.On("shutdown", mock.AnythingOfType("*context.timerCtx")).Return(wantErr)
			registry.Register(lastHook.name, lastHook.shutdown)

			for _, hook := range hooks[:len(hooks)-1] {
				hook.On("shutdown", mock.AnythingOfType("*context.timerCtx")).Return(nil)
				registry.Register(hook.name, hook.shutdown)
			}

			err := registry.PerformShutdown(ctx)
			require.Error(t, err)

			for _, hook := range hooks {
				hook.AssertExpectations(t)
			}
		})

		t.Run("should return error when shutdown times out", func(t *testing.T) {
			deps := makeMockDeps()
			deps.GracefulShutdownTimeout = time.Millisecond
			registry := NewShutdownHooks(deps)
			registry.Register("slow-hook", func(_ context.Context) error {
				time.Sleep(10 * time.Millisecond)
				return nil
			})

			err := registry.PerformShutdown(t.Context())

			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	})
}
