//go:build !release

// TODO: generated code should include "//go:build !release" tags
//go:generate mockgen -typed -package diag -destination mock_slog_handler_test.go log/slog Handler

package diag

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
)

// openTestLogFile will open a log file in a project root directory.
func openTestLogFile() *os.File {
	_, filename, _, _ := runtime.Caller(0) // Will be current file
	testFilePath := filepath.Join(filename, "..", "..", "..", "test.log")
	f, err := os.OpenFile(testFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		err = fmt.Errorf("fail to open log file %q for test logging: %w", testFilePath, err)
		panic(err)
	}
	return f
}

var testOutput = openTestLogFile() //nolint:gochecknoglobals //it's ok for tests

func RootTestLogger() *slog.Logger {
	return SetupRootLogger(
		NewRootLoggerOpts().WithOutput(testOutput),
	)
}
