package services

import "time"

type TimeProvider interface {
	Now() time.Time
}

// TimeProviderFn adapts a function into a TimeProvider.
type TimeProviderFn func() time.Time

func NewTimeProvider() TimeProviderFn {
	return TimeProviderFn(time.Now)
}

func (fn TimeProviderFn) Now() time.Time {
	return fn()
}
