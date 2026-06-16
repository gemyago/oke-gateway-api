package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type stubNLBGatewayModel struct {
	relevant         bool
	data             resolvedGatewayDetails
	nlb              *networkloadbalancer.NetworkLoadBalancer
	resolveErr       error
	getErr           error
	programErr       error
	deprovisionErr   error
	setProgrammedErr error
	programmedNow    bool
	deprovisioned    bool
	alreadyDone      bool
	programmedNLB    *networkloadbalancer.NetworkLoadBalancer
}

func (s *stubNLBGatewayModel) resolveReconcileRequest(
	_ context.Context,
	_ reconcile.Request,
	receiver *resolvedGatewayDetails,
) (bool, error) {
	*receiver = s.data
	return s.relevant, s.resolveErr
}

func (s *stubNLBGatewayModel) ensureNetworkLoadBalancer(
	context.Context,
	*resolvedGatewayDetails,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	panic("not implemented")
}

func (s *stubNLBGatewayModel) getNetworkLoadBalancer(
	context.Context,
	*resolvedGatewayDetails,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	return s.nlb, s.getErr
}

func (s *stubNLBGatewayModel) programGateway(context.Context, *resolvedGatewayDetails) error {
	s.programmedNow = true
	return s.programErr
}

func (s *stubNLBGatewayModel) deprovisionGateway(context.Context, *resolvedGatewayDetails) error {
	s.deprovisioned = true
	return s.deprovisionErr
}

func (s *stubNLBGatewayModel) isProgrammed(context.Context, *resolvedGatewayDetails) bool {
	return s.alreadyDone
}

func (s *stubNLBGatewayModel) setProgrammed(
	_ context.Context,
	_ *resolvedGatewayDetails,
	nlb *networkloadbalancer.NetworkLoadBalancer,
) error {
	s.programmedNow = true
	s.programmedNLB = nlb
	return s.setProgrammedErr
}

type stubResourcesModel struct {
	conditionSet bool
	setCalled    bool
	setErr       error
}

func (s *stubResourcesModel) setCondition(context.Context, setConditionParams) error {
	s.setCalled = true
	return s.setErr
}

func (s *stubResourcesModel) isConditionSet(isConditionSetParams) bool {
	return s.conditionSet
}

