//go:build !release

package services

import (
	"math"
	"strings"
	"time"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
)

type MockNow struct {
	value time.Time
}

var _ TimeProvider = &MockNow{}

func (m *MockNow) SetValue(t time.Time) {
	m.value = t
}

func (m *MockNow) Now() time.Time {
	return m.value
}

func NewMockNow() *MockNow {
	return &MockNow{
		value: time.UnixMilli(faker.RandomUnixTime()),
	}
}

func MockNowValue(p TimeProvider) time.Time {
	mp, ok := p.(*MockNow)
	if !ok {
		panic("provided TimeProvider is not a MockNow")
	}
	return mp.value
}

const defaultTestShutdownTimeout = 30 * time.Second

func NewTestShutdownHooks() *ShutdownHooks {
	return NewShutdownHooks(ShutdownHooksRegistryDeps{
		RootLogger:              diag.RootTestLogger(),
		GracefulShutdownTimeout: defaultTestShutdownTimeout,
	})
}

func RandomString(length int) string {
	segmentsCount := math.Ceil(float64(length) / 32)
	segments := make([]string, int(segmentsCount))
	for i := range segments {
		segments[i] = faker.UUIDDigit()
	}
	return strings.Join(segments, "")[:length]
}
