package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type stubTCPRouteModel struct {
	resolved         []resolvedTCPRouteDetails
	resolveErr       error
	programErr       error
	deprovisionErr   error
	setProgrammedErr error
	setRejectedErr   error
	deprovisioned    bool
	programmed       bool
	rejected         bool
}

func (s *stubTCPRouteModel) resolveRequest(context.Context, reconcile.Request) ([]resolvedTCPRouteDetails, error) {
	return s.resolved, s.resolveErr
}

func (s *stubTCPRouteModel) programRoute(context.Context, resolvedTCPRouteDetails) error {
	return s.programErr
}

func (s *stubTCPRouteModel) deprovisionRoute(context.Context, resolvedTCPRouteDetails) error {
	s.deprovisioned = true
	return s.deprovisionErr
}

func (s *stubTCPRouteModel) setProgrammed(context.Context, resolvedTCPRouteDetails) error {
	s.programmed = true
	return s.setProgrammedErr
}

func (s *stubTCPRouteModel) setRejected(context.Context, resolvedTCPRouteDetails, tcpRouteStatusError) error {
	s.rejected = true
	return s.setRejectedErr
}

type stubUDPRouteModel struct {
	resolved         []resolvedUDPRouteDetails
	resolveErr       error
	programErr       error
	deprovisionErr   error
	setProgrammedErr error
	setRejectedErr   error
	deprovisioned    bool
	programmed       bool
	rejected         bool
}

func (s *stubUDPRouteModel) resolveRequest(context.Context, reconcile.Request) ([]resolvedUDPRouteDetails, error) {
	return s.resolved, s.resolveErr
}

func (s *stubUDPRouteModel) programRoute(context.Context, resolvedUDPRouteDetails) error {
	return s.programErr
}

func (s *stubUDPRouteModel) deprovisionRoute(context.Context, resolvedUDPRouteDetails) error {
	s.deprovisioned = true
	return s.deprovisionErr
}

func (s *stubUDPRouteModel) setProgrammed(context.Context, resolvedUDPRouteDetails) error {
	s.programmed = true
	return s.setProgrammedErr
}

func (s *stubUDPRouteModel) setRejected(context.Context, resolvedUDPRouteDetails, udpRouteStatusError) error {
	s.rejected = true
	return s.setRejectedErr
}

type stubTLSRouteModel struct {
	resolved         []resolvedTLSRouteDetails
	resolveErr       error
	programErr       error
	deprovisionErr   error
	setProgrammedErr error
	setRejectedErr   error
	deprovisioned    bool
	programmed       bool
	rejected         bool
}

func (s *stubTLSRouteModel) resolveRequest(context.Context, reconcile.Request) ([]resolvedTLSRouteDetails, error) {
	return s.resolved, s.resolveErr
}

func (s *stubTLSRouteModel) programRoute(context.Context, resolvedTLSRouteDetails) error {
	return s.programErr
}

func (s *stubTLSRouteModel) deprovisionRoute(context.Context, resolvedTLSRouteDetails) error {
	s.deprovisioned = true
	return s.deprovisionErr
}

func (s *stubTLSRouteModel) setProgrammed(context.Context, resolvedTLSRouteDetails) error {
	s.programmed = true
	return s.setProgrammedErr
}

func (s *stubTLSRouteModel) setRejected(context.Context, resolvedTLSRouteDetails, tlsRouteStatusError) error {
	s.rejected = true
	return s.setRejectedErr
}

