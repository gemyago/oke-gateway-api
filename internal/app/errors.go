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
