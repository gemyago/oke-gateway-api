package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestDriftRequeue(t *testing.T) {
	t.Run("returns empty result when interval is disabled", func(t *testing.T) {
		assert.Equal(t, reconcile.Result{}, driftRequeue(0))
		assert.Equal(t, reconcile.Result{}, driftRequeue(-time.Second))
	})

	t.Run("returns requeue after when interval is enabled", func(t *testing.T) {
		interval := 5 * time.Minute

		assertDriftRequeue(t, driftRequeue(interval), interval)
	})

	t.Run("enforces minimum positive interval", func(t *testing.T) {
		assertDriftRequeue(t, driftRequeue(time.Second), minimumDriftRequeueInterval)
	})
}

func TestShouldSetPendingForReconcile(t *testing.T) {
	t.Run("sets pending when programming is required", func(t *testing.T) {
		assert.True(t, shouldSetPendingForReconcile(true, 2*time.Minute))
	})

	t.Run("sets pending when drift reconciliation is disabled", func(t *testing.T) {
		assert.True(t, shouldSetPendingForReconcile(false, 0))
		assert.True(t, shouldSetPendingForReconcile(false, -time.Second))
	})

	t.Run("skips pending for drift only reconciliation", func(t *testing.T) {
		assert.False(t, shouldSetPendingForReconcile(false, 2*time.Minute))
	})
}

func assertDriftRequeue(t *testing.T, result reconcile.Result, interval time.Duration) {
	t.Helper()

	assert.GreaterOrEqual(t, result.RequeueAfter, interval)
	assert.LessOrEqual(t, result.RequeueAfter, interval+interval/maxDriftRequeueJitterRatio)
}
