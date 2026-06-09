package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

type stubNetworkLoadBalancerClient struct {
	createBackendSetRequests []networkloadbalancer.CreateBackendSetRequest
	updateBackendSetRequests []networkloadbalancer.UpdateBackendSetRequest
	createListenerRequests   []networkloadbalancer.CreateListenerRequest
	updateListenerRequests   []networkloadbalancer.UpdateListenerRequest
	deleteListenerRequests   []networkloadbalancer.DeleteListenerRequest
	deleteBackendSetRequests []networkloadbalancer.DeleteBackendSetRequest
	getRequests              []networkloadbalancer.GetNetworkLoadBalancerRequest
	getResponse              networkloadbalancer.GetNetworkLoadBalancerResponse
	createBackendSetResponse networkloadbalancer.CreateBackendSetResponse
	updateBackendSetResponse networkloadbalancer.UpdateBackendSetResponse
	createListenerResponse   networkloadbalancer.CreateListenerResponse
	updateListenerResponse   networkloadbalancer.UpdateListenerResponse
	deleteListenerResponse   networkloadbalancer.DeleteListenerResponse
	deleteBackendSetResponse networkloadbalancer.DeleteBackendSetResponse
	createBackendSetErr      error
	updateBackendSetErr      error
	createListenerErr        error
	updateListenerErr        error
	deleteListenerErr        error
	deleteBackendSetErr      error
	getErr                   error

	omitCreateBackendSetWorkRequest bool
	omitUpdateBackendSetWorkRequest bool
	omitCreateListenerWorkRequest   bool
	omitUpdateListenerWorkRequest   bool
	omitDeleteListenerWorkRequest   bool
	omitDeleteBackendSetWorkRequest bool
}

func TestNetworkLoadBalancerGatewayModelResolveAndProgramStatus(t *testing.T) {
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge", Generation: 3},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "oke-nlb",
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  "nlb-config",
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(
			gateway,
			&gatewayv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
				Spec: gatewayv1.GatewayClassSpec{
					ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
				},
			},
			&types.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "nlb-config"},
				Spec:       types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..existing"},
			},
		).
		WithStatusSubresource(&gatewayv1.Gateway{}).
		Build()
	model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		OciClient:  &stubNetworkLoadBalancerClient{},
		ResourcesModel: newResourcesModel(
			resourcesModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient},
		),
		WorkRequestsWatcher: &stubWorkRequestsWatcher{},
	})

	var details resolvedGatewayDetails
	relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
		NamespacedName: k8stypes.NamespacedName{Namespace: "iot", Name: "edge"},
	}, &details)
	require.NoError(t, err)
	assert.True(t, relevant)
	assert.Equal(t, "nlb-config", details.config.Name)

	assert.False(t, model.isProgrammed(t.Context(), &details))
	err = model.setProgrammed(t.Context(), &details, &networkloadbalancer.NetworkLoadBalancer{
		Id: new("ocid1.networkloadbalancer.oc1..managed"),
		IpAddresses: []networkloadbalancer.IpAddress{
			{IpAddress: new("10.0.0.2")},
			{IpAddress: new("192.0.2.10")},
			{IpAddress: new("10.0.0.1")},
			{IpAddress: new("10.0.0.2")},
			{},
			{IpAddress: new("")},
		},
	})
	require.NoError(t, err)

	var updated gatewayv1.Gateway
	require.NoError(t, k8sClient.Get(t.Context(), k8stypes.NamespacedName{Namespace: "iot", Name: "edge"}, &updated))
	assert.Contains(t, updated.Finalizers, NetworkLoadBalancerGatewayProgrammedFinalizer)
	assert.Equal(t, NetworkLoadBalancerGatewayProgrammingRevisionValue,
		updated.Annotations[NetworkLoadBalancerGatewayProgrammingRevisionAnnotation])
	assert.Equal(t, "ocid1.networkloadbalancer.oc1..managed",
		updated.Annotations[NetworkLoadBalancerGatewayIDAnnotation])
	addressType := gatewayv1.IPAddressType
	assert.Equal(t, []gatewayv1.GatewayStatusAddress{
		{Type: &addressType, Value: "192.0.2.10"},
		{Type: &addressType, Value: "10.0.0.1"},
		{Type: &addressType, Value: "10.0.0.2"},
	}, updated.Status.Addresses)
}

func TestNetworkLoadBalancerGatewayModelIsProgrammedWithExistingNLB(t *testing.T) {
	model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
		RootLogger:     diag.RootTestLogger(),
		ResourcesModel: newResourcesModel(resourcesModelDeps{RootLogger: diag.RootTestLogger()}),
	})
	gateway := gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "iot",
			Name:       "edge",
			Generation: 3,
			Annotations: map[string]string{
				NetworkLoadBalancerGatewayProgrammingRevisionAnnotation: NetworkLoadBalancerGatewayProgrammingRevisionValue,
				NetworkLoadBalancerGatewayIDAnnotation:                  "ocid1.networkloadbalancer.oc1..old",
			},
		},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{{
				Type:               string(gatewayv1.GatewayConditionProgrammed),
				Status:             metav1.ConditionTrue,
				ObservedGeneration: 3,
			}},
		},
	}

	assert.True(t, model.isProgrammed(t.Context(), &resolvedGatewayDetails{
		gateway: gateway,
		config: types.GatewayConfig{
			Spec: types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..old"},
		},
	}))
	assert.False(t, model.isProgrammed(t.Context(), &resolvedGatewayDetails{
		gateway: gateway,
		config: types.GatewayConfig{
			Spec: types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..new"},
		},
	}))
}