func TestTCPRouteController(t *testing.T) {
	req := reconcile.Request{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}}

	t.Run("programs resolved route", func(t *testing.T) {
		model := &stubTCPRouteModel{resolved: []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}}}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Empty(t, result)
		assert.True(t, model.programmed)
	})

	t.Run("returns busy requeue for network load balancer busy errors", func(t *testing.T) {
		model := &stubTCPRouteModel{
			resolved:   []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}},
			programErr: &networkLoadBalancerBusyError{id: "nlb-id"},
		}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, networkLoadBalancerBusyRequeueAfter, result.RequeueAfter)
		assert.False(t, model.programmed)
		assert.False(t, model.rejected)
	})

	t.Run("returns drift requeue for resolved route when interval is configured", func(t *testing.T) {
		driftInterval := 17 * time.Minute
		model := &stubTCPRouteModel{resolved: []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}}}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
			DriftInterval: driftInterval,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assertDriftRequeue(t, result, driftInterval)
		assert.True(t, model.programmed)
	})

	t.Run("returns busy requeue for network load balancer busy errors", func(t *testing.T) {
		model := &stubTCPRouteModel{
			resolved:   []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}},
			programErr: &networkLoadBalancerBusyError{id: "nlb-id"},
		}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, networkLoadBalancerBusyRequeueAfter, result.RequeueAfter)
		assert.False(t, model.programmed)
		assert.False(t, model.rejected)
	})

	t.Run("sets rejected status for route status errors", func(t *testing.T) {
		model := &stubTCPRouteModel{
			resolved: []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}},
			programErr: newTCPRouteAcceptedStatusError(
				gatewayv1.RouteReasonNotAllowedByListeners,
				"rejected",
			),
		}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, model.rejected)
		assert.False(t, model.programmed)
	})

	t.Run("deprovisions deleted route with finalizer", func(t *testing.T) {
		model := &stubTCPRouteModel{resolved: []resolvedTCPRouteDetails{{
			tcpRoute: gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{},
				Finalizers:        []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			}},
		}}}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, model.deprovisioned)
	})

	t.Run("wraps resolve errors", func(t *testing.T) {
		model := &stubTCPRouteModel{resolveErr: errors.New("boom")}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to resolve TCPRoute parent")
	})

	t.Run("ignores routes with no resolved parents", func(t *testing.T) {
		model := &stubTCPRouteModel{}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, model.programmed)
	})

	t.Run("skips deleted route without finalizer", func(t *testing.T) {
		model := &stubTCPRouteModel{resolved: []resolvedTCPRouteDetails{{
			tcpRoute: gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{}}},
		}}}
		controller := NewTCPRouteController(TCPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TCPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, model.deprovisioned)
	})

	t.Run("wraps program and status errors", func(t *testing.T) {
		for name, model := range map[string]*stubTCPRouteModel{
			"program": {
				resolved:   []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}},
				programErr: errors.New("boom"),
			},
			"set programmed": {
				resolved:         []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}},
				setProgrammedErr: errors.New("boom"),
			},
			"set rejected": {
				resolved: []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{}}},
				programErr: newTCPRouteAcceptedStatusError(
					gatewayv1.RouteReasonNotAllowedByListeners,
					"rejected",
				),
				setRejectedErr: errors.New("boom"),
			},
			"deprovision": {
				resolved: []resolvedTCPRouteDetails{{tcpRoute: gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				}}}},
				deprovisionErr: errors.New("boom"),
			},
		} {
			t.Run(name, func(t *testing.T) {
				controller := NewTCPRouteController(TCPRouteControllerDeps{
					RootLogger:    diag.RootTestLogger(),
					TCPRouteModel: model,
				})

				_, err := controller.Reconcile(t.Context(), req)

				require.Error(t, err)
			})
		}
	})
}

