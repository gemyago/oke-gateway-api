package app

import (
	"errors"
	"net/http"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

func TestNetworkLoadBalancerRetry(t *testing.T) {
	t.Run("detects updating lifecycle state", func(t *testing.T) {
		nlbID := "nlb-id"
		err := networkLoadBalancerBusyErrorFromState(&networkloadbalancer.NetworkLoadBalancer{
			Id:             &nlbID,
			LifecycleState: networkloadbalancer.LifecycleStateUpdating,
		})

		var busyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &busyErr)
		assert.Contains(t, err.Error(), nlbID)
	})

	t.Run("ignores non-updating lifecycle state", func(t *testing.T) {
		err := networkLoadBalancerBusyErrorFromState(&networkloadbalancer.NetworkLoadBalancer{
			LifecycleState: networkloadbalancer.LifecycleStateActive,
		})

		require.NoError(t, err)
	})

	t.Run("detects OCI invalid updating state transition conflict", func(t *testing.T) {
		nlbID := "nlb-id"
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("InvalidStateTransition"),
			ociapi.RandomServiceErrorWithMessage(
				"Invalid State Transition of NLB lifeCycle state from Updating to Updating",
			),
		)

		err := networkLoadBalancerBusyErrorFromOCI(&nlbID, cause)

		require.ErrorIs(t, err, cause)
		assert.Contains(t, err.Error(), nlbID)
	})

	t.Run("ignores unrelated OCI errors", func(t *testing.T) {
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusInternalServerError),
			ociapi.RandomServiceErrorWithMessage("temporary failure"),
		)

		err := networkLoadBalancerBusyErrorFromOCI(new(string), cause)

		require.Nil(t, err)
	})

	t.Run("ignores non-service errors", func(t *testing.T) {
		err := networkLoadBalancerBusyErrorFromOCI(new(string), errors.New("boom"))

		require.Nil(t, err)
	})
}
