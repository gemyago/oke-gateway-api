package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	t.Run("should load local config with default opts", func(t *testing.T) {
		cfg := New()
		err := Load(cfg, NewLoadOpts())
		require.NoError(t, err)

		require.Equal(t, "DEBUG", cfg.GetString("defaultLogLevel"))
	})
	t.Run("should fail if no default config is found", func(t *testing.T) {
		opts := NewLoadOpts()
		opts.defaultConfigFileName = "not-existing.json"
		cfg := New()
		err := Load(cfg, opts)
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("should load env specific config", func(t *testing.T) {
		cfg := New()
		err := Load(cfg, NewLoadOpts().WithEnv("test"))
		require.NoError(t, err)

		require.Equal(t, "DEBUG", cfg.GetString("defaultLogLevel"))
	})
	t.Run("should return error if config is not found", func(t *testing.T) {
		cfg := New()
		err := Load(cfg, NewLoadOpts().WithEnv("not-existing"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}
