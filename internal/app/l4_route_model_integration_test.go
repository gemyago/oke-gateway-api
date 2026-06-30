package app

import (
	"testing"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

func newL4TestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, discoveryv1.AddToScheme(scheme))
	require.NoError(t, gatewayv1.Install(scheme))
	require.NoError(t, gatewayv1.Install(scheme))
	require.NoError(t, gatewayv1beta1.Install(scheme))
	require.NoError(t, types.AddKnownTypes(scheme))
	return scheme
}

func l4GatewayObjects(listener gatewayv1.Listener) []runtime.Object {
	return []runtime.Object{
		&gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
			},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{
						Group: ConfigRefGroup,
						Kind:  ConfigRefKind,
						Name:  "nlb-config",
					},
				},
				Listeners: []gatewayv1.Listener{listener},
			},
		},
		&types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..existing"},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend"},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Name: "traffic", Port: listener.Port}},
			},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "backend-a",
				Labels: map[string]string{
					discoveryv1.LabelServiceName: "backend",
				},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.0.10"},
					Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
				},
				{
					Addresses:  []string{"10.0.0.11"},
					Conditions: discoveryv1.EndpointConditions{Ready: new(false)},
				},
			},
		},
	}
}

func TestTCPRouteModelResolveAndProgram(t *testing.T) {
	port := gatewayv1.PortNumber(1935)
	listener := gatewayv1.Listener{
		Name:     "rtmp",
		Protocol: gatewayv1.TCPProtocolType,
		Port:     port,
	}
	route := &gatewayv1.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp", Generation: 7},
		Spec: gatewayv1.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("rtmp"))},
				},
			},
			Rules: []gatewayv1.TCPRouteRule{
				{
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "backend",
								Port: &port,
							},
						},
					},
				},
			},
		},
	}

	objects := append(l4GatewayObjects(listener), route)
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&gatewayv1.TCPRoute{}).
		Build()
	nlbClient := &stubNetworkLoadBalancerClient{}
	model := newTCPRouteModel(tcpRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
			networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_rtmp": {
						Name: new("bs_rtmp"),
					},
				},
			},
		},
		OciNetworkLoadBalancerAPI: nlbClient,
		WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
	})

	resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
	})
	require.NoError(t, err)
	require.Len(t, resolved, 1)

	err = model.programRoute(t.Context(), resolved[0])
	require.NoError(t, err)
	require.Len(t, nlbClient.updateBackendSetRequests, 1)
	update := nlbClient.updateBackendSetRequests[0]
	assert.Equal(t, "bs_rtmp", lo.FromPtr(update.BackendSetName))
	require.Len(t, update.UpdateBackendSetDetails.Backends, 1)
	assert.False(t, lo.FromPtr(update.UpdateBackendSetDetails.IsPreserveSource))
	assert.Equal(t, "10.0.0.10", lo.FromPtr(update.UpdateBackendSetDetails.Backends[0].IpAddress))
	assert.Equal(t, 1935, lo.FromPtr(update.UpdateBackendSetDetails.Backends[0].Port))
	assert.Nil(t, update.UpdateBackendSetDetails.HealthChecker.Port)

	err = model.setProgrammed(t.Context(), resolved[0])
	require.NoError(t, err)
	var updated gatewayv1.TCPRoute
	require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}, &updated))
	assert.Contains(t, updated.Finalizers, NetworkLoadBalancerTCPRouteProgrammedFinalizer)
	assert.Len(t, updated.Status.Parents, 1)
}