func TestNetworkLoadBalancerGatewayModelResolveBranches(t *testing.T) {
	gateway := func(mutators ...func(*gatewayv1.Gateway)) *gatewayv1.Gateway {
		g := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		for _, mutate := range mutators {
			mutate(g)
		}
		return g
	}
	gatewayClass := func(controller gatewayv1.GatewayController) *gatewayv1.GatewayClass {
		return &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: controller,
			},
		}
	}
	config := &types.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "nlb-config"},
		Spec:       types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..existing"},
	}
	now := metav1.Now()
	deletingGateway := func(mutators ...func(*gatewayv1.Gateway)) *gatewayv1.Gateway {
		return gateway(append([]func(*gatewayv1.Gateway){
			func(g *gatewayv1.Gateway) {
				g.Finalizers = []string{NetworkLoadBalancerGatewayProgrammedFinalizer}
				g.DeletionTimestamp = &now
				g.Annotations = map[string]string{NetworkLoadBalancerGatewayIDAnnotation: "nlb-id"}
			},
		}, mutators...)...)
	}

	for name, tc := range map[string]struct {
		objects  []client.Object
		relevant bool
		err      string
	}{
		"missing gateway": {
			objects:  []client.Object{},
			relevant: false,
		},
		"missing gateway class": {
			objects:  []client.Object{gateway()},
			relevant: false,
		},
		"wrong controller": {
			objects: []client.Object{
				gateway(),
				gatewayClass(gatewayv1.GatewayController("example.com/other")),
			},
			relevant: false,
		},
		"missing infrastructure": {
			objects: []client.Object{
				gateway(func(g *gatewayv1.Gateway) { g.Spec.Infrastructure = nil }),
				gatewayClass(gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)),
			},
			err: "spec.infrastructure is missing parametersRef",
		},
		"missing config": {
			objects: []client.Object{
				gateway(),
				gatewayClass(gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)),
			},
			err: "non-existent GatewayConfig",
		},
		"deleting with missing infrastructure and nlb annotation": {
			objects: []client.Object{
				deletingGateway(func(g *gatewayv1.Gateway) { g.Spec.Infrastructure = nil }),
				gatewayClass(gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)),
			},
			relevant: true,
		},
		"deleting with missing config and nlb annotation": {
			objects: []client.Object{
				deletingGateway(),
				gatewayClass(gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)),
			},
			relevant: true,
		},
		"resolved": {
			objects: []client.Object{
				gateway(),
				gatewayClass(gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)),
				config,
			},
			relevant: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(tc.objects...).
				Build()
			model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  k8sClient,
			})

			var details resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
				NamespacedName: k8stypes.NamespacedName{Namespace: "iot", Name: "edge"},
			}, &details)

			assert.Equal(t, tc.relevant, relevant)
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.err)
			}
		})
	}

	t.Run("wraps gateway client get errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		k8sClient.EXPECT().
			Get(t.Context(), k8stypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.Anything).
			Return(errors.New("get failed"))
		model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
		})

		var details resolvedGatewayDetails
		relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
			NamespacedName: k8stypes.NamespacedName{Namespace: "iot", Name: "edge"},
		}, &details)

		assert.False(t, relevant)
		require.ErrorContains(t, err, "failed to get Gateway")
	})

	t.Run("wraps gateway class client get errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		k8sClient.EXPECT().
			Get(t.Context(), k8stypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.Anything).
			RunAndReturn(func(_ context.Context, _ k8stypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				g, ok := obj.(*gatewayv1.Gateway)
				require.True(t, ok)
				*g = *gateway()
				return nil
			})
		k8sClient.EXPECT().
			Get(t.Context(), k8stypes.NamespacedName{Name: "oke-nlb"}, mock.Anything).
			Return(errors.New("get failed"))
		model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
		})

		var details resolvedGatewayDetails
		relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
			NamespacedName: k8stypes.NamespacedName{Namespace: "iot", Name: "edge"},
		}, &details)

		assert.False(t, relevant)
		require.ErrorContains(t, err, "failed to get GatewayClass")
	})
}

func (s *stubNetworkLoadBalancerClient) GetNetworkLoadBalancer(
	_ context.Context,
	request networkloadbalancer.GetNetworkLoadBalancerRequest,
) (networkloadbalancer.GetNetworkLoadBalancerResponse, error) {
	s.getRequests = append(s.getRequests, request)
	return s.getResponse, s.getErr
}

func (s *stubNetworkLoadBalancerClient) CreateListener(
	_ context.Context,
	request networkloadbalancer.CreateListenerRequest,
) (networkloadbalancer.CreateListenerResponse, error) {
	s.createListenerRequests = append(s.createListenerRequests, request)
	if s.createListenerResponse.OpcWorkRequestId == nil &&
		s.createListenerErr == nil &&
		!s.omitCreateListenerWorkRequest {
		s.createListenerResponse.OpcWorkRequestId = new("create-listener-work-request")
	}
	return s.createListenerResponse, s.createListenerErr
}

func (s *stubNetworkLoadBalancerClient) UpdateListener(
	_ context.Context,
	request networkloadbalancer.UpdateListenerRequest,
) (networkloadbalancer.UpdateListenerResponse, error) {
	s.updateListenerRequests = append(s.updateListenerRequests, request)
	if s.updateListenerResponse.OpcWorkRequestId == nil &&
		s.updateListenerErr == nil &&
		!s.omitUpdateListenerWorkRequest {
		s.updateListenerResponse.OpcWorkRequestId = new("update-listener-work-request")
	}
	return s.updateListenerResponse, s.updateListenerErr
}

func (s *stubNetworkLoadBalancerClient) DeleteListener(
	_ context.Context,
	request networkloadbalancer.DeleteListenerRequest,
) (networkloadbalancer.DeleteListenerResponse, error) {
	s.deleteListenerRequests = append(s.deleteListenerRequests, request)
	if s.deleteListenerResponse.OpcWorkRequestId == nil &&
		s.deleteListenerErr == nil &&
		!s.omitDeleteListenerWorkRequest {
		s.deleteListenerResponse.OpcWorkRequestId = new("delete-listener-work-request")
	}
	return s.deleteListenerResponse, s.deleteListenerErr
}

