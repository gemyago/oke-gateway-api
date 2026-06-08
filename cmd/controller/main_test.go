package main

import (
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(t *testing.T) {
	fake := faker.New()

	t.Run("start", func(t *testing.T) {
		t.Run("should initialize app", func(t *testing.T) {
			t.Setenv("APP_K8SAPI_NOOP", "true")
			t.Setenv("APP_OCIAPI_NOOP", "true")

			rootCmd := setupCommands()
			rootCmd.SetArgs([]string{"start", "--noop", "--logs-file", "../../test.log"})
			require.NoError(t, rootCmd.Execute())
		})
		t.Run("should fail if bad log level", func(t *testing.T) {
			rootCmd := setupCommands()
			rootCmd.SilenceErrors = true
			rootCmd.SilenceUsage = true
			rootCmd.SetArgs([]string{"start", "--noop", "-l", fake.Lorem().Word(), "--logs-file", "../../test.log"})
			assert.Error(t, rootCmd.Execute())
		})
		t.Run("should fail if unexpected env", func(t *testing.T) {
			rootCmd := setupCommands()
			rootCmd.SilenceErrors = true
			rootCmd.SilenceUsage = true
			rootCmd.SetArgs([]string{"start", "--noop", "-e", fake.Lorem().Word(), "--logs-file", "../../test.log"})
			gotErr := rootCmd.Execute()
			assert.ErrorContains(t, gotErr, "failed to read config")
		})
	})
}