func TestUDPRouteModelResolveAndProgram(t *testing.T) {
	port := gatewayv1.PortNumber(5684)
	listener := gatewayv1.Listener{
		Name:     "coap",
		Protocol: gatewayv1.UDPProtocolType,
		Port:     port,
	}
	route := &gatewayv1.UDPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "iot",
			Name:       "coap",
			Generation: 7,
			Annotations: map[string]string{
				NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "9000",
			},
		},
		Spec: gatewayv1.UDPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("coap"))},
				},
			},
			Rules: []gatewayv1.UDPRouteRule{
				{
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "backend",
								Port: &port,
							},
						},
					},
				},
			},
		},
	}

	objects := append(l4GatewayObjects(listener), route)
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&gatewayv1.UDPRoute{}).
		Build()
	nlbClient := &stubNetworkLoadBalancerClient{}
	model := newUDPRouteModel(udpRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
			networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_coap": {
						Name: new("bs_coap"),
					},
				},
			},
		},
		OciNetworkLoadBalancerAPI: nlbClient,
		WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
	})

	resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
	})
	require.NoError(t, err)
	require.Len(t, resolved, 1)

	err = model.programRoute(t.Context(), resolved[0])
	require.NoError(t, err)
	require.Len(t, nlbClient.updateBackendSetRequests, 1)
	update := nlbClient.updateBackendSetRequests[0]
	assert.Equal(t, "bs_coap", lo.FromPtr(update.BackendSetName))
	require.Len(t, update.UpdateBackendSetDetails.Backends, 1)
	assert.False(t, lo.FromPtr(update.UpdateBackendSetDetails.IsPreserveSource))
	assert.Equal(t, "10.0.0.10", lo.FromPtr(update.UpdateBackendSetDetails.Backends[0].IpAddress))
	assert.Equal(t, 5684, lo.FromPtr(update.UpdateBackendSetDetails.Backends[0].Port))
	assert.Equal(t, 9000, lo.FromPtr(update.UpdateBackendSetDetails.HealthChecker.Port))

	err = model.setProgrammed(t.Context(), resolved[0])
	require.NoError(t, err)
	var updated gatewayv1.UDPRoute
	require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "coap"}, &updated))
	assert.Contains(t, updated.Finalizers, NetworkLoadBalancerUDPRouteProgrammedFinalizer)
	assert.Len(t, updated.Status.Parents, 1)
}

func TestTLSRouteModelResolveAndProgramNLBPassthrough(t *testing.T) {
	port := gatewayv1.PortNumber(443)
	listener := gatewayv1.Listener{
		Name:     "tls",
		Protocol: gatewayv1.TLSProtocolType,
		Port:     port,
		TLS: &gatewayv1.ListenerTLSConfig{
			Mode: lo.ToPtr(gatewayv1.TLSModePassthrough),
		},
	}
	route := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls", Generation: 7},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("tls"))},
				},
			},
			Hostnames: []gatewayv1.Hostname{"passthrough.example.com"},
			Rules: []gatewayv1.TLSRouteRule{
				{
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "backend",
								Port: &port,
							},
						},
					},
				},
			},
		},
	}

	objects := append(l4GatewayObjects(listener), route)
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&gatewayv1.TLSRoute{}).
		Build()
	nlbClient := &stubNetworkLoadBalancerClient{}
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
			networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_tls": {
						Name: new("bs_tls"),
					},
				},
			},
		},
		OciNetworkLoadBalancerAPI: nlbClient,
		NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
	})

	resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "tls"},
	})
	require.NoError(t, err)
	require.Len(t, resolved, 1)

	err = model.programRoute(t.Context(), resolved[0])
	require.NoError(t, err)
	require.Len(t, nlbClient.updateBackendSetRequests, 1)
	update := nlbClient.updateBackendSetRequests[0]
	assert.Equal(t, "bs_tls", lo.FromPtr(update.BackendSetName))
	require.Len(t, update.UpdateBackendSetDetails.Backends, 1)
	assert.False(t, lo.FromPtr(update.UpdateBackendSetDetails.IsPreserveSource))
	assert.Equal(t, "10.0.0.10", lo.FromPtr(update.UpdateBackendSetDetails.Backends[0].IpAddress))
	assert.Equal(t, 443, lo.FromPtr(update.UpdateBackendSetDetails.Backends[0].Port))
	assert.Equal(t, 443, lo.FromPtr(update.UpdateBackendSetDetails.HealthChecker.Port))

	err = model.setProgrammed(t.Context(), resolved[0])
	require.NoError(t, err)
	var updated gatewayv1.TLSRoute
	require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "tls"}, &updated))
	assert.Contains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
	assert.Len(t, updated.Status.Parents, 1)
}

