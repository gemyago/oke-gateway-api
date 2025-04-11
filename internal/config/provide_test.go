package config

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/gemyago/golang-backend-boilerplate/internal/di"
	"github.com/go-faker/faker/v4"
	"github.com/samber/lo"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/dig"
)

func Test_provideConfigValue(t *testing.T) {
	t.Run("should provide config value as int", func(t *testing.T) {
		cfg := viper.New()
		intCfgKey := "int-cfg-key"
		cfg.Set(intCfgKey, rand.IntN(1000))

		int32CfgKey := "int-32-cfg-key"
		cfg.Set(int32CfgKey, rand.Int32N(1000))

		type configReceiver struct {
			dig.In
			IntVal   int   `name:"config.int-cfg-key"`
			Int32Val int32 `name:"config.int-32-cfg-key"`
		}

		container := dig.New()
		require.NoError(t, di.ProvideAll(container,
			provideConfigValue(cfg, intCfgKey).asInt(),
			provideConfigValue(cfg, int32CfgKey).asInt32(),
		))

		require.NoError(t, container.Invoke(func(receiver configReceiver) {
			require.Equal(t, cfg.GetInt(intCfgKey), receiver.IntVal)
			require.Equal(t, cfg.GetInt32(int32CfgKey), receiver.Int32Val)
		}))
	})

	t.Run("should provide config value as string", func(t *testing.T) {
		cfg := viper.New()
		configKey := "string-cfg"
		cfg.Set(configKey, faker.Sentence())

		type configReceiver struct {
			dig.In
			Value string `name:"config.string-cfg"`
		}
		container := dig.New()
		require.NoError(t, di.ProvideAll(container, provideConfigValue(cfg, configKey).asString()))
		require.NoError(t, container.Invoke(func(receiver configReceiver) {
			require.Equal(t, cfg.GetString(configKey), receiver.Value)
		}))
	})

	t.Run("should provide config value as bool", func(t *testing.T) {
		cfg := viper.New()
		configKey := "bool-cfg"
		cfg.Set(configKey, lo.If(rand.IntN(2) == 1, true).Else(false))
		type configReceiver struct {
			dig.In
			Value bool `name:"config.bool-cfg"`
		}
		container := dig.New()
		require.NoError(t, di.ProvideAll(container, provideConfigValue(cfg, configKey).asBool()))
		require.NoError(t, container.Invoke(func(receiver configReceiver) {
			require.Equal(t, cfg.GetBool(configKey), receiver.Value)
		}))
	})

	t.Run("should provide config value as duration", func(t *testing.T) {
		cfg := viper.New()
		configKey := "duration-cfg"
		cfg.Set(configKey, rand.IntN(1000))
		type configReceiver struct {
			dig.In
			Value time.Duration `name:"config.duration-cfg"`
		}
		container := dig.New()
		require.NoError(t, di.ProvideAll(container, provideConfigValue(cfg, configKey).asDuration()))
		require.NoError(t, container.Invoke(func(receiver configReceiver) {
			require.Equal(t, cfg.GetDuration(configKey), receiver.Value)
		}))
	})

	t.Run("should panic if config key is not found", func(t *testing.T) {
		cfg := viper.New()
		configKey := "int-cfg-key"
		container := dig.New()
		assert.Panics(t, func() {
			require.NoError(t, di.ProvideAll(container, provideConfigValue(cfg, configKey).asInt()))
		})
	})
}
