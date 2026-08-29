package services

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeProvider_Now(t *testing.T) {
	tp := NewTimeProvider()
	now := time.Now()
	got := tp.Now()
	assert.WithinDuration(t, now, got, time.Second)
}

func TestMockNow(t *testing.T) {
	t.Run("returns configured time", func(t *testing.T) {
		mockNow := NewMockNow()
		value := time.Now().Add(10 * time.Minute).Truncate(time.Millisecond)

		mockNow.SetValue(value)

		assert.Equal(t, value, mockNow.Now())
		assert.Equal(t, value, MockNowValue(mockNow))
	})

	t.Run("panics for non mock time provider", func(t *testing.T) {
		require.PanicsWithValue(t, "provided TimeProvider is not a MockNow", func() {
			MockNowValue(NewTimeProvider())
		})
	})
}

func TestRandomString(t *testing.T) {
	t.Run("returns requested length across segment boundaries", func(t *testing.T) {
		for _, length := range []int{1, randomStringSegmentLength, randomStringSegmentLength + 1} {
			value := RandomString(length)

			require.Len(t, value, length)
			assert.Empty(t, strings.Trim(value, "0123456789"))
		}
	})
}