func TestNetworkLoadBalancerGatewayController(t *testing.T) {
	req := reconcile.Request{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "edge"}}
	baseData := resolvedGatewayDetails{
		gateway: gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
		},
	}

	t.Run("programs relevant gateway", func(t *testing.T) {
		nlb := &networkloadbalancer.NetworkLoadBalancer{}
		gatewayModel := &stubNLBGatewayModel{relevant: true, data: baseData, nlb: nlb}
		resourcesModel := &stubResourcesModel{}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: resourcesModel,
			GatewayModel:   gatewayModel,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Empty(t, result)
		assert.True(t, resourcesModel.setCalled)
		assert.True(t, gatewayModel.programmedNow)
		assert.Same(t, nlb, gatewayModel.programmedNLB)
	})

	t.Run("ignores irrelevant gateway", func(t *testing.T) {
		gatewayModel := &stubNLBGatewayModel{data: baseData}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{},
			GatewayModel:   gatewayModel,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, gatewayModel.programmedNow)
	})

	t.Run("deprovisions deleted gateway with finalizer", func(t *testing.T) {
		data := baseData
		data.gateway.DeletionTimestamp = &metav1.Time{}
		data.gateway.Finalizers = []string{NetworkLoadBalancerGatewayProgrammedFinalizer}
		gatewayModel := &stubNLBGatewayModel{relevant: true, data: data}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{},
			GatewayModel:   gatewayModel,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, gatewayModel.deprovisioned)
	})

	t.Run("sets condition for resource status errors", func(t *testing.T) {
		resourcesModel := &stubResourcesModel{}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: resourcesModel,
			GatewayModel: &stubNLBGatewayModel{
				data:       baseData,
				resolveErr: &resourceStatusError{conditionType: "Accepted", reason: "Invalid", message: "bad config"},
			},
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, resourcesModel.setCalled)
	})

	t.Run("wraps program errors", func(t *testing.T) {
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{conditionSet: true},
			GatewayModel: &stubNLBGatewayModel{
				relevant:   true,
				data:       baseData,
				programErr: errors.New("boom"),
			},
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to program Network Load Balancer Gateway")
	})

	t.Run("returns busy requeue for network load balancer busy errors", func(t *testing.T) {
		resourcesModel := &stubResourcesModel{conditionSet: true}
		gatewayModel := &stubNLBGatewayModel{
			relevant:   true,
			data:       baseData,
			programErr: &networkLoadBalancerBusyError{id: "nlb-id"},
		}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: resourcesModel,
			GatewayModel:   gatewayModel,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, networkLoadBalancerBusyRequeueAfter, result.RequeueAfter)
		assert.False(t, resourcesModel.setCalled)
		assert.True(t, gatewayModel.programmedNow)
	})

	t.Run("returns drift requeue for program resource status errors", func(t *testing.T) {
		driftInterval := 29 * time.Minute
		resourcesModel := &stubResourcesModel{conditionSet: true}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: resourcesModel,
			GatewayModel: &stubNLBGatewayModel{
				relevant: true,
				data:     baseData,
				programErr: &resourceStatusError{
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					reason:        string(gatewayv1.GatewayReasonPending),
					message:       "waiting for network load balancer",
				},
			},
			DriftInterval: driftInterval,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assertDriftRequeue(t, result, driftInterval)
		assert.True(t, resourcesModel.setCalled)
	})

	t.Run("wraps programmed network load balancer lookup errors", func(t *testing.T) {
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{conditionSet: true},
			GatewayModel: &stubNLBGatewayModel{
				relevant: true,
				data:     baseData,
				getErr:   errors.New("boom"),
			},
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to get programmed Network Load Balancer")
	})

	t.Run("sets condition for programmed network load balancer lookup status errors", func(t *testing.T) {
		driftInterval := 31 * time.Minute
		resourcesModel := &stubResourcesModel{conditionSet: true}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: resourcesModel,
			GatewayModel: &stubNLBGatewayModel{
				relevant: true,
				data:     baseData,
				getErr: &resourceStatusError{
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					reason:        string(gatewayv1.GatewayReasonPending),
					message:       "waiting for network load balancer",
				},
			},
			DriftInterval: driftInterval,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assertDriftRequeue(t, result, driftInterval)
		assert.True(t, resourcesModel.setCalled)
	})

	t.Run("skips deleted gateway without finalizer", func(t *testing.T) {
		data := baseData
		data.gateway.DeletionTimestamp = &metav1.Time{}
		gatewayModel := &stubNLBGatewayModel{relevant: true, data: data}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{},
			GatewayModel:   gatewayModel,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, gatewayModel.deprovisioned)
	})

	t.Run("returns early when already programmed", func(t *testing.T) {
		gatewayModel := &stubNLBGatewayModel{relevant: true, data: baseData, alreadyDone: true}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{conditionSet: true},
			GatewayModel:   gatewayModel,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, gatewayModel.programmedNow)
	})

	t.Run("programs already programmed gateway when drift interval is enabled", func(t *testing.T) {
		nlb := &networkloadbalancer.NetworkLoadBalancer{}
		driftInterval := 11 * time.Minute
		gatewayModel := &stubNLBGatewayModel{
			relevant:    true,
			data:        baseData,
			alreadyDone: true,
			nlb:         nlb,
		}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{conditionSet: true},
			GatewayModel:   gatewayModel,
			DriftInterval:  driftInterval,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assertDriftRequeue(t, result, driftInterval)
		assert.True(t, gatewayModel.programmedNow)
		assert.Same(t, nlb, gatewayModel.programmedNLB)
	})

	t.Run("wraps accepted condition errors", func(t *testing.T) {
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{setErr: errors.New("boom")},
			GatewayModel:   &stubNLBGatewayModel{relevant: true, data: baseData},
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to set accepted condition")
	})

	t.Run("wraps programmed condition errors", func(t *testing.T) {
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{conditionSet: true},
			GatewayModel: &stubNLBGatewayModel{
				relevant:         true,
				data:             baseData,
				setProgrammedErr: errors.New("boom"),
			},
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.Error(t, err)
	})

	t.Run("wraps deprovision and resource status condition errors", func(t *testing.T) {
		data := baseData
		data.gateway.DeletionTimestamp = &metav1.Time{}
		data.gateway.Finalizers = []string{NetworkLoadBalancerGatewayProgrammedFinalizer}
		controller := NewNetworkLoadBalancerGatewayController(NetworkLoadBalancerGatewayControllerDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{setErr: errors.New("condition failed")},
			GatewayModel: &stubNLBGatewayModel{
				relevant:       true,
				data:           data,
				deprovisionErr: &resourceStatusError{conditionType: "Programmed", reason: "Invalid", message: "bad"},
			},
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to set condition")
	})
}