func TestTLSRouteController(t *testing.T) {
	req := reconcile.Request{NamespacedName: apitypes.NamespacedName{Namespace: "media", Name: "rtmps"}}

	t.Run("sets BackendTLSPolicy availability on concrete model", func(t *testing.T) {
		model := &tlsRouteModelImpl{}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		controller.SetBackendTLSPolicyEnabled(false)
		require.True(t, model.backendTLSDisabled)

		controller.SetBackendTLSPolicyEnabled(true)
		require.False(t, model.backendTLSDisabled)
	})

	t.Run("programs resolved route", func(t *testing.T) {
		model := &stubTLSRouteModel{resolved: []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}}}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Empty(t, result)
		assert.True(t, model.programmed)
	})

	t.Run("returns drift requeue for resolved route when interval is configured", func(t *testing.T) {
		driftInterval := 23 * time.Minute
		model := &stubTLSRouteModel{resolved: []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}}}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
			DriftInterval: driftInterval,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assertDriftRequeue(t, result, driftInterval)
		assert.True(t, model.programmed)
	})

	t.Run("returns busy requeue for network load balancer busy errors", func(t *testing.T) {
		model := &stubTLSRouteModel{
			resolved:   []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}},
			programErr: &networkLoadBalancerBusyError{id: "nlb-id"},
		}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, networkLoadBalancerBusyRequeueAfter, result.RequeueAfter)
		assert.False(t, model.programmed)
		assert.False(t, model.rejected)
	})

	t.Run("sets rejected status for route status errors", func(t *testing.T) {
		model := &stubTLSRouteModel{
			resolved: []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}},
			programErr: newTLSRouteAcceptedStatusError(
				gatewayv1.RouteReasonNotAllowedByListeners,
				"rejected",
			),
		}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, model.rejected)
		assert.False(t, model.programmed)
	})

	t.Run("deprovisions deleted route with matching finalizer", func(t *testing.T) {
		model := &stubTLSRouteModel{resolved: []resolvedTLSRouteDetails{{
			gatewayDetails: resolvedGatewayDetails{
				gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			},
			tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{},
				Finalizers:        []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
			}},
		}}}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, model.deprovisioned)
	})

	t.Run("wraps resolve errors", func(t *testing.T) {
		model := &stubTLSRouteModel{resolveErr: errors.New("boom")}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to resolve TLSRoute parent")
	})

	t.Run("ignores routes with no resolved parents", func(t *testing.T) {
		model := &stubTLSRouteModel{}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, model.programmed)
	})

	t.Run("skips deleted route without matching finalizer", func(t *testing.T) {
		model := &stubTLSRouteModel{resolved: []resolvedTLSRouteDetails{{
			gatewayDetails: resolvedGatewayDetails{
				gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
					ControllerName: ControllerClassName,
				}},
			},
			tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{},
				Finalizers:        []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
			}},
		}}}
		controller := NewTLSRouteController(TLSRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			TLSRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, model.deprovisioned)
	})

	t.Run("wraps program and status errors", func(t *testing.T) {
		for name, model := range map[string]*stubTLSRouteModel{
			"program": {
				resolved:   []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}},
				programErr: errors.New("boom"),
			},
			"set programmed": {
				resolved:         []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}},
				setProgrammedErr: errors.New("boom"),
			},
			"set rejected": {
				resolved: []resolvedTLSRouteDetails{{tlsRoute: gatewayv1.TLSRoute{}}},
				programErr: newTLSRouteAcceptedStatusError(
					gatewayv1.RouteReasonNotAllowedByListeners,
					"rejected",
				),
				setRejectedErr: errors.New("boom"),
			},
			"deprovision": {
				resolved: []resolvedTLSRouteDetails{{
					gatewayDetails: resolvedGatewayDetails{
						gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
							ControllerName: ControllerClassName,
						}},
					},
					tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &metav1.Time{},
						Finalizers:        []string{LoadBalancerTLSRouteProgrammedFinalizer},
					}},
				}},
				deprovisionErr: errors.New("boom"),
			},
		} {
			t.Run(name, func(t *testing.T) {
				controller := NewTLSRouteController(TLSRouteControllerDeps{
					RootLogger:    diag.RootTestLogger(),
					TLSRouteModel: model,
				})

				_, err := controller.Reconcile(t.Context(), req)

				require.Error(t, err)
			})
		}
	})
}