func TestL4RouteEndpointBackendResolution(t *testing.T) {
	port := gatewayv1.PortNumber(1935)
	objects := []runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend"},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{
					Name:       "traffic",
					Port:       port,
					TargetPort: intstr.FromInt(8080),
				}},
			},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "backend-a",
				Labels:    map[string]string{discoveryv1.LabelServiceName: "backend"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses: []string{"10.0.0.10"},
					Conditions: discoveryv1.EndpointConditions{
						Ready:       new(true),
						Terminating: new(true),
					},
				},
				{
					Addresses:  []string{},
					Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(objects...).
		Build()
	tcpModel := mustTCPRouteModelImpl(t, newTCPRouteModel(tcpRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
	}))
	udpModel := mustUDPRouteModelImpl(t, newUDPRouteModel(udpRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
	}))
	tcpRoute := gatewayv1.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
		Spec: gatewayv1.TCPRouteSpec{Rules: []gatewayv1.TCPRouteRule{
			{BackendRefs: []gatewayv1.BackendRef{
				{
					BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					Weight:                 new(int32(2)),
				},
				{
					BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					Weight:                 new(int32(3)),
				},
				{
					BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					Weight:                 new(int32(0)),
				},
			}},
		}},
	}
	udpRoute := gatewayv1.UDPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
		Spec: gatewayv1.UDPRouteSpec{Rules: []gatewayv1.UDPRouteRule{
			{BackendRefs: []gatewayv1.BackendRef{
				{
					BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					Weight:                 new(int32(2)),
				},
				{
					BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					Weight:                 new(int32(3)),
				},
			}},
		}},
	}

	tcpBackends, err := tcpModel.endpointBackendsForRoute(t.Context(), tcpRoute)
	require.NoError(t, err)
	require.Len(t, tcpBackends, 1)
	assert.Equal(t, 5, lo.FromPtr(tcpBackends[0].Weight))
	assert.True(t, lo.FromPtr(tcpBackends[0].IsDrain))
	assert.Equal(t, 8080, lo.FromPtr(tcpBackends[0].Port))

	udpBackends, err := udpModel.endpointBackendsForRoute(t.Context(), udpRoute)
	require.NoError(t, err)
	require.Len(t, udpBackends, 1)
	assert.Equal(t, 5, lo.FromPtr(udpBackends[0].Weight))
	assert.Equal(t, 8080, lo.FromPtr(udpBackends[0].Port))

	invalidKind := gatewayv1.Kind("Deployment")
	tcpRoute.Spec.Rules[0].BackendRefs = []gatewayv1.BackendRef{{
		BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Kind: &invalidKind, Port: &port},
	}}
	_, err = tcpModel.endpointBackendsForRoute(t.Context(), tcpRoute)
	require.ErrorContains(t, err, "unsupported referent")

	udpRoute.Spec.Rules[0].BackendRefs = []gatewayv1.BackendRef{{
		BackendObjectReference: gatewayv1.BackendObjectReference{Name: "missing"},
	}}
	_, err = udpModel.endpointBackendsForRoute(t.Context(), udpRoute)
	require.ErrorContains(t, err, "missing port")

	udpRoute.Spec.Rules[0].BackendRefs = []gatewayv1.BackendRef{{
		BackendObjectReference: gatewayv1.BackendObjectReference{Name: "missing", Port: &port},
	}}
	_, err = udpModel.endpointBackendsForRoute(t.Context(), udpRoute)
	require.ErrorContains(t, err, "not found")
}

func TestL4RouteResolveRequestBranches(t *testing.T) {
	tcpPort := gatewayv1.PortNumber(1935)
	udpPort := gatewayv1.PortNumber(5684)
	serviceKindRef := gatewayv1.Kind(serviceKind)
	tcpRoute := &gatewayv1.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
		Spec: gatewayv1.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Kind: &serviceKindRef, Name: "ignored"},
				{Name: "missing"},
				{Name: "wrong-class"},
				{Name: "no-infra"},
				{Name: "missing-config"},
				{Name: "no-match-tcp"},
			},
		}},
	}
	udpRoute := &gatewayv1.UDPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
		Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Kind: &serviceKindRef, Name: "ignored"},
				{Name: "missing"},
				{Name: "wrong-class"},
				{Name: "no-infra"},
				{Name: "missing-config"},
				{Name: "no-match-udp"},
			},
		}},
	}
	objects := []runtime.Object{
		tcpRoute,
		udpRoute,
		&gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
			},
		},
		&gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "other"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "wrong-class"},
			Spec:       gatewayv1.GatewaySpec{GatewayClassName: "other"},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "no-infra"},
			Spec:       gatewayv1.GatewaySpec{GatewayClassName: "oke-nlb"},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "missing-config"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "missing"},
				},
			},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "no-match-tcp"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
				Listeners: []gatewayv1.Listener{
					{Name: "udp", Protocol: gatewayv1.UDPProtocolType, Port: udpPort},
				},
			},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "no-match-udp"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
				Listeners: []gatewayv1.Listener{
					{Name: "tcp", Protocol: gatewayv1.TCPProtocolType, Port: tcpPort},
				},
			},
		},
		&types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "ocid1.networkloadbalancer.oc1..existing"},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&gatewayv1.TCPRoute{}, &gatewayv1.UDPRoute{}).
		Build()
	tcpModel := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
	udpModel := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

	tcpResolved, err := tcpModel.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
	})
	require.NoError(t, err)
	assert.Empty(t, tcpResolved)
	udpResolved, err := udpModel.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
	})
	require.NoError(t, err)
	assert.Empty(t, udpResolved)

	_, err = tcpModel.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "missing-route"},
	})
	require.NoError(t, err)
	_, err = udpModel.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "missing-route"},
	})
	require.NoError(t, err)
}

