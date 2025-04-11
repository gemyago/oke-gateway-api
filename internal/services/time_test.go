package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTimeProvider_Now(t *testing.T) {
	tp := NewTimeProvider()
	now := time.Now()
	got := tp.Now()
	assert.WithinDuration(t, now, got, time.Second)
}
