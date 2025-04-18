package main

import (
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/dig"
)

func setupCommands() *cobra.Command {
	container := dig.New()
	rootCmd := newRootCmd(container)
	rootCmd.AddCommand(
		newStartServerCmd(container),
	)
	return rootCmd
}

func main() { // coverage-ignore
	rootCmd := setupCommands()
	if err := rootCmd.Execute(); err != nil {
		// cobra will print error to stdout
		os.Exit(1)
	}
}
