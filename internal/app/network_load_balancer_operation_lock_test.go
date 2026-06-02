package app

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

func TestNetworkLoadBalancerOperationLocks(t *testing.T) {
	t.Run("serializes operations for the same load balancer", func(t *testing.T) {
		locks := newNetworkLoadBalancerOperationLocks()
		nlbID := new("ocid1.networkloadbalancer.oc1..test")
		started := make(chan struct{})
		release := make(chan struct{})
		var secondStarted atomic.Bool

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			err := locks.withLock(nlbID, func() error {
				close(started)
				<-release
				return nil
			})
			assert.NoError(t, err)
		}()
		<-started

		go func() {
			defer wg.Done()
			err := locks.withLock(nlbID, func() error {
				secondStarted.Store(true)
				return nil
			})
			assert.NoError(t, err)
		}()

		assert.False(t, secondStarted.Load())
		close(release)
		wg.Wait()
		assert.True(t, secondStarted.Load())
	})

	t.Run("does not serialize operations for empty ids", func(t *testing.T) {
		locks := newNetworkLoadBalancerOperationLocks()
		started := make(chan struct{})
		release := make(chan struct{})
		var secondStarted atomic.Bool

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			err := locks.withLock(nil, func() error {
				close(started)
				<-release
				return nil
			})
			assert.NoError(t, err)
		}()
		<-started

		go func() {
			defer wg.Done()
			err := locks.withLock(nil, func() error {
				secondStarted.Store(true)
				return nil
			})
			assert.NoError(t, err)
		}()

		assert.Eventually(t, secondStarted.Load, time.Second, 10*time.Millisecond)
		close(release)
		wg.Wait()
	})

	t.Run("recovers if an unexpected lock value is stored", func(t *testing.T) {
		locks := newNetworkLoadBalancerOperationLocks()
		nlbID := new("ocid1.networkloadbalancer.oc1..bad-lock")
		locks.mutexes.Store(*nlbID, "unexpected")
		called := false

		err := locks.withLock(nlbID, func() error {
			called = true
			return nil
		})

		require.NoError(t, err)
		assert.True(t, called)
		_, exists := locks.mutexes.Load(*nlbID)
		assert.False(t, exists)
	})
}

func TestNetworkLoadBalancerOperationLockID(t *testing.T) {
	t.Run("uses gateway annotation first", func(t *testing.T) {
		got := networkLoadBalancerOperationLockID(resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						NetworkLoadBalancerGatewayIDAnnotation: "annotation-nlb",
					},
				},
			},
			config: types.GatewayConfig{
				Spec: types.GatewayConfigSpec{LoadBalancerID: "config-nlb"},
			},
		})

		require.NotNil(t, got)
		assert.Equal(t, "annotation-nlb", *got)
	})

	t.Run("falls back to existing nlb config", func(t *testing.T) {
		got := networkLoadBalancerOperationLockID(resolvedGatewayDetails{
			config: types.GatewayConfig{
				Spec: types.GatewayConfigSpec{LoadBalancerID: "config-nlb"},
			},
		})

		require.NotNil(t, got)
		assert.Equal(t, "config-nlb", *got)
	})
}
