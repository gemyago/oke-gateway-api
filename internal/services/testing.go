//go:build !release

package services

import (
	"math"
	"strings"
	"time"

	"github.com/jaswdr/faker/v2"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

const randomStringSegmentLength = 32

type MockNow struct {
	value time.Time
}

var _ TimeProvider = &MockNow{}

func NewMockNow() *MockNow {
	return &MockNow{
		value: time.UnixMilli(faker.New().Time().Unix(time.Now())),
	}
}

func (m *MockNow) SetValue(t time.Time) {
	m.value = t
}

func (m *MockNow) Now() time.Time {
	return m.value
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
	segmentsCount := math.Ceil(float64(length) / randomStringSegmentLength)
	segments := make([]string, int(segmentsCount))
	for i := range segments {
		segments[i] = faker.New().Numerify("################################")
	}
	return strings.Join(segments, "")[:length]
}