func TestL4RouteProgramRouteStaleAndUpToDate(t *testing.T) {
	for name, protocol := range map[string]gatewayv1.ProtocolType{
		"tcp": gatewayv1.TCPProtocolType,
		"udp": gatewayv1.UDPProtocolType,
	} {
		t.Run(name, func(t *testing.T) {
			port := gatewayv1.PortNumber(1935)
			listener := gatewayv1.Listener{Name: "rtmp", Protocol: protocol, Port: port}
			routeKey := apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}
			objects := l4GatewayObjects(listener)
			if protocol == gatewayv1.TCPProtocolType {
				objects = append(objects, &gatewayv1.TCPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:  routeKey.Namespace,
						Name:       routeKey.Name,
						Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
						Annotations: map[string]string{
							NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_old",
						},
					},
					Spec: gatewayv1.TCPRouteSpec{
						CommonRouteSpec: gatewayv1.CommonRouteSpec{
							ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
						},
						Rules: []gatewayv1.TCPRouteRule{{BackendRefs: []gatewayv1.BackendRef{{
							BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
						}}}},
					},
				})
			} else {
				objects = append(objects, &gatewayv1.UDPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:  routeKey.Namespace,
						Name:       routeKey.Name,
						Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
						Annotations: map[string]string{
							NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_old",
							NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation:       "1935",
						},
					},
					Spec: gatewayv1.UDPRouteSpec{
						CommonRouteSpec: gatewayv1.CommonRouteSpec{
							ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
						},
						Rules: []gatewayv1.UDPRouteRule{{BackendRefs: []gatewayv1.BackendRef{{
							BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
						}}}},
					},
				})
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithRuntimeObjects(objects...).
				Build()
			nlb := networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_old":  {Name: new("bs_old")},
					"bs_rtmp": {Name: new("bs_rtmp")},
				},
			}
			nlbClient := &stubNetworkLoadBalancerClient{}
			details := resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			}

			if protocol == gatewayv1.TCPProtocolType {
				model := newTCPRouteModel(tcpRouteModelDeps{
					RootLogger:                diag.RootTestLogger(),
					K8sClient:                 k8sClient,
					NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{networkLoadBalancer: nlb},
					OciNetworkLoadBalancerAPI: nlbClient,
					WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
				})
				var route gatewayv1.TCPRoute
				require.NoError(t, k8sClient.Get(t.Context(), routeKey, &route))
				err := model.programRoute(t.Context(), resolvedTCPRouteDetails{
					gatewayDetails:  details,
					tcpRoute:        route,
					matchedListener: listener,
				})
				require.NoError(t, err)
			} else {
				model := newUDPRouteModel(udpRouteModelDeps{
					RootLogger:                diag.RootTestLogger(),
					K8sClient:                 k8sClient,
					NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{networkLoadBalancer: nlb},
					OciNetworkLoadBalancerAPI: nlbClient,
					WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
				})
				var route gatewayv1.UDPRoute
				require.NoError(t, k8sClient.Get(t.Context(), routeKey, &route))
				err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
					gatewayDetails:  details,
					udpRoute:        route,
					matchedListener: listener,
				})
				require.NoError(t, err)
			}
			require.Len(t, nlbClient.updateBackendSetRequests, 2)
			assert.Equal(t, "bs_old", lo.FromPtr(nlbClient.updateBackendSetRequests[0].BackendSetName))
			assert.Equal(t, "bs_rtmp", lo.FromPtr(nlbClient.updateBackendSetRequests[1].BackendSetName))
		})
	}
}
