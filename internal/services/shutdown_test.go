package services

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/gemyago/golang-backend-boilerplate/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockShutdownHook struct {
	name string
	mock.Mock
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
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)
			hookName := faker.Word()
			fn := func(_ context.Context) error { return nil }
			assert.False(t, registry.HasHook(hookName, fn))
			registry.Register(hookName, fn)
			require.True(t, registry.HasHook(hookName, fn))
			assert.False(t, registry.HasHook(faker.Word(), func(_ context.Context) error { return nil }))
		})
	})

	t.Run("PerformShutdown", func(t *testing.T) {
		t.Run("should call all hooks", func(t *testing.T) {
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)

			hooks := []*mockShutdownHook{
				{name: faker.Word()},
				{name: faker.Word()},
				{name: faker.Word()},
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
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)

			hooks := []*mockShutdownHook{
				{name: faker.Word()},
				{name: faker.Word()},
				{name: faker.Word()},
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
			deps := makeMockDeps()
			registry := NewShutdownHooks(deps)

			hooks := []*mockShutdownHook{
				{name: faker.Word()},
				{name: faker.Word()},
				{name: "should-fail-" + faker.Word()},
			}

			ctx := t.Context()

			wantErr := errors.New(faker.Sentence())
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
	})
}
