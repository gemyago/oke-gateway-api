package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayModelImpl_programGateway(t *testing.T) {
	t.Run("MissingLoadBalancerAnnotation", func(t *testing.T) {
		// Arrange
		model := &gatewayModelImpl{}  // No deps for now
		gateway := newRandomGateway() // Create gateway without the annotation

		// Act
		err := model.programGateway(t.Context(), gateway)

		// Assert
		require.Error(t, err)

		var statusErr *resourceStatusError
		require.ErrorAs(t, err, &statusErr, "Error should be a resourceStatusError")

		assert.Equal(t, ProgrammedGatewayConditionType, statusErr.conditionType)
		assert.Equal(t, MissingAnnotationReason, statusErr.reason)
		expectedMsg := fmt.Sprintf("Gateway is missing load balancer ID annotation '%s'", LoadBalancerIDAnnotation)
		assert.Equal(t, expectedMsg, statusErr.message)
		assert.NoError(t, statusErr.cause)
	})
}
