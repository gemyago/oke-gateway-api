//go:build !release

package diag

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/go-logr/logr"
	ctrlog "sigs.k8s.io/controller-runtime/pkg/log"
)

func projectRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

// ProjectPath resolves a path relative to the e2e module root.
func ProjectPath(name string) string {
	return filepath.Join(projectRoot(), name)
}

// OpenProjectLogFile opens a log file in the e2e module root directory.
func OpenProjectLogFile(name string) (*os.File, error) {
	logPath := ProjectPath(name)

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open project log file %q: %w", logPath, err)
	}

	return f, nil
}

func mustOpenProjectLogFile(name string) *os.File {
	f, err := OpenProjectLogFile(name)
	if err != nil {
		panic(err)
	}

	return f
}

var testOutput = mustOpenProjectLogFile("test.log") //nolint:gochecknoglobals // it's ok for tests

var controllerRuntimeLoggerOnce sync.Once //nolint:gochecknoglobals // shared test logger setup

func RootTestLogger() *slog.Logger {
	logger := SetupRootLogger(
		NewRootLoggerOpts().WithOutput(testOutput).WithLogLevel(slog.LevelDebug).WithJSONLogs(true),
	)

	controllerRuntimeLoggerOnce.Do(func() {
		ctrlog.SetLogger(logr.FromSlogHandler(logger.Handler()))
	})

	return logger
}