func (s *stubNetworkLoadBalancerClient) CreateBackendSet(
	_ context.Context,
	request networkloadbalancer.CreateBackendSetRequest,
) (networkloadbalancer.CreateBackendSetResponse, error) {
	s.createBackendSetRequests = append(s.createBackendSetRequests, request)
	if s.createBackendSetResponse.OpcWorkRequestId == nil &&
		s.createBackendSetErr == nil &&
		!s.omitCreateBackendSetWorkRequest {
		s.createBackendSetResponse.OpcWorkRequestId = new("create-backend-set-work-request")
	}
	return s.createBackendSetResponse, s.createBackendSetErr
}

func (s *stubNetworkLoadBalancerClient) UpdateBackendSet(
	_ context.Context,
	request networkloadbalancer.UpdateBackendSetRequest,
) (networkloadbalancer.UpdateBackendSetResponse, error) {
	s.updateBackendSetRequests = append(s.updateBackendSetRequests, request)
	if s.updateBackendSetResponse.OpcWorkRequestId == nil &&
		s.updateBackendSetErr == nil &&
		!s.omitUpdateBackendSetWorkRequest {
		s.updateBackendSetResponse.OpcWorkRequestId = new("update-backend-set-work-request")
	}
	return s.updateBackendSetResponse, s.updateBackendSetErr
}

func (s *stubNetworkLoadBalancerClient) DeleteBackendSet(
	_ context.Context,
	request networkloadbalancer.DeleteBackendSetRequest,
) (networkloadbalancer.DeleteBackendSetResponse, error) {
	s.deleteBackendSetRequests = append(s.deleteBackendSetRequests, request)
	if s.deleteBackendSetResponse.OpcWorkRequestId == nil &&
		s.deleteBackendSetErr == nil &&
		!s.omitDeleteBackendSetWorkRequest {
		s.deleteBackendSetResponse.OpcWorkRequestId = new("delete-backend-set-work-request")
	}
	return s.deleteBackendSetResponse, s.deleteBackendSetErr
}

func (s *stubNetworkLoadBalancerClient) CreateBackend(
	context.Context,
	networkloadbalancer.CreateBackendRequest,
) (networkloadbalancer.CreateBackendResponse, error) {
	panic("unexpected CreateBackend call")
}

func (s *stubNetworkLoadBalancerClient) UpdateBackend(
	context.Context,
	networkloadbalancer.UpdateBackendRequest,
) (networkloadbalancer.UpdateBackendResponse, error) {
	panic("unexpected UpdateBackend call")
}

func (s *stubNetworkLoadBalancerClient) DeleteBackend(
	context.Context,
	networkloadbalancer.DeleteBackendRequest,
) (networkloadbalancer.DeleteBackendResponse, error) {
	panic("unexpected DeleteBackend call")
}

type stubWorkRequestsWatcher struct {
	waited []string
	err    error
}

func (s *stubWorkRequestsWatcher) WaitFor(_ context.Context, workRequestID string) error {
	s.waited = append(s.waited, workRequestID)
	return s.err
}

func nlbHealthCheckerFromDetails(
	details networkloadbalancer.HealthCheckerDetails,
) *networkloadbalancer.HealthChecker {
	return &networkloadbalancer.HealthChecker{
		Protocol:         details.Protocol,
		Port:             details.Port,
		Retries:          details.Retries,
		TimeoutInMillis:  details.TimeoutInMillis,
		IntervalInMillis: details.IntervalInMillis,
	}
}

func matchingNLBBackendSet(listener gatewayv1.Listener) networkloadbalancer.BackendSet {
	healthChecker := networkLoadBalancerHealthCheckerDetails(listener.Protocol, new(int(listener.Port)))
	return networkloadbalancer.BackendSet{
		Name:             new(networkLoadBalancerBackendSetName(listener)),
		Policy:           networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple,
		HealthChecker:    nlbHealthCheckerFromDetails(healthChecker),
		IsPreserveSource: new(false),
	}
}

