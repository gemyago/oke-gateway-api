package app

import "fmt"

type resourceStatusError struct {
	conditionType string
	reason        string
	message       string
	cause         error
}

func (e resourceStatusError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf(
			"resourceStatusError: type=%s, reason=%s, message=%s, cause=%s",
			e.conditionType, e.reason, e.message, e.cause)
	}
	return fmt.Sprintf("resourceStatusError: type=%s, reason=%s, message=%s", e.conditionType, e.reason, e.message)
}

type ReconcileError struct {
	message   string
	retriable bool
	cause     error
}

func (e ReconcileError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("reconcileError: retriable=%t, message=%s, cause=%s", e.retriable, e.message, e.cause)
	}
	return fmt.Sprintf("reconcileError: retriable=%t, message=%s", e.retriable, e.message)
}

func (e ReconcileError) IsRetriable() bool {
	return e.retriable
}

func NewReconcileError(message string, retriable bool) *ReconcileError {
	return &ReconcileError{
		retriable: retriable,
		message:   message,
	}
}

func NewReconcileErrorWithCause(message string, retriable bool, cause error) *ReconcileError {
	return &ReconcileError{
		retriable: retriable,
		message:   message,
		cause:     cause,
	}
}