func TestUDPRouteController(t *testing.T) {
	req := reconcile.Request{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}}

	t.Run("programs resolved route", func(t *testing.T) {
		model := &stubUDPRouteModel{resolved: []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}}}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Empty(t, result)
		assert.True(t, model.programmed)
	})

	t.Run("returns busy requeue for network load balancer busy errors", func(t *testing.T) {
		model := &stubUDPRouteModel{
			resolved:   []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}},
			programErr: &networkLoadBalancerBusyError{id: "nlb-id"},
		}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, networkLoadBalancerBusyRequeueAfter, result.RequeueAfter)
		assert.False(t, model.programmed)
		assert.False(t, model.rejected)
	})

	t.Run("returns drift requeue for resolved route when interval is configured", func(t *testing.T) {
		driftInterval := 19 * time.Minute
		model := &stubUDPRouteModel{resolved: []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}}}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
			DriftInterval: driftInterval,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assertDriftRequeue(t, result, driftInterval)
		assert.True(t, model.programmed)
	})

	t.Run("returns busy requeue for network load balancer busy errors", func(t *testing.T) {
		model := &stubUDPRouteModel{
			resolved:   []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}},
			programErr: &networkLoadBalancerBusyError{id: "nlb-id"},
		}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, networkLoadBalancerBusyRequeueAfter, result.RequeueAfter)
		assert.False(t, model.programmed)
		assert.False(t, model.rejected)
	})

	t.Run("sets rejected status for route status errors", func(t *testing.T) {
		model := &stubUDPRouteModel{
			resolved: []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}},
			programErr: newUDPRouteAcceptedStatusError(
				gatewayv1.RouteReasonNotAllowedByListeners,
				"rejected",
			),
		}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, model.rejected)
		assert.False(t, model.programmed)
	})

	t.Run("deprovisions deleted route with finalizer", func(t *testing.T) {
		model := &stubUDPRouteModel{resolved: []resolvedUDPRouteDetails{{
			udpRoute: gatewayv1alpha2.UDPRoute{ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{},
				Finalizers:        []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
			}},
		}}}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.True(t, model.deprovisioned)
	})

	t.Run("wraps resolve errors", func(t *testing.T) {
		model := &stubUDPRouteModel{resolveErr: errors.New("boom")}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.ErrorContains(t, err, "failed to resolve UDPRoute parent")
	})

	t.Run("ignores routes with no resolved parents", func(t *testing.T) {
		model := &stubUDPRouteModel{}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, model.programmed)
	})

	t.Run("skips deleted route without finalizer", func(t *testing.T) {
		model := &stubUDPRouteModel{resolved: []resolvedUDPRouteDetails{{
			udpRoute: gatewayv1alpha2.UDPRoute{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{}}},
		}}}
		controller := NewUDPRouteController(UDPRouteControllerDeps{
			RootLogger:    diag.RootTestLogger(),
			UDPRouteModel: model,
		})

		_, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.False(t, model.deprovisioned)
	})

	t.Run("wraps program and status errors", func(t *testing.T) {
		for name, model := range map[string]*stubUDPRouteModel{
			"program": {
				resolved:   []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}},
				programErr: errors.New("boom"),
			},
			"set programmed": {
				resolved:         []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}},
				setProgrammedErr: errors.New("boom"),
			},
			"set rejected": {
				resolved: []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{}}},
				programErr: newUDPRouteAcceptedStatusError(
					gatewayv1.RouteReasonNotAllowedByListeners,
					"rejected",
				),
				setRejectedErr: errors.New("boom"),
			},
			"deprovision": {
				resolved: []resolvedUDPRouteDetails{{udpRoute: gatewayv1alpha2.UDPRoute{ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				}}}},
				deprovisionErr: errors.New("boom"),
			},
		} {
			t.Run(name, func(t *testing.T) {
				controller := NewUDPRouteController(UDPRouteControllerDeps{
					RootLogger:    diag.RootTestLogger(),
					UDPRouteModel: model,
				})

				_, err := controller.Reconcile(t.Context(), req)

				require.Error(t, err)
			})
		}
	})
}