func TestNetworkLoadBalancerGatewayModel(t *testing.T) {
	newModel := func(
		client *stubNetworkLoadBalancerClient,
		watcher *stubWorkRequestsWatcher,
	) networkLoadBalancerGatewayModel {
		return newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger:          diag.RootTestLogger().WithGroup("test").With(slog.String("test", t.Name())),
			OciClient:           client,
			WorkRequestsWatcher: watcher,
		})
	}

	t.Run("matches network load balancer listener and backend set desired state", func(t *testing.T) {
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935}
		healthChecker := networkLoadBalancerHealthCheckerDetails(listener.Protocol, new(int(listener.Port)))
		assert.False(t, networkLoadBalancerHealthCheckerMatches(nil, healthChecker))
		assert.True(t, networkLoadBalancerHealthCheckerMatches(
			nlbHealthCheckerFromDetails(healthChecker),
			healthChecker,
		))

		assert.True(t, networkLoadBalancerBackendSetMatches(
			matchingNLBBackendSet(listener),
			networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple,
			healthChecker,
		))
		assert.False(t, networkLoadBalancerBackendSetMatches(
			networkloadbalancer.BackendSet{
				Policy:           networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple,
				HealthChecker:    nlbHealthCheckerFromDetails(healthChecker),
				IsPreserveSource: new(true),
			},
			networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple,
			healthChecker,
		))

		assert.True(t, networkLoadBalancerListenerMatches(
			networkloadbalancer.Listener{
				Protocol:              networkloadbalancer.ListenerProtocolsTcp,
				Port:                  new(1935),
				DefaultBackendSetName: new("bs_rtmp"),
			},
			networkloadbalancer.ListenerProtocolsTcp,
			1935,
			"bs_rtmp",
		))
		assert.False(t, networkLoadBalancerListenerMatches(
			networkloadbalancer.Listener{
				Protocol:              networkloadbalancer.ListenerProtocolsUdp,
				Port:                  new(1935),
				DefaultBackendSetName: new("bs_rtmp"),
			},
			networkloadbalancer.ListenerProtocolsTcp,
			1935,
			"bs_rtmp",
		))
	})

	newDetails := func() *resolvedGatewayDetails {
		return &resolvedGatewayDetails{
			gateway: *newRandomGateway(func(g *gatewayv1.Gateway) {
				g.Namespace = "iot"
				g.Name = "edge"
				g.UID = k8stypes.UID("gateway-uid")
			}),
			config: types.GatewayConfig{
				Spec: types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..existing"},
			},
		}
	}

	t.Run("gets existing network load balancer by id", func(t *testing.T) {
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
				},
			},
			createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: new("create-backend-set-work"),
			},
			createListenerResponse: networkloadbalancer.CreateListenerResponse{
				OpcWorkRequestId: new("create-listener-work"),
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})

		got, err := model.ensureNetworkLoadBalancer(t.Context(), newDetails())

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "ocid1.networkloadbalancer.oc1..existing", lo.FromPtr(got.Id))
		require.Len(t, client.getRequests, 1)
		assert.Equal(t, "ocid1.networkloadbalancer.oc1..existing",
			lo.FromPtr(client.getRequests[0].NetworkLoadBalancerId))

		got, err = model.getNetworkLoadBalancer(t.Context(), newDetails())
		require.NoError(t, err)
		assert.Equal(t, "ocid1.networkloadbalancer.oc1..existing", lo.FromPtr(got.Id))
		require.Len(t, client.getRequests, 2)
	})

	t.Run("wraps get network load balancer errors", func(t *testing.T) {
		model := newModel(&stubNetworkLoadBalancerClient{getErr: errors.New("get failed")}, &stubWorkRequestsWatcher{})

		got, err := model.ensureNetworkLoadBalancer(t.Context(), newDetails())

		require.Nil(t, got)
		require.ErrorContains(t, err, "failed to get OCI Network Load Balancer")
	})

	t.Run("returns programmed false status error when network load balancer is not found", func(t *testing.T) {
		details := newDetails()
		model := newModel(&stubNetworkLoadBalancerClient{
			getErr: ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
		}, &stubWorkRequestsWatcher{})

		got, err := model.ensureNetworkLoadBalancer(t.Context(), details)

		require.Nil(t, got)
		var statusErr *resourceStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, string(gatewayv1.GatewayConditionProgrammed), statusErr.conditionType)
		assert.Equal(t, string(gatewayv1.GatewayReasonPending), statusErr.reason)
		assert.Equal(t,
			"referenced OCI Network Load Balancer ocid1.networkloadbalancer.oc1..existing not found",
			statusErr.message,
		)
	})

	t.Run("returns invalid parameters for missing load balancer id", func(t *testing.T) {
		model := newModel(&stubNetworkLoadBalancerClient{}, &stubWorkRequestsWatcher{})
		details := newDetails()
		details.config.Spec.LoadBalancerID = ""

		got, err := model.ensureNetworkLoadBalancer(t.Context(), details)

		require.Nil(t, got)
		var statusErr *resourceStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, "spec.loadBalancerId is required for OCI Network Load Balancer gateways", statusErr.message)
	})

	t.Run("covers unsupported listener protocols", func(t *testing.T) {
		_, supported := networkLoadBalancerListenerProtocol(gatewayv1.HTTPProtocolType)
		assert.False(t, supported)
		_, supported = networkLoadBalancerListenerProtocol(gatewayv1.ProtocolType("SCTP"))
		assert.False(t, supported)
	})

	t.Run("programs tcp and udp gateway listeners", func(t *testing.T) {
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
				},
			},
			createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: new("create-backend-set-work"),
			},
			createListenerResponse: networkloadbalancer.CreateListenerResponse{
				OpcWorkRequestId: new("create-listener-work"),
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})
		details := newDetails()
		details.gateway.Spec.Listeners = []gatewayv1.Listener{
			{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				Port:     1935,
			},
			{
				Name:     "coap-dtls",
				Protocol: gatewayv1.UDPProtocolType,
				Port:     5684,
			},
		}

		err := model.programGateway(t.Context(), details)

		require.NoError(t, err)
		assert.Equal(t,
			"ocid1.networkloadbalancer.oc1..existing",
			details.gateway.Annotations[NetworkLoadBalancerGatewayIDAnnotation],
		)
		require.Len(t, client.createBackendSetRequests, 2)
		assert.Equal(t, "bs_rtmp", lo.FromPtr(client.createBackendSetRequests[0].Name))
		assert.False(t, lo.FromPtr(client.createBackendSetRequests[0].IsPreserveSource))
		assert.Equal(t, networkloadbalancer.HealthCheckProtocolsTcp,
			client.createBackendSetRequests[0].HealthChecker.Protocol)
		assert.Equal(t, "bs_coap_dtls", lo.FromPtr(client.createBackendSetRequests[1].Name))
		assert.False(t, lo.FromPtr(client.createBackendSetRequests[1].IsPreserveSource))
		assert.Equal(t, networkloadbalancer.HealthCheckProtocolsTcp,
			client.createBackendSetRequests[1].HealthChecker.Protocol)
		assert.Empty(t, client.createBackendSetRequests[1].HealthChecker.RequestData)
		assert.Empty(t, client.createBackendSetRequests[1].HealthChecker.ResponseData)

		require.Len(t, client.createListenerRequests, 2)
		assert.Equal(t, "rtmp", lo.FromPtr(client.createListenerRequests[0].Name))
		assert.Equal(t, networkloadbalancer.ListenerProtocolsTcp, client.createListenerRequests[0].Protocol)
		assert.Equal(t, 1935, lo.FromPtr(client.createListenerRequests[0].Port))
		assert.Equal(t, "bs_rtmp", lo.FromPtr(client.createListenerRequests[0].DefaultBackendSetName))
		assert.Equal(t, "coap-dtls", lo.FromPtr(client.createListenerRequests[1].Name))
		assert.Equal(t, networkloadbalancer.ListenerProtocolsUdp, client.createListenerRequests[1].Protocol)
		assert.Equal(t, 5684, lo.FromPtr(client.createListenerRequests[1].Port))
		assert.Equal(t, "bs_coap_dtls", lo.FromPtr(client.createListenerRequests[1].DefaultBackendSetName))
	})

	t.Run("programs existing network load balancer by id without marking it managed", func(t *testing.T) {
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
					Listeners:   map[string]networkloadbalancer.Listener{},
					BackendSets: map[string]networkloadbalancer.BackendSet{},
				},
			},
			createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: new("create-backend-set-work"),
			},
			createListenerResponse: networkloadbalancer.CreateListenerResponse{
				OpcWorkRequestId: new("create-listener-work"),
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})
		details := newDetails()
		details.gateway.Spec.Listeners = []gatewayv1.Listener{{
			Name:     "rtmp",
			Protocol: gatewayv1.TCPProtocolType,
			Port:     1935,
		}}

		err := model.programGateway(t.Context(), details)

		require.NoError(t, err)
		assert.Equal(t,
			"ocid1.networkloadbalancer.oc1..existing",
			details.gateway.Annotations[NetworkLoadBalancerGatewayIDAnnotation],
		)
		require.Len(t, client.createBackendSetRequests, 1)
		assert.False(t, lo.FromPtr(client.createBackendSetRequests[0].IsPreserveSource))
		require.Len(t, client.createListenerRequests, 1)
	})

	t.Run("updates existing listener and backend set", func(t *testing.T) {
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
					Listeners: map[string]networkloadbalancer.Listener{
						"rtmp": {Name: new("rtmp")},
					},
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_rtmp": {Name: new("bs_rtmp")},
					},
				},
			},
			updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: new("update-backend-set-work"),
			},
			updateListenerResponse: networkloadbalancer.UpdateListenerResponse{
				OpcWorkRequestId: new("update-listener-work"),
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})
		details := newDetails()
		details.gateway.Spec.Listeners = []gatewayv1.Listener{
			{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
		}

		err := model.programGateway(t.Context(), details)

		require.NoError(t, err)
		require.Len(t, client.updateBackendSetRequests, 1)
		require.Len(t, client.updateListenerRequests, 1)
		assert.Equal(t, "rtmp", lo.FromPtr(client.updateListenerRequests[0].ListenerName))
	})

	t.Run("skips existing listener and backend set when they already match", func(t *testing.T) {
		healthChecker := networkLoadBalancerHealthCheckerDetails(gatewayv1.TCPProtocolType, new(1935))
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
					Listeners: map[string]networkloadbalancer.Listener{
						"rtmp": {
							Name:                  new("rtmp"),
							DefaultBackendSetName: new("bs_rtmp"),
							Port:                  new(1935),
							Protocol:              networkloadbalancer.ListenerProtocolsTcp,
						},
					},
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_rtmp": {
							Name:             new("bs_rtmp"),
							Policy:           networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple,
							HealthChecker:    nlbHealthCheckerFromDetails(healthChecker),
							IsPreserveSource: new(false),
						},
					},
				},
			},
			updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: new("update-backend-set-work"),
			},
			updateListenerResponse: networkloadbalancer.UpdateListenerResponse{
				OpcWorkRequestId: new("update-listener-work"),
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})
		details := newDetails()
		details.gateway.Spec.Listeners = []gatewayv1.Listener{
			{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
		}

		err := model.programGateway(t.Context(), details)

		require.NoError(t, err)
		assert.Empty(t, client.updateBackendSetRequests)
		assert.Empty(t, client.updateListenerRequests)
		assert.Empty(t, client.createBackendSetRequests)
		assert.Empty(t, client.createListenerRequests)
	})

	t.Run("updates drifted listener and backend set fields", func(t *testing.T) {
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
					Listeners: map[string]networkloadbalancer.Listener{
						"rtmp": {
							Name:                  new("rtmp"),
							DefaultBackendSetName: new("wrong"),
							Port:                  new(80),
							Protocol:              networkloadbalancer.ListenerProtocolsUdp,
						},
					},
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_rtmp": {
							Name:   new("bs_rtmp"),
							Policy: networkloadbalancer.NetworkLoadBalancingPolicyTwoTuple,
							HealthChecker: &networkloadbalancer.HealthChecker{
								Protocol: networkloadbalancer.HealthCheckProtocolsUdp,
							},
							IsPreserveSource: new(true),
						},
					},
				},
			},
			updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: new("update-backend-set-work"),
			},
			updateListenerResponse: networkloadbalancer.UpdateListenerResponse{
				OpcWorkRequestId: new("update-listener-work"),
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})
		details := newDetails()
		details.gateway.Spec.Listeners = []gatewayv1.Listener{
			{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
		}

		err := model.programGateway(t.Context(), details)

		require.NoError(t, err)
		require.Len(t, client.updateBackendSetRequests, 1)
		assert.Equal(t, string(networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple),
			lo.FromPtr(client.updateBackendSetRequests[0].Policy))
		assert.False(t, lo.FromPtr(client.updateBackendSetRequests[0].IsPreserveSource))
		assert.Equal(t, networkloadbalancer.HealthCheckProtocolsTcp,
			client.updateBackendSetRequests[0].HealthChecker.Protocol)
		assert.Equal(t, 1935, lo.FromPtr(client.updateBackendSetRequests[0].HealthChecker.Port))

		require.Len(t, client.updateListenerRequests, 1)
		assert.Equal(t, networkloadbalancer.ListenerProtocolsTcp,
			client.updateListenerRequests[0].UpdateListenerDetails.Protocol)
		assert.Equal(t, 1935, lo.FromPtr(client.updateListenerRequests[0].UpdateListenerDetails.Port))
		assert.Equal(t, "bs_rtmp",
			lo.FromPtr(client.updateListenerRequests[0].UpdateListenerDetails.DefaultBackendSetName))
	})

	t.Run("rejects unsupported gateway listener protocol and empty load balancer id", func(t *testing.T) {
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: nil,
				},
			},
		}
		model := newModel(client, &stubWorkRequestsWatcher{})
		details := newDetails()

		err := model.programGateway(t.Context(), details)
		require.ErrorContains(t, err, "OCI Network Load Balancer id is empty")

		client.getResponse.NetworkLoadBalancer.Id = new("nlb-id")
		details.gateway.Spec.Listeners = []gatewayv1.Listener{
			{Name: "http", Protocol: gatewayv1.HTTPProtocolType, Port: 80},
		}
		err = model.programGateway(t.Context(), details)
		require.ErrorContains(t, err, "unsupported protocol")
	})

	t.Run("removes stale listeners and backend sets", func(t *testing.T) {
		watcher := &stubWorkRequestsWatcher{}
		client := &stubNetworkLoadBalancerClient{
			getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
				NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("ocid1.networkloadbalancer.oc1..existing"),
					DisplayName: new("shared-nlb"),
					Listeners: map[string]networkloadbalancer.Listener{
						"rtmp": {
							Name: new("rtmp"),
						},
						"old": {
							Name: new("old"),
						},
					},
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_rtmp": {
							Name: new("bs_rtmp"),
						},
						"bs_old": {
							Name: new("bs_old"),
						},
					},
				},
			},
			deleteListenerResponse: networkloadbalancer.DeleteListenerResponse{
				OpcWorkRequestId: new("delete-listener-work-request"),
			},
			deleteBackendSetResponse: networkloadbalancer.DeleteBackendSetResponse{
				OpcWorkRequestId: new("delete-backend-set-work-request"),
			},
			updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: new("update-backend-set-work-request"),
			},
			updateListenerResponse: networkloadbalancer.UpdateListenerResponse{
				OpcWorkRequestId: new("update-listener-work-request"),
			},
		}
		model := newModel(client, watcher)
		details := newDetails()
		details.gateway.Spec.Listeners = []gatewayv1.Listener{
			{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				Port:     1935,
			},
		}

		err := model.programGateway(t.Context(), details)

		require.NoError(t, err)
		require.Len(t, client.deleteListenerRequests, 1)
		assert.Equal(t, "old", lo.FromPtr(client.deleteListenerRequests[0].ListenerName))
		require.Len(t, client.deleteBackendSetRequests, 1)
		assert.Equal(t, "bs_old", lo.FromPtr(client.deleteBackendSetRequests[0].BackendSetName))
		assert.ElementsMatch(t,
			[]string{
				"update-backend-set-work-request",
				"update-listener-work-request",
				"delete-listener-work-request",
				"delete-backend-set-work-request",
			},
			watcher.waited,
		)
	})

	t.Run("wraps stale cleanup errors", func(t *testing.T) {
		for name, client := range map[string]*stubNetworkLoadBalancerClient{
			"listener": {
				deleteListenerErr: errors.New("delete listener failed"),
			},
			"backend set": {
				deleteBackendSetErr: errors.New("delete backend set failed"),
			},
		} {
			t.Run(name, func(t *testing.T) {
				client.getResponse = networkloadbalancer.GetNetworkLoadBalancerResponse{
					NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
						Id:          new("ocid1.networkloadbalancer.oc1..existing"),
						DisplayName: new("shared-nlb"),
						Listeners: map[string]networkloadbalancer.Listener{
							"old": {Name: new("old")},
						},
						BackendSets: map[string]networkloadbalancer.BackendSet{
							"bs_old": {Name: new("bs_old")},
						},
					},
				}
				model := newModel(client, &stubWorkRequestsWatcher{})

				err := model.programGateway(t.Context(), newDetails())

				require.Error(t, err)
			})
		}
	})

	t.Run("wraps programmed condition errors", func(t *testing.T) {
		model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger:     diag.RootTestLogger(),
			ResourcesModel: &stubResourcesModel{setErr: errors.New("status failed")},
		})

		err := model.setProgrammed(t.Context(), newDetails(), nil)

		require.ErrorContains(t, err, "failed to set programmed condition")
	})

	t.Run("deprovisions gateway without deleting existing network load balancer", func(t *testing.T) {
		nlbClient := &stubNetworkLoadBalancerClient{}
		k8sClient := NewMockk8sClient(t)
		k8sClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerGatewayProgrammedFinalizer)
				assert.NotContains(t, obj.GetAnnotations(), NetworkLoadBalancerGatewayIDAnnotation)
				return nil
			})
		model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciClient:           nlbClient,
			WorkRequestsWatcher: &stubWorkRequestsWatcher{},
		})
		details := newDetails()
		details.gateway.Finalizers = []string{NetworkLoadBalancerGatewayProgrammedFinalizer}
		details.gateway.Annotations = map[string]string{NetworkLoadBalancerGatewayIDAnnotation: "nlb-id"}

		err := model.deprovisionGateway(t.Context(), details)

		require.NoError(t, err)
	})

	t.Run("removes finalizer when network load balancer is already gone", func(t *testing.T) {
		nlbClient := &stubNetworkLoadBalancerClient{}
		k8sClient := NewMockk8sClient(t)
		k8sClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerGatewayProgrammedFinalizer)
				return nil
			})
		model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciClient:           nlbClient,
			WorkRequestsWatcher: &stubWorkRequestsWatcher{},
		})
		details := newDetails()
		details.gateway.Finalizers = []string{NetworkLoadBalancerGatewayProgrammedFinalizer}

		err := model.deprovisionGateway(t.Context(), details)

		require.NoError(t, err)
	})

	t.Run("wraps deprovision update errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		k8sClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).
			Return(errors.New("update failed"))
		model := newNetworkLoadBalancerGatewayModel(networkLoadBalancerGatewayModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciClient:           &stubNetworkLoadBalancerClient{},
			WorkRequestsWatcher: &stubWorkRequestsWatcher{},
		})
		details := newDetails()
		details.gateway.Finalizers = []string{NetworkLoadBalancerGatewayProgrammedFinalizer}

		err := model.deprovisionGateway(t.Context(), details)
		require.ErrorContains(t, err, "failed to remove finalizer from Gateway")
	})

	t.Run("wraps listener and backend set reconcile errors", func(t *testing.T) {
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935}
		busyErr := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("InvalidStateTransition"),
			ociapi.RandomServiceErrorWithMessage(
				"Invalid State Transition of NLB lifeCycle state from Updating to Updating",
			),
		)
		for name, tc := range map[string]struct {
			client   *stubNetworkLoadBalancerClient
			wantBusy bool
			msg      string
		}{
			"create backend set": {
				client: &stubNetworkLoadBalancerClient{createBackendSetErr: errors.New("create backend set failed")},
				msg:    "failed to create OCI Network Load Balancer backend set",
			},
			"update backend set": {
				client: &stubNetworkLoadBalancerClient{updateBackendSetErr: errors.New("update backend set failed")},
				msg:    "failed to update OCI Network Load Balancer backend set",
			},
			"busy create backend set": {
				client:   &stubNetworkLoadBalancerClient{createBackendSetErr: busyErr},
				wantBusy: true,
			},
			"busy update backend set": {
				client:   &stubNetworkLoadBalancerClient{updateBackendSetErr: busyErr},
				wantBusy: true,
			},
			"create listener": {
				client: &stubNetworkLoadBalancerClient{
					createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
						OpcWorkRequestId: new("create-backend-set-work"),
					},
					createListenerErr: errors.New("create listener failed"),
				},
				msg: "failed to create OCI Network Load Balancer listener",
			},
			"busy create listener": {
				client: &stubNetworkLoadBalancerClient{
					createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
						OpcWorkRequestId: new("create-backend-set-work"),
					},
					createListenerErr: busyErr,
				},
				wantBusy: true,
			},
			"update listener": {
				client: &stubNetworkLoadBalancerClient{
					updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
						OpcWorkRequestId: new("update-backend-set-work"),
					},
					updateListenerErr: errors.New("update listener failed"),
				},
				msg: "failed to update OCI Network Load Balancer listener",
			},
			"busy update listener": {
				client: &stubNetworkLoadBalancerClient{
					updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
						OpcWorkRequestId: new("update-backend-set-work"),
					},
					updateListenerErr: busyErr,
				},
				wantBusy: true,
			},
		} {
			t.Run(name, func(t *testing.T) {
				nlb := networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
				}
				if tc.client.updateBackendSetErr != nil || tc.client.updateListenerErr != nil {
					nlb.BackendSets = map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}}
				}
				if tc.client.updateListenerErr != nil {
					nlb.Listeners = map[string]networkloadbalancer.Listener{"rtmp": {Name: new("rtmp")}}
				}
				model := newModel(tc.client, &stubWorkRequestsWatcher{})

				err := mustNetworkLoadBalancerGatewayModelImpl(t, model).reconcileListener(t.Context(), nlb, listener)

				if tc.wantBusy {
					var gotBusyErr *networkLoadBalancerBusyError
					require.ErrorAs(t, err, &gotBusyErr)
				} else {
					require.ErrorContains(t, err, tc.msg)
				}
			})
		}
	})

	t.Run("waits for listener and backend set work requests", func(t *testing.T) {
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935}
		for name, tc := range map[string]struct {
			client *stubNetworkLoadBalancerClient
			nlb    networkloadbalancer.NetworkLoadBalancer
			waited string
		}{
			"create backend set": {
				client: &stubNetworkLoadBalancerClient{
					createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
						OpcWorkRequestId: new("create-backend-set-work"),
					},
					createListenerErr: errors.New("stop after backend set"),
				},
				nlb:    networkloadbalancer.NetworkLoadBalancer{Id: new("nlb-id")},
				waited: "create-backend-set-work",
			},
			"update backend set": {
				client: &stubNetworkLoadBalancerClient{
					updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
						OpcWorkRequestId: new("update-backend-set-work"),
					},
					createListenerErr: errors.New("stop after backend set"),
				},
				nlb: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
				},
				waited: "update-backend-set-work",
			},
			"create listener": {
				client: &stubNetworkLoadBalancerClient{
					createBackendSetResponse: networkloadbalancer.CreateBackendSetResponse{
						OpcWorkRequestId: new("create-backend-set-work"),
					},
					createListenerResponse: networkloadbalancer.CreateListenerResponse{
						OpcWorkRequestId: new("create-listener-work"),
					},
				},
				nlb:    networkloadbalancer.NetworkLoadBalancer{Id: new("nlb-id")},
				waited: "create-listener-work",
			},
			"update listener": {
				client: &stubNetworkLoadBalancerClient{
					updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
						OpcWorkRequestId: new("update-backend-set-work"),
					},
					updateListenerResponse: networkloadbalancer.UpdateListenerResponse{
						OpcWorkRequestId: new("update-listener-work"),
					},
				},
				nlb: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
					Listeners:   map[string]networkloadbalancer.Listener{"rtmp": {Name: new("rtmp")}},
				},
				waited: "update-listener-work",
			},
		} {
			t.Run(name, func(t *testing.T) {
				watcher := &stubWorkRequestsWatcher{}
				model := newModel(tc.client, watcher)

				err := mustNetworkLoadBalancerGatewayModelImpl(
					t,
					model,
				).reconcileListener(t.Context(), tc.nlb, listener)

				if tc.client.createListenerErr != nil {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				assert.Contains(t, watcher.waited, tc.waited)
			})
		}
	})

	t.Run("returns errors when listener or backend set work request id is missing", func(t *testing.T) {
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935}
		for name, nlb := range map[string]networkloadbalancer.NetworkLoadBalancer{
			"create backend set": {
				Id: new("nlb-id"),
			},
			"update backend set": {
				Id:          new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
			},
			"create listener": {
				Id:          new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": matchingNLBBackendSet(listener)},
			},
			"update listener": {
				Id:          new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": matchingNLBBackendSet(listener)},
				Listeners:   map[string]networkloadbalancer.Listener{"rtmp": {Name: new("rtmp")}},
			},
		} {
			t.Run(name, func(t *testing.T) {
				client := &stubNetworkLoadBalancerClient{}
				switch name {
				case "create backend set":
					client.omitCreateBackendSetWorkRequest = true
				case "update backend set":
					client.omitUpdateBackendSetWorkRequest = true
				case "create listener":
					client.omitCreateListenerWorkRequest = true
				case "update listener":
					client.omitUpdateListenerWorkRequest = true
				}
				model := newModel(client, &stubWorkRequestsWatcher{})

				err := mustNetworkLoadBalancerGatewayModelImpl(t, model).reconcileListener(t.Context(), nlb, listener)

				require.ErrorContains(t, err, "missing work request id")
			})
		}
	})

	t.Run("wraps cleanup wait and program setup errors", func(t *testing.T) {
		busyErr := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("InvalidStateTransition"),
			ociapi.RandomServiceErrorWithMessage(
				"Invalid State Transition of NLB lifeCycle state from Updating to Updating",
			),
		)
		model := newModel(&stubNetworkLoadBalancerClient{
			deleteListenerResponse: networkloadbalancer.DeleteListenerResponse{
				OpcWorkRequestId: new("delete-listener-work"),
			},
		}, &stubWorkRequestsWatcher{err: errors.New("wait failed")})
		modelImpl := mustNetworkLoadBalancerGatewayModelImpl(t, model)
		err := modelImpl.removeMissingListeners(t.Context(), networkloadbalancer.NetworkLoadBalancer{
			Id:        new("nlb-id"),
			Listeners: map[string]networkloadbalancer.Listener{"old": {Name: new("old")}},
		}, nil)
		require.ErrorContains(t, err, "failed waiting for stale listener old deletion")

		model = newModel(&stubNetworkLoadBalancerClient{
			deleteBackendSetResponse: networkloadbalancer.DeleteBackendSetResponse{
				OpcWorkRequestId: new("delete-backend-set-work"),
			},
		}, &stubWorkRequestsWatcher{err: errors.New("wait failed")})
		modelImpl = mustNetworkLoadBalancerGatewayModelImpl(t, model)
		err = modelImpl.removeMissingBackendSets(t.Context(), networkloadbalancer.NetworkLoadBalancer{
			Id:          new("nlb-id"),
			BackendSets: map[string]networkloadbalancer.BackendSet{"bs_old": {Name: new("bs_old")}},
		}, nil)
		require.ErrorContains(t, err, "failed waiting for stale backend set bs_old deletion")

		model = newModel(
			&stubNetworkLoadBalancerClient{omitDeleteListenerWorkRequest: true},
			&stubWorkRequestsWatcher{},
		)
		modelImpl = mustNetworkLoadBalancerGatewayModelImpl(t, model)
		err = modelImpl.removeMissingListeners(t.Context(), networkloadbalancer.NetworkLoadBalancer{
			Id:        new("nlb-id"),
			Listeners: map[string]networkloadbalancer.Listener{"old": {Name: new("old")}},
		}, nil)
		require.ErrorContains(t, err, "missing work request id")

		model = newModel(
			&stubNetworkLoadBalancerClient{omitDeleteBackendSetWorkRequest: true},
			&stubWorkRequestsWatcher{},
		)
		modelImpl = mustNetworkLoadBalancerGatewayModelImpl(t, model)
		err = modelImpl.removeMissingBackendSets(t.Context(), networkloadbalancer.NetworkLoadBalancer{
			Id:          new("nlb-id"),
			BackendSets: map[string]networkloadbalancer.BackendSet{"bs_old": {Name: new("bs_old")}},
		}, nil)
		require.ErrorContains(t, err, "missing work request id")

		model = newModel(
			&stubNetworkLoadBalancerClient{deleteListenerErr: busyErr},
			&stubWorkRequestsWatcher{},
		)
		modelImpl = mustNetworkLoadBalancerGatewayModelImpl(t, model)
		err = modelImpl.removeMissingListeners(t.Context(), networkloadbalancer.NetworkLoadBalancer{
			Id:        new("nlb-id"),
			Listeners: map[string]networkloadbalancer.Listener{"old": {Name: new("old")}},
		}, nil)
		var gotBusyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &gotBusyErr)

		model = newModel(
			&stubNetworkLoadBalancerClient{deleteBackendSetErr: busyErr},
			&stubWorkRequestsWatcher{},
		)
		modelImpl = mustNetworkLoadBalancerGatewayModelImpl(t, model)
		err = modelImpl.removeMissingBackendSets(t.Context(), networkloadbalancer.NetworkLoadBalancer{
			Id:          new("nlb-id"),
			BackendSets: map[string]networkloadbalancer.BackendSet{"bs_old": {Name: new("bs_old")}},
		}, nil)
		gotBusyErr = nil
		require.ErrorAs(t, err, &gotBusyErr)

		model = newModel(&stubNetworkLoadBalancerClient{getErr: errors.New("get failed")}, &stubWorkRequestsWatcher{})
		err = model.programGateway(t.Context(), newDetails())
		require.ErrorContains(t, err, "failed to get OCI Network Load Balancer")

		assert.NotContains(t,
			desiredNetworkLoadBalancerListenerNames([]gatewayv1.Listener{{
				Name:     "http",
				Protocol: gatewayv1.HTTPProtocolType,
				Port:     80,
			}}),
			"http",
		)
		assert.NotContains(t,
			desiredNetworkLoadBalancerBackendSetNames([]gatewayv1.Listener{{
				Name:     "http",
				Protocol: gatewayv1.HTTPProtocolType,
				Port:     80,
			}}),
			"bs_http",
		)
	})

	t.Run("programGateway wraps stale resource deletion errors", func(t *testing.T) {
		for name, client := range map[string]*stubNetworkLoadBalancerClient{
			"listener": {
				getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
					NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
						Id:        new("nlb-id"),
						Listeners: map[string]networkloadbalancer.Listener{"old": {Name: new("old")}},
					},
				},
				deleteListenerErr: errors.New("delete listener failed"),
			},
			"backend set": {
				getResponse: networkloadbalancer.GetNetworkLoadBalancerResponse{
					NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
						Id:          new("nlb-id"),
						BackendSets: map[string]networkloadbalancer.BackendSet{"bs_old": {Name: new("bs_old")}},
					},
				},
				deleteBackendSetErr: errors.New("delete backend set failed"),
			},
		} {
			t.Run(name, func(t *testing.T) {
				model := newModel(client, &stubWorkRequestsWatcher{})

				err := model.programGateway(t.Context(), newDetails())

				require.Error(t, err)
			})
		}
	})
}
