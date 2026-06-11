package diag

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestSetupRootLogger(t *testing.T) {
	t.Parallel()

	t.Run("writes text logs by default", func(t *testing.T) {
		t.Parallel()

		var output bytes.Buffer
		logger := SetupRootLogger(NewRootLoggerOpts().WithOutput(&output))

		logger.Info("hello")

		logOutput := output.String()
		assertContains(t, logOutput, "level=INFO")
		assertContains(t, logOutput, "msg=hello")
	})

	t.Run("writes json logs when enabled", func(t *testing.T) {
		t.Parallel()

		var output bytes.Buffer
		logger := SetupRootLogger(NewRootLoggerOpts().WithOutput(&output).WithJSONLogs(true))

		logger.Info("hello")

		logOutput := output.String()
		assertContains(t, logOutput, `"level":"INFO"`)
		assertContains(t, logOutput, `"msg":"hello"`)
	})
}

func TestSetLogAttributesToContext(t *testing.T) {
	t.Parallel()

	correlationID := "cid-123"

	var output bytes.Buffer
	logger := SetupRootLogger(NewRootLoggerOpts().WithOutput(&output).WithJSONLogs(true))
	ctx := SetLogAttributesToContext(context.Background(), LogAttributes{
		CorrelationID: slog.StringValue(correlationID),
	})

	logger.InfoContext(ctx, "request complete")

	assertContains(t, output.String(), `"correlationId":"`+correlationID+`"`)
}

func TestWithGroupAddsFlatGroupAttribute(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(
		SetupRootLogger(NewRootLoggerOpts().WithOutput(&output).WithJSONLogs(true)).Handler().WithGroup("probe"),
	)

	logger.Info("hello")

	logOutput := output.String()
	assertContains(t, logOutput, `"group":"probe"`)
	if strings.Contains(logOutput, `"probe":{"`) {
		t.Fatalf("expected group to stay flat in log output, got %q", logOutput)
	}
}

func assertContains(t *testing.T, text string, want string) {
	t.Helper()

	if !strings.Contains(text, want) {
		t.Fatalf("expected %q to contain %q", text, want)
	}
}
