package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

func albTLSRouteObjects(listener gatewayv1.Listener) []runtime.Object {
	return []runtime.Object{
		&gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		},
		&gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{
						Group: ConfigRefGroup,
						Kind:  ConfigRefKind,
						Name:  "alb-config",
					},
				},
				Listeners: []gatewayv1.Listener{listener},
			},
		},
		&types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "ocid1.loadbalancer.oc1..existing"},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Name: "rtmp", Port: 1935}},
			},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmp-a",
				Labels: map[string]string{
					discoveryv1.LabelServiceName: "rtmp",
				},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.1.10"},
					Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
				},
			},
		},
	}
}

func TestTLSRouteModelResolveAndProgramALBTerminate(t *testing.T) {
	listener := gatewayv1.Listener{
		Name:     "rtmps",
		Protocol: gatewayv1.TLSProtocolType,
		Port:     443,
		TLS: &gatewayv1.ListenerTLSConfig{
			Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
			CertificateRefs: []gatewayv1.SecretObjectReference{{
				Name: "rtmps-cert",
			}},
		},
	}
	backendPort := gatewayv1.PortNumber(1935)
	route := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps", Generation: 4},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("rtmps"))},
				},
			},
			Hostnames: []gatewayv1.Hostname{"rtmps.example.com"},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "rtmp",
						Port: &backendPort,
					},
				}},
			}},
		},
	}

	objects := append(albTLSRouteObjects(listener), route)
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&gatewayv1.TLSRoute{}).
		Build()
	ociClient := NewMockociLoadBalancerClient(t)
	ociModel := NewMockociLoadBalancerModel(t)
	watcher := &stubWorkRequestsWatcher{}
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger:           diag.RootTestLogger(),
		K8sClient:            k8sClient,
		OciLoadBalancerAPI:   ociClient,
		OciLoadBalancerModel: ociModel,
		WorkRequestsWatcher:  watcher,
	})
	workRequestID := "wr-tlsroute"
	certName := "media-rtmps-cert-rev-1"
	loadBalancerID := "ocid1.loadbalancer.oc1..existing"
	ociClient.EXPECT().
		GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: &loadBalancerID,
		}).
		Return(loadbalancer.GetLoadBalancerResponse{
			LoadBalancer: loadbalancer.LoadBalancer{
				BackendSets:  map[string]loadbalancer.BackendSet{},
				Listeners:    map[string]loadbalancer.Listener{},
				Certificates: map[string]loadbalancer.Certificate{},
			},
		}, nil)
	ociClient.EXPECT().
		CreateBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.CreateBackendSetRequest) bool {
			return lo.FromPtr(request.LoadBalancerId) == "ocid1.loadbalancer.oc1..existing" &&
				lo.FromPtr(request.CreateBackendSetDetails.Policy) == tlsRouteBackendSetPolicy &&
				lo.FromPtr(request.CreateBackendSetDetails.HealthChecker.Protocol) == "TCP" &&
				lo.FromPtr(request.CreateBackendSetDetails.HealthChecker.Port) == 1935 &&
				len(request.CreateBackendSetDetails.Backends) == 1 &&
				lo.FromPtr(request.CreateBackendSetDetails.Backends[0].IpAddress) == "10.0.1.10" &&
				lo.FromPtr(request.CreateBackendSetDetails.Backends[0].Port) == 1935
		})).
		Return(loadbalancer.CreateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
	ociModel.EXPECT().
		reconcileListenersCertificates(t.Context(), mock.Anything).
		Return(reconcileListenersCertificatesResult{
			certificatesByListener: map[string][]loadbalancer.Certificate{
				"rtmps": {{CertificateName: &certName}},
			},
		}, nil)
	ociClient.EXPECT().
		CreateListener(t.Context(), mock.MatchedBy(func(request loadbalancer.CreateListenerRequest) bool {
			return lo.FromPtr(request.LoadBalancerId) == "ocid1.loadbalancer.oc1..existing" &&
				lo.FromPtr(request.CreateListenerDetails.Name) == "rtmps" &&
				lo.FromPtr(request.CreateListenerDetails.Protocol) == tlsRouteLoadBalancerProtocol &&
				lo.FromPtr(request.CreateListenerDetails.Port) == 443 &&
				request.CreateListenerDetails.SslConfiguration != nil &&
				lo.FromPtr(request.CreateListenerDetails.SslConfiguration.CertificateName) == certName
		})).
		Return(loadbalancer.CreateListenerResponse{OpcWorkRequestId: &workRequestID}, nil)

	resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: "media", Name: "rtmps"},
	})
	require.NoError(t, err)
	require.Len(t, resolved, 1)

	err = model.programRoute(t.Context(), resolved[0])
	require.NoError(t, err)

	err = model.setProgrammed(t.Context(), resolved[0])
	require.NoError(t, err)
	var updated gatewayv1.TLSRoute
	require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{Namespace: "media", Name: "rtmps"}, &updated))
	assert.Contains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
	assert.Equal(t,
		map[string]string{"rtmps": tlsRouteBackendSetName(*route, listener)},
		annotatedLoadBalancerTLSRouteResources(&updated),
	)
	assert.Len(t, updated.Status.Parents, 1)
	assert.Equal(t, ControllerClassName, string(updated.Status.Parents[0].ControllerName))
	acceptedCondition := meta.FindStatusCondition(
		updated.Status.Parents[0].Conditions,
		string(gatewayv1.RouteConditionAccepted),
	)
	require.NotNil(t, acceptedCondition)
	assert.Equal(t, fmt.Sprintf("TLSRoute rtmps accepted by %s", ControllerClassName), acceptedCondition.Message)
}

func TestTLSRouteModelValidation(t *testing.T) {
	model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})
	baseDetails := func() resolvedTLSRouteDetails {
		return resolvedTLSRouteDetails{
			tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "rtmps"}},
			matchedListener: gatewayv1.Listener{
				Name:     "rtmps",
				Protocol: gatewayv1.TLSProtocolType,
				TLS: &gatewayv1.ListenerTLSConfig{
					Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
				},
			},
			gatewayDetails: resolvedGatewayDetails{
				gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
					ControllerName: ControllerClassName,
				}},
			},
		}
	}

	t.Run("accepts missing hostname", func(t *testing.T) {
		err := model.validateRoute(baseDetails())
		require.NoError(t, err)
	})

	t.Run("rejects ALB passthrough", func(t *testing.T) {
		details := baseDetails()
		details.tlsRoute.Spec.Hostnames = []gatewayv1.Hostname{"rtmps.example.com"}
		details.matchedListener.TLS.Mode = lo.ToPtr(gatewayv1.TLSModePassthrough)
		err := model.validateRoute(details)
		require.ErrorContains(t, err, "supports only Terminate mode")
	})

	t.Run("rejects NLB terminate", func(t *testing.T) {
		details := baseDetails()
		details.tlsRoute.Spec.Hostnames = []gatewayv1.Hostname{"rtmps.example.com"}
		details.gatewayDetails.gatewayClass.Spec.ControllerName = NetworkLoadBalancerControllerClassName
		err := model.validateRoute(details)
		require.ErrorContains(t, err, "supports only Passthrough mode")
	})

	t.Run("rejects missing tls mode", func(t *testing.T) {
		details := baseDetails()
		details.tlsRoute.Spec.Hostnames = []gatewayv1.Hostname{"rtmps.example.com"}
		details.matchedListener.TLS.Mode = nil
		err := model.validateRoute(details)
		require.ErrorContains(t, err, "must specify tls.mode")
	})

	t.Run("rejects unsupported controller", func(t *testing.T) {
		details := baseDetails()
		details.tlsRoute.Spec.Hostnames = []gatewayv1.Hostname{"rtmps.example.com"}
		details.gatewayDetails.gatewayClass.Spec.ControllerName = "example.com/controller"
		err := model.validateRoute(details)
		require.ErrorContains(t, err, "unsupported GatewayClass controller")
	})
}

func TestTLSRouteModelHealthCheckPort(t *testing.T) {
	backendPort := gatewayv1.PortNumber(443)
	route := gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"},
		Spec: gatewayv1.TLSRouteSpec{Rules: []gatewayv1.TLSRouteRule{{
			BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "rtmp",
				Port: &backendPort,
			}}},
		}}},
	}

	t.Run("uses numeric target port", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
					Port:       443,
					TargetPort: intstr.FromInt(1935),
				}}},
			}).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		port, err := model.routeHealthCheckPort(t.Context(), route)

		require.NoError(t, err)
		assert.Equal(t, 1935, port)
	})

	t.Run("uses endpoint port for named target port", func(t *testing.T) {
		portName := "tls"
		endpointPort := int32(8443)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Name:       portName,
						Port:       443,
						TargetPort: intstr.FromString(portName),
					}}},
				},
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "media",
						Name:      "rtmp-a",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "rtmp"},
					},
					Ports: []discoveryv1.EndpointPort{{
						Name: &portName,
						Port: &endpointPort,
					}},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		port, err := model.routeHealthCheckPort(t.Context(), route)

		require.NoError(t, err)
		assert.Equal(t, 8443, port)
	})

	t.Run("falls back to service port for named target without endpoint port", func(t *testing.T) {
		portName := "tls"
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Name:       portName,
						Port:       443,
						TargetPort: intstr.FromString(portName),
					}}},
				},
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "media",
						Name:      "rtmp-a",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "rtmp"},
					},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		port, err := model.routeHealthCheckPort(t.Context(), route)

		require.NoError(t, err)
		assert.Equal(t, 443, port)
	})

	t.Run("wraps endpoint slice list errors for named target port", func(t *testing.T) {
		portName := "tls"
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "media", Name: "rtmp"}, &corev1.Service{}).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*corev1.Service) = corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Name:       portName,
						Port:       443,
						TargetPort: intstr.FromString(portName),
					}}},
				}
				return nil
			})
		wantErr := errors.New("list failed")
		k8sClient.EXPECT().
			List(t.Context(), &discoveryv1.EndpointSliceList{},
				client.MatchingLabels{discoveryv1.LabelServiceName: "rtmp"},
				client.InNamespace("media")).
			Return(wantErr)

		_, err := model.routeHealthCheckPort(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to list endpoint slices")
	})

	t.Run("rejects routes without backend refs", func(t *testing.T) {
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

		_, err := model.routeHealthCheckPort(t.Context(), gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "empty"},
		})

		require.ErrorContains(t, err, "has no backendRefs")
	})
}

func TestTLSRouteModelProgramLoadBalancerTerminateRouteErrors(t *testing.T) {
	mode := gatewayv1.TLSModeTerminate
	backendPort := gatewayv1.PortNumber(1935)
	listener := gatewayv1.Listener{
		Name:     "rtmps",
		Protocol: gatewayv1.TLSProtocolType,
		Port:     443,
		TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
	}
	route := gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"},
		Spec: gatewayv1.TLSRouteSpec{
			Hostnames: []gatewayv1.Hostname{"rtmps.example.com"},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "rtmp",
					Port: &backendPort,
				}}},
			}},
		},
	}
	details := resolvedTLSRouteDetails{
		tlsRoute:        route,
		matchedListener: listener,
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
			config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "lb-id"}},
		},
	}

	t.Run("wraps load balancer get errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("get failed")
		ociClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{}, wantErr)

		err := model.programLoadBalancerTerminateRoute(t.Context(), details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get OCI Load Balancer")
	})

	t.Run("returns backend resolution errors", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			K8sClient:          k8sClient,
			OciLoadBalancerAPI: ociClient,
		})
		ociClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{
				BackendSets: map[string]loadbalancer.BackendSet{},
			}}, nil)

		err := model.programLoadBalancerTerminateRoute(t.Context(), details)

		require.ErrorContains(t, err, "backendRef service media/rtmp not found")
	})

	t.Run("wraps certificate reconciliation errors", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Port: 1935,
					}}},
				},
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "media",
						Name:      "rtmp-a",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "rtmp"},
					},
					Endpoints: []discoveryv1.Endpoint{{
						Addresses:  []string{"10.0.1.10"},
						Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
					}},
				},
			).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		ociModel := NewMockociLoadBalancerModel(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:           diag.RootTestLogger(),
			K8sClient:            k8sClient,
			OciLoadBalancerAPI:   ociClient,
			OciLoadBalancerModel: ociModel,
		})
		backendSetName := tlsRouteBackendSetName(route, listener)
		ociClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{
				BackendSets: map[string]loadbalancer.BackendSet{
					backendSetName: {
						Name:   &backendSetName,
						Policy: new(tlsRouteBackendSetPolicy),
						HealthChecker: &loadbalancer.HealthChecker{
							Protocol: new("TCP"),
							Port:     new(1935),
						},
						Backends: []loadbalancer.Backend{{
							IpAddress: new("10.0.1.10"),
							Port:      new(1935),
							Weight:    new(1),
							Drain:     new(false),
						}},
					},
				},
			}}, nil)
		wantErr := errors.New("cert failed")
		ociModel.EXPECT().
			reconcileListenersCertificates(t.Context(), mock.Anything).
			Return(reconcileListenersCertificatesResult{}, wantErr)

		err := model.programLoadBalancerTerminateRoute(t.Context(), details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to reconcile listener certificates")
	})
}

func TestTLSRouteModelLoadBalancerBackendSet(t *testing.T) {
	backends := []loadbalancer.BackendDetails{{
		IpAddress: new("10.0.1.10"),
		Port:      new(1935),
		Weight:    new(1),
	}}

	t.Run("updates changed backend set", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-update-backend-set"
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.UpdateBackendSetRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.BackendSetName) == "bs" &&
					lo.FromPtr(request.UpdateBackendSetDetails.HealthChecker.Port) == 1935 &&
					len(request.UpdateBackendSetDetails.Backends) == 1
			})).
			Return(loadbalancer.UpdateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

		err := model.reconcileLoadBalancerBackendSet(t.Context(), "lb-id", "bs", loadbalancer.BackendSet{
			Name:   new("bs"),
			Policy: new("LEAST_CONNECTIONS"),
			HealthChecker: &loadbalancer.HealthChecker{
				Protocol: new("TCP"),
				Port:     new(80),
			},
		}, backends, 1935)

		require.NoError(t, err)
	})

	t.Run("updates backend set with stale ssl configuration", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-remove-backend-set-ssl"
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.UpdateBackendSetRequest) bool {
				return request.UpdateBackendSetDetails.SslConfiguration == nil &&
					lo.FromPtr(request.UpdateBackendSetDetails.HealthChecker.Port) == 1935 &&
					len(request.UpdateBackendSetDetails.Backends) == 1
			})).
			Return(loadbalancer.UpdateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

		err := model.reconcileLoadBalancerBackendSet(t.Context(), "lb-id", "bs", loadbalancer.BackendSet{
			Name:   new("bs"),
			Policy: new(tlsRouteBackendSetPolicy),
			HealthChecker: &loadbalancer.HealthChecker{
				Protocol: new("TCP"),
				Port:     new(1935),
			},
			Backends: []loadbalancer.Backend{{
				IpAddress: new("10.0.1.10"),
				Port:      new(1935),
				Weight:    new(1),
			}},
			SslConfiguration: &loadbalancer.SslConfiguration{CertificateName: new("old-cert")},
		}, backends, 1935)

		require.NoError(t, err)
	})

	t.Run("skips matching backend set", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})

		err := model.reconcileLoadBalancerBackendSet(t.Context(), "lb-id", "bs", loadbalancer.BackendSet{
			Name:   new("bs"),
			Policy: new(tlsRouteBackendSetPolicy),
			HealthChecker: &loadbalancer.HealthChecker{
				Protocol: new("TCP"),
				Port:     new(1935),
			},
			Backends: []loadbalancer.Backend{{
				IpAddress: new("10.0.1.10"),
				Port:      new(1935),
				Weight:    new(1),
				Drain:     new(false),
			}},
		}, backends, 1935)

		require.NoError(t, err)
	})

	t.Run("wraps create errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("create failed")
		ociClient.EXPECT().
			CreateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.CreateBackendSetResponse{}, wantErr)

		err := model.reconcileLoadBalancerBackendSet(
			t.Context(),
			"lb-id",
			"bs",
			loadbalancer.BackendSet{},
			backends,
			1935,
		)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to create TLSRoute backend set")
	})

	t.Run("returns error when update work request is missing", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateBackendSetResponse{}, nil)

		err := model.reconcileLoadBalancerBackendSet(t.Context(), "lb-id", "bs", loadbalancer.BackendSet{
			Name: new("bs"),
		}, backends, 1935)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("wraps wait errors after update", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-update-backend-set"
		wantErr := errors.New("wait failed")
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

		err := model.reconcileLoadBalancerBackendSet(t.Context(), "lb-id", "bs", loadbalancer.BackendSet{
			Name: new("bs"),
		}, backends, 1935)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for TLSRoute backend set")
	})

	t.Run("wraps update errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("update failed")
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateBackendSetResponse{}, wantErr)

		err := model.reconcileLoadBalancerBackendSet(t.Context(), "lb-id", "bs", loadbalancer.BackendSet{
			Name: new("bs"),
		}, backends, 1935)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update TLSRoute backend set")
	})

	t.Run("returns error when create work request is missing", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		ociClient.EXPECT().
			CreateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.CreateBackendSetResponse{}, nil)

		err := model.reconcileLoadBalancerBackendSet(
			t.Context(),
			"lb-id",
			"bs",
			loadbalancer.BackendSet{},
			backends,
			1935,
		)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("wraps wait errors after create", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-create-backend-set"
		wantErr := errors.New("wait failed")
		ociClient.EXPECT().
			CreateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.CreateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

		err := model.reconcileLoadBalancerBackendSet(
			t.Context(),
			"lb-id",
			"bs",
			loadbalancer.BackendSet{},
			backends,
			1935,
		)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for TLSRoute backend set")
	})
}

func TestTLSRouteLoadBalancerBackendsEqual(t *testing.T) {
	assert.False(t, loadBalancerBackendsEqual(nil, []loadbalancer.BackendDetails{{IpAddress: new("10.0.1.10")}}))
	assert.False(t, loadBalancerBackendsEqual(
		[]loadbalancer.Backend{{IpAddress: new("10.0.1.10"), Port: new(1935), Weight: new(1)}},
		[]loadbalancer.BackendDetails{{IpAddress: new("10.0.1.10"), Port: new(1935), Weight: new(2)}},
	))
	assert.False(t, loadBalancerBackendsEqual(
		[]loadbalancer.Backend{{IpAddress: new("10.0.1.10"), Port: new(1935)}},
		[]loadbalancer.BackendDetails{{IpAddress: new("10.0.1.11"), Port: new(1935)}},
	))
}

func TestTLSRouteModelLoadBalancerListener(t *testing.T) {
	mode := gatewayv1.TLSModeTerminate
	listener := gatewayv1.Listener{
		Name:     "rtmps",
		Protocol: gatewayv1.TLSProtocolType,
		Port:     443,
		TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
	}
	sslConfig := &loadbalancer.SslConfigurationDetails{CertificateIds: []string{"ocid1.certificate.oc1..cert"}}

	t.Run("updates changed listener", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-update-listener"
		ociClient.EXPECT().
			UpdateListener(t.Context(), mock.MatchedBy(func(request loadbalancer.UpdateListenerRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.ListenerName) == "rtmps" &&
					lo.FromPtr(request.UpdateListenerDetails.Protocol) == tlsRouteLoadBalancerProtocol &&
					lo.FromPtr(request.UpdateListenerDetails.DefaultBackendSetName) == "bs"
			})).
			Return(loadbalancer.UpdateListenerResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

		err := model.reconcileLoadBalancerTLSListener(t.Context(), "lb-id", "bs", loadbalancer.Listener{
			Name:                  new("rtmps"),
			Protocol:              new("HTTP"),
			Port:                  new(443),
			DefaultBackendSetName: new("old"),
			RoutingPolicyName:     new("old-policy"),
		}, listener, sslConfig)

		require.NoError(t, err)
	})

	t.Run("skips matching listener", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})

		err := model.reconcileLoadBalancerTLSListener(t.Context(), "lb-id", "bs", loadbalancer.Listener{
			Name:                  new("rtmps"),
			Protocol:              new(tlsRouteLoadBalancerProtocol),
			Port:                  new(443),
			DefaultBackendSetName: new("bs"),
			SslConfiguration: &loadbalancer.SslConfiguration{
				CertificateIds: []string{"ocid1.certificate.oc1..cert"},
			},
		}, listener, sslConfig)

		require.NoError(t, err)
	})

	t.Run("wraps create errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("create listener failed")
		ociClient.EXPECT().
			CreateListener(t.Context(), mock.Anything).
			Return(loadbalancer.CreateListenerResponse{}, wantErr)

		err := model.reconcileLoadBalancerTLSListener(
			t.Context(),
			"lb-id",
			"bs",
			loadbalancer.Listener{},
			listener,
			sslConfig,
		)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to create TLSRoute listener")
	})

	t.Run("returns error when update work request is missing", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		ociClient.EXPECT().
			UpdateListener(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateListenerResponse{}, nil)

		err := model.reconcileLoadBalancerTLSListener(t.Context(), "lb-id", "bs", loadbalancer.Listener{
			Name:     new("rtmps"),
			Protocol: new("HTTP"),
		}, listener, sslConfig)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("wraps wait errors after update", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-update-listener"
		wantErr := errors.New("wait failed")
		ociClient.EXPECT().
			UpdateListener(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateListenerResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

		err := model.reconcileLoadBalancerTLSListener(t.Context(), "lb-id", "bs", loadbalancer.Listener{
			Name:     new("rtmps"),
			Protocol: new("HTTP"),
		}, listener, sslConfig)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for TLSRoute listener")
	})

	t.Run("wraps update errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("update listener failed")
		ociClient.EXPECT().
			UpdateListener(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateListenerResponse{}, wantErr)

		err := model.reconcileLoadBalancerTLSListener(t.Context(), "lb-id", "bs", loadbalancer.Listener{
			Name:     new("rtmps"),
			Protocol: new("HTTP"),
		}, listener, sslConfig)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update TLSRoute listener")
	})

	t.Run("returns error when create work request is missing", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		ociClient.EXPECT().
			CreateListener(t.Context(), mock.Anything).
			Return(loadbalancer.CreateListenerResponse{}, nil)

		err := model.reconcileLoadBalancerTLSListener(
			t.Context(),
			"lb-id",
			"bs",
			loadbalancer.Listener{},
			listener,
			sslConfig,
		)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("wraps wait errors after create", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		workRequestID := "wr-create-listener"
		wantErr := errors.New("wait failed")
		ociClient.EXPECT().
			CreateListener(t.Context(), mock.Anything).
			Return(loadbalancer.CreateListenerResponse{OpcWorkRequestId: &workRequestID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

		err := model.reconcileLoadBalancerTLSListener(
			t.Context(),
			"lb-id",
			"bs",
			loadbalancer.Listener{},
			listener,
			sslConfig,
		)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for TLSRoute listener")
	})
}

func TestTLSRouteModelCertificateAndStatus(t *testing.T) {
	listener := gatewayv1.Listener{Name: "rtmps", Protocol: gatewayv1.TLSProtocolType}
	details := resolvedTLSRouteDetails{matchedListener: listener}
	model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

	t.Run("uses OCI certificate id", func(t *testing.T) {
		sslConfig, err := model.tlsListenerSSLConfig(details, reconcileListenersCertificatesResult{
			certificateIDsByListener: map[string]string{"rtmps": "ocid1.certificate.oc1..cert"},
		})

		require.NoError(t, err)
		require.NotNil(t, sslConfig)
		assert.Equal(t, []string{"ocid1.certificate.oc1..cert"}, sslConfig.CertificateIds)
	})

	t.Run("rejects missing certificate", func(t *testing.T) {
		_, err := model.tlsListenerSSLConfig(details, reconcileListenersCertificatesResult{})

		require.ErrorContains(t, err, "requires certificateRefs")
	})

	t.Run("merges parent status into existing parent", func(t *testing.T) {
		parentRef := gatewayv1.ParentReference{Name: "edge"}
		parents := mergeTLSRouteParentStatus(
			[]gatewayv1.RouteParentStatus{{
				ParentRef:      parentRef,
				ControllerName: ControllerClassName,
			}},
			parentRef,
			ControllerClassName,
			[]metav1.Condition{{
				Type:   string(gatewayv1.RouteConditionAccepted),
				Status: metav1.ConditionTrue,
				Reason: string(gatewayv1.RouteReasonAccepted),
			}},
		)

		require.Len(t, parents, 1)
		require.Len(t, parents[0].Conditions, 1)
		assert.Equal(t, metav1.ConditionTrue, parents[0].Conditions[0].Status)
	})

	t.Run("appends parent status for new parent", func(t *testing.T) {
		parentRef := gatewayv1.ParentReference{Name: "edge"}
		parents := mergeTLSRouteParentStatus(
			[]gatewayv1.RouteParentStatus{{
				ParentRef:      gatewayv1.ParentReference{Name: "other"},
				ControllerName: ControllerClassName,
			}},
			parentRef,
			ControllerClassName,
			[]metav1.Condition{{
				Type:   string(gatewayv1.RouteConditionAccepted),
				Status: metav1.ConditionTrue,
				Reason: string(gatewayv1.RouteReasonAccepted),
			}},
		)

		require.Len(t, parents, 2)
		assert.Equal(t, parentRef, parents[1].ParentRef)
	})

	t.Run("wraps parent status update errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		statusWriter := k8sapi.NewMockSubResourceWriter(t)
		statusModel := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		wantErr := errors.New("status failed")
		k8sClient.EXPECT().Status().Return(statusWriter)
		statusWriter.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr)

		err := statusModel.updateParentStatus(t.Context(), resolvedTLSRouteDetails{
			tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}},
			matchedRef: gatewayv1.ParentReference{
				Name: "edge",
			},
			gatewayDetails: resolvedGatewayDetails{
				gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
					ControllerName: ControllerClassName,
				}},
			},
		}, []metav1.Condition{{
			Type:   string(gatewayv1.RouteConditionAccepted),
			Status: metav1.ConditionTrue,
			Reason: string(gatewayv1.RouteReasonAccepted),
		}})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update TLSRoute")
	})

	t.Run("returns programmed finalizer update errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		statusModel := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		wantErr := errors.New("update failed")
		k8sClient.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr)

		err := statusModel.setProgrammed(t.Context(), resolvedTLSRouteDetails{
			tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}},
			matchedListener: gatewayv1.Listener{
				Name: "rtmps",
			},
			gatewayDetails: resolvedGatewayDetails{
				gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
					ControllerName: ControllerClassName,
				}},
			},
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update TLSRoute media/rtmps finalizer and annotations")
	})
}

func TestTLSRouteProgrammedResourceAnnotations(t *testing.T) {
	t.Run("parses and clears ALB resource annotations", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedResourcesAnnotation: " , invalid, https/bs_https, /missing-listener, missing-backend/ ",
				},
			},
		}

		resources := annotatedLoadBalancerTLSRouteResources(route)

		require.Equal(t, map[string]string{"https": "bs_https"}, resources)

		setAnnotatedLoadBalancerTLSRouteResources(route, nil)

		assert.NotContains(t, route.Annotations, LoadBalancerTLSRouteProgrammedResourcesAnnotation)
	})

	t.Run("sets ALB resource annotations deterministically", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{}

		setAnnotatedLoadBalancerTLSRouteResources(route, map[string]string{
			"rtmps":  "bs_rtmps",
			"secure": "bs_secure",
		})

		assert.Equal(t,
			"rtmps/bs_rtmps,secure/bs_secure",
			route.Annotations[LoadBalancerTLSRouteProgrammedResourcesAnnotation],
		)
	})

	t.Run("builds legacy ALB resources only from ALB parent statuses with section names", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("rtmps")
		backendSets := map[string]struct{}{"bs": {}}
		route := gatewayv1.TLSRoute{Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
			Parents: []gatewayv1.RouteParentStatus{
				{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: ControllerClassName,
				},
				{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: ControllerClassName,
				},
				{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				},
			},
		}}}

		resources := loadBalancerTLSRouteResourcesFromLegacyStatus(route, backendSets)

		assert.Equal(t, map[string]string{"rtmps": "bs"}, resources)
	})

	t.Run("matches ALB programmed resource parent without section name", func(t *testing.T) {
		assert.True(t, loadBalancerTLSRouteParentHasProgrammedResource(
			gatewayv1.RouteParentStatus{ParentRef: gatewayv1.ParentReference{Name: "edge"}},
			map[string]string{"rtmps": "bs"},
		))
		assert.False(t, loadBalancerTLSRouteParentHasProgrammedResource(
			gatewayv1.RouteParentStatus{ParentRef: gatewayv1.ParentReference{Name: "edge"}},
			nil,
		))
	})

	t.Run("removes stale NLB finalizer when no backend sets are recorded", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		err := model.cleanupStaleNetworkLoadBalancerProgrammedState(t.Context(), *route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
	})

	t.Run("removes stale ALB finalizer when no resources are recorded", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		err := model.cleanupStaleLoadBalancerProgrammedState(t.Context(), *route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
	})

	t.Run("wraps stale programmed state update errors", func(t *testing.T) {
		route := gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}}
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		wantErr := errors.New("update failed")
		k8sClient.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr)

		err := model.removeProgrammedState(t.Context(), route, LoadBalancerTLSRouteProgrammedFinalizer)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update TLSRoute media/rtmps after stale programmed state cleanup")
	})

	t.Run("stale NLB cleanup waits when old parent gateway cannot be resolved", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "missing",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
		})

		err := model.cleanupStaleNetworkLoadBalancerProgrammedState(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("stale NLB cleanup ignores unrelated parent statuses", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

		err := model.cleanupStaleNetworkLoadBalancerProgrammedState(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("stale NLB cleanup returns backend set update errors", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		gatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, gateway, gatewayClass, gatewayConfig).
			Build()
		wantErr := errors.New("update failed")
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_tls": {Name: new("bs_tls")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{updateBackendSetErr: wantErr},
			NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
		})

		err := model.cleanupStaleNetworkLoadBalancerProgrammedState(t.Context(), *route)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("stale ALB cleanup ignores unrelated or unmatched parent statuses", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("other")
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedResourcesAnnotation: "rtmps/bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{
					{
						ParentRef:      gatewayv1.ParentReference{Name: "edge"},
						ControllerName: NetworkLoadBalancerControllerClassName,
					},
					{
						ParentRef: gatewayv1.ParentReference{
							Name:        "edge",
							SectionName: &sectionName,
						},
						ControllerName: ControllerClassName,
					},
				},
			}},
		}
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

		err := model.cleanupStaleLoadBalancerProgrammedState(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("stale ALB cleanup waits when old parent gateway cannot be resolved", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedResourcesAnnotation: "rtmps/bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "missing"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
		})

		err := model.cleanupStaleLoadBalancerProgrammedState(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("stale ALB cleanup returns OCI delete errors", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedResourcesAnnotation: "rtmps/bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		deleteErrorGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		deleteErrorGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		deleteErrorGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(deleteErrorGateway, deleteErrorGatewayClass, deleteErrorGatewayConfig).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			K8sClient:          k8sClient,
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("delete failed")
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{}, wantErr)

		err := model.cleanupStaleLoadBalancerProgrammedState(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("detached ALB cleanup ignores empty resource set", func(t *testing.T) {
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

		err := model.deprovisionDetachedLoadBalancerRoute(t.Context(), gatewayv1.TLSRoute{}, nil)

		require.NoError(t, err)
	})
}

func TestTLSRouteModelDeleteLoadBalancerRouteResources(t *testing.T) {
	ociClient := NewMockociLoadBalancerClient(t)
	watcher := NewMockworkRequestsWatcher(t)
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger:          diag.RootTestLogger(),
		OciLoadBalancerAPI:  ociClient,
		WorkRequestsWatcher: watcher,
	})
	deleteListenerID := "wr-delete-listener"
	deleteBackendSetID := "wr-delete-backend-set"
	details := resolvedTLSRouteDetails{
		tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}},
		matchedListener: gatewayv1.Listener{
			Name: "rtmps",
		},
		gatewayDetails: resolvedGatewayDetails{
			config: types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "lb-id"}},
		},
	}

	ociClient.EXPECT().
		DeleteListener(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteListenerRequest) bool {
			return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
				lo.FromPtr(request.ListenerName) == "rtmps"
		})).
		Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
	watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
	ociClient.EXPECT().
		DeleteBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteBackendSetRequest) bool {
			return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
				lo.FromPtr(request.BackendSetName) == tlsRouteBackendSetName(details.tlsRoute, details.matchedListener)
		})).
		Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
	watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)

	err := model.deleteLoadBalancerRouteResources(t.Context(), details)

	require.NoError(t, err)
}

func TestTLSRouteModelDeleteLoadBalancerRouteResourcesErrors(t *testing.T) {
	details := resolvedTLSRouteDetails{
		tlsRoute:        gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}},
		matchedListener: gatewayv1.Listener{Name: "rtmps"},
		gatewayDetails: resolvedGatewayDetails{
			config: types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "lb-id"}},
		},
	}

	t.Run("wraps listener delete errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("delete listener failed")
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{}, wantErr)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to delete TLSRoute listener")
	})

	t.Run("returns error when listener delete work request is missing", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{}, nil)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("wraps listener delete wait errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "wr-delete-listener"
		wantErr := errors.New("wait failed")
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(wantErr)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for TLSRoute listener")
	})

	t.Run("wraps backend set delete errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "wr-delete-listener"
		wantErr := errors.New("delete backend set failed")
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteBackendSetResponse{}, wantErr)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to delete TLSRoute backend set")
	})

	t.Run("ignores missing listener and backend set", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			OciLoadBalancerAPI: ociClient,
		})
		notFoundErr := ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound))
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{}, notFoundErr)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteBackendSetResponse{}, notFoundErr)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.NoError(t, err)
	})

	t.Run("returns error when backend set delete work request is missing", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "wr-delete-listener"
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteBackendSetResponse{}, nil)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("wraps backend set delete wait errors", func(t *testing.T) {
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "wr-delete-listener"
		deleteBackendSetID := "wr-delete-backend-set"
		wantErr := errors.New("wait failed")
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(wantErr)

		err := model.deleteLoadBalancerRouteResources(t.Context(), details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for TLSRoute backend set")
	})
}

func TestTLSRouteModelResolveRequestRejectedAndFinalizers(t *testing.T) {
	mode := gatewayv1.TLSModeTerminate
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "oke-alb",
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
			},
			Listeners: []gatewayv1.Listener{{
				Name:     "other",
				Protocol: gatewayv1.TLSProtocolType,
				Port:     443,
				TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
			}},
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
	}
	gatewayConfig := &types.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
		Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
	}

	t.Run("ignores missing route", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "media", Name: "missing"},
		})

		require.NoError(t, err)
		assert.Empty(t, resolved)
	})

	t.Run("sets rejected status when listener does not match", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps", Generation: 7},
			Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "edge",
					SectionName: lo.ToPtr(gatewayv1.SectionName("rtmps")),
				}},
			}},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(gateway, gatewayClass, gatewayConfig, route).
			WithStatusSubresource(&gatewayv1.TLSRoute{}).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		err := model.rejectNoMatchingListener(t.Context(), *route, route.Spec.ParentRefs[0])

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(
			t.Context(),
			apitypes.NamespacedName{Namespace: "media", Name: "rtmps"},
			&updated,
		))
		require.Len(t, updated.Status.Parents, 1)
		assert.Equal(t, metav1.ConditionFalse, updated.Status.Parents[0].Conditions[0].Status)
	})

	t.Run("removes finalizers from unresolved route", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Finalizers: []string{
					LoadBalancerTLSRouteProgrammedFinalizer,
					NetworkLoadBalancerTLSRouteProgrammedFinalizer,
				},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation:         "bs",
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs",
				},
			},
		}
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		k8sClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(updated *gatewayv1.TLSRoute) bool {
				return len(updated.Finalizers) == 0 &&
					updated.Annotations[LoadBalancerTLSRouteProgrammedBackendSetAnnotation] == "" &&
					updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation] == ""
			})).
			Return(nil)

		err := model.removeDeletingRouteFinalizers(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("wraps finalizer removal update errors", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		wantErr := errors.New("update failed")
		k8sClient.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr)

		err := model.removeDeletingRouteFinalizers(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to remove finalizers from deleting TLSRoute")
	})

	t.Run("handles unresolved finalized route", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		k8sClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(updated *gatewayv1.TLSRoute) bool {
				return len(updated.Finalizers) == 0
			})).
			Return(nil)

		err := model.handleUnresolvedFinalizedRoute(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("resolve request returns empty result when route is missing", func(t *testing.T) {
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				Build(),
		})

		resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "media", Name: "missing"},
		})

		require.NoError(t, err)
		assert.Empty(t, resolved)
	})

	t.Run("resolve request removes unresolved ALB finalizer", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(route),
		})

		require.NoError(t, err)
		assert.Empty(t, resolved)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
	})

	t.Run("resolve request cleans detached ALB route resources before removing finalizer", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("rtmps")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
				},
			},
			Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "edge",
					SectionName: &sectionName,
				}},
			}},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		albGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		albGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		albGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, albGateway, albGatewayClass, albGatewayConfig).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "delete-listener"
		deleteBackendSetID := "delete-backend-set"
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteListenerRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.ListenerName) == "rtmps"
			})).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteBackendSetRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.BackendSetName) == "bs"
			})).
			Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)

		err := model.deprovisionDetachedRoute(t.Context(), *route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedBackendSetAnnotation])
	})

	t.Run("resolve request cleans detached NLB route backend set before removing finalizer", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "tls",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:        "edge",
					SectionName: &sectionName,
				}},
			}},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		nlbGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		nlbGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		nlbGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, nlbGateway, nlbGatewayClass, nlbGatewayConfig).
			Build()
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_tls": {Name: new("bs_tls")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
		})

		err := model.deprovisionDetachedRoute(t.Context(), *route)

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.Empty(t, nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.Backends)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation])
	})

	t.Run("detached ALB cleanup waits for listener section before removing finalizer", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
		})

		err := model.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("detached ALB cleanup returns OCI delete errors", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("rtmps")
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		deleteErrorGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		deleteErrorGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		deleteErrorGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(deleteErrorGateway, deleteErrorGatewayClass, deleteErrorGatewayConfig).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			K8sClient:          k8sClient,
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("delete failed")
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{}, wantErr)

		err := model.deprovisionDetachedRoute(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("detached ALB cleanup ignores missing OCI resources", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("rtmps")
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		notFoundGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		notFoundGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		notFoundGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(&route, notFoundGateway, notFoundGatewayClass, notFoundGatewayConfig).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			K8sClient:          k8sClient,
			OciLoadBalancerAPI: ociClient,
		})
		notFoundErr := ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound))
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{}, notFoundErr)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteBackendSetResponse{}, notFoundErr)

		err := model.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(&route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedBackendSetAnnotation])
	})

	t.Run("detached ALB cleanup uses programmed resource annotation without section name", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedResourcesAnnotation: "rtmps/bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		annotatedGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		annotatedGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		annotatedGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(&route, annotatedGateway, annotatedGatewayClass, annotatedGatewayConfig).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "delete-listener"
		deleteBackendSetID := "delete-backend-set"
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteListenerRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.ListenerName) == "rtmps"
			})).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteBackendSetRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.BackendSetName) == "bs"
			})).
			Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)

		err := model.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(&route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedResourcesAnnotation])
	})

	t.Run("stale NLB programmed state is cleared before ALB programming", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		staleNLBGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		staleNLBGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		staleNLBGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, staleNLBGateway, staleNLBGatewayClass, staleNLBGatewayConfig).
			Build()
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_tls": {Name: new("bs_tls")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
		})

		err := model.cleanupStaleNetworkLoadBalancerProgrammedState(t.Context(), *route)

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.Empty(t, nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.Backends)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation])
	})

	t.Run("stale ALB programmed state is cleared before NLB programming", func(t *testing.T) {
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
					LoadBalancerTLSRouteProgrammedResourcesAnnotation:  "rtmps/bs",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		staleALBGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		staleALBGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		staleALBGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, staleALBGateway, staleALBGatewayClass, staleALBGatewayConfig).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "delete-listener"
		deleteBackendSetID := "delete-backend-set"
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteListenerRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.ListenerName) == "rtmps"
			})).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteBackendSetRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.BackendSetName) == "bs"
			})).
			Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)

		err := model.cleanupStaleLoadBalancerProgrammedState(t.Context(), *route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedBackendSetAnnotation])
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedResourcesAnnotation])
	})

	t.Run("detached NLB cleanup removes finalizer when load balancer is gone", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "tls",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		missingNLBGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		missingNLBGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		missingNLBGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, missingNLBGateway, missingNLBGatewayClass, missingNLBGatewayConfig).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				returnNil: true,
			},
		})

		err := model.deprovisionDetachedRoute(t.Context(), *route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation])
	})

	t.Run("detached NLB cleanup removes finalizer when backend set is gone", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "tls",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		missingBackendSetGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		missingBackendSetGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		missingBackendSetGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				route,
				missingBackendSetGateway,
				missingBackendSetGatewayClass,
				missingBackendSetGatewayConfig,
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{},
				},
			},
		})

		err := model.deprovisionDetachedRoute(t.Context(), *route)

		require.NoError(t, err)
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation])
	})

	t.Run("detached NLB cleanup returns load balancer lookup errors", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("tls")
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "tls",
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "edge",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		lookupErrorGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		lookupErrorGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		lookupErrorGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		wantErr := errors.New("get nlb failed")
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithRuntimeObjects(lookupErrorGateway, lookupErrorGatewayClass, lookupErrorGatewayConfig).
				Build(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{err: wantErr},
		})

		err := model.deprovisionDetachedRoute(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("detached ALB cleanup ignores unsupported GatewayClass", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("rtmps")
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"},
		}
		unsupportedGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "other",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "config"},
				},
			},
		}
		unsupportedGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "other"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController("example.com/other"),
			},
		}
		unsupportedGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "config"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(unsupportedGateway, unsupportedGatewayClass, unsupportedGatewayConfig).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, resolved, err := model.resolveDetachedLoadBalancerRouteGateway(
			t.Context(),
			route,
			gatewayv1.RouteParentStatus{ParentRef: gatewayv1.ParentReference{
				Name:        "edge",
				SectionName: &sectionName,
			}},
		)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("detached cleanup waits when no matching parent status exists", func(t *testing.T) {
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

		err := model.deprovisionDetachedRoute(t.Context(), gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmps",
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
				},
			},
		})
		require.NoError(t, err)

		err = model.deprovisionDetachedRoute(t.Context(), gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "tls",
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
		})
		require.NoError(t, err)
	})

	t.Run("detached ALB gateway resolver ignores missing Gateway", func(t *testing.T) {
		sectionName := gatewayv1.SectionName("rtmps")
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
		})

		_, resolved, err := model.resolveDetachedLoadBalancerRouteGateway(
			t.Context(),
			gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}},
			gatewayv1.RouteParentStatus{ParentRef: gatewayv1.ParentReference{
				Name:        "missing",
				SectionName: &sectionName,
			}},
		)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("detached finalizer removal returns update errors", func(t *testing.T) {
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		wantErr := errors.New("update failed")
		k8sClient.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr)

		err := model.removeDetachedRouteFinalizers(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update detached TLSRoute")
	})

	t.Run("handles unresolved deleting route", func(t *testing.T) {
		now := metav1.Now()
		route := gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "media",
				Name:              "rtmps",
				DeletionTimestamp: &now,
				Finalizers:        []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
		}
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		k8sClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(updated *gatewayv1.TLSRoute) bool {
				return len(updated.Finalizers) == 0
			})).
			Return(nil)

		err := model.handleUnresolvedFinalizedRoute(t.Context(), route)

		require.NoError(t, err)
	})
}

func TestTLSRouteModelDeprovisionLoadBalancerRoute(t *testing.T) {
	mode := gatewayv1.TLSModeTerminate
	route := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "media",
			Name:       "rtmps",
			Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
		},
		Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
		}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(route).
		Build()
	ociClient := NewMockociLoadBalancerClient(t)
	watcher := NewMockworkRequestsWatcher(t)
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger:          diag.RootTestLogger(),
		K8sClient:           k8sClient,
		OciLoadBalancerAPI:  ociClient,
		WorkRequestsWatcher: watcher,
	})
	deleteListenerID := "wr-delete-listener"
	deleteBackendSetID := "wr-delete-backend-set"
	details := resolvedTLSRouteDetails{
		tlsRoute: *route,
		matchedListener: gatewayv1.Listener{
			Name:     "rtmps",
			Protocol: gatewayv1.TLSProtocolType,
			TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
		},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			}},
			config: types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "lb-id"}},
		},
	}
	ociClient.EXPECT().
		DeleteListener(t.Context(), mock.Anything).
		Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
	watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
	ociClient.EXPECT().
		DeleteBackendSet(t.Context(), mock.Anything).
		Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
	watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)

	err := model.deprovisionRoute(t.Context(), details)

	require.NoError(t, err)
	var updated gatewayv1.TLSRoute
	require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{Namespace: "media", Name: "rtmps"}, &updated))
	assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
}

func TestTLSRouteModelDeprovisionLoadBalancerRouteErrors(t *testing.T) {
	details := resolvedTLSRouteDetails{
		tlsRoute: gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
			},
			Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			}},
		},
		matchedListener: gatewayv1.Listener{Name: "rtmps"},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			}},
			config: types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "lb-id"}},
		},
	}

	t.Run("returns next route lookup errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
		})
		wantErr := errors.New("list failed")
		k8sClient.EXPECT().List(t.Context(), &gatewayv1.TLSRouteList{}).Return(wantErr)

		handoffDetails := details
		handoffDetails.matchedListener = gatewayv1.Listener{
			Name:     "rtmps",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
			},
		}

		err := model.deprovisionLoadBalancerRoute(t.Context(), handoffDetails)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("wraps finalizer update errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
		})
		deleteListenerID := "wr-delete-listener"
		deleteBackendSetID := "wr-delete-backend-set"
		k8sClient.EXPECT().
			List(t.Context(), &gatewayv1.TLSRouteList{}).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.TLSRoute{}))
				return nil
			})
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)
		wantErr := errors.New("update failed")
		k8sClient.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr)

		handoffDetails := details
		handoffDetails.matchedListener = gatewayv1.Listener{
			Name:     "rtmps",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
			},
		}

		err := model.deprovisionLoadBalancerRoute(t.Context(), handoffDetails)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to remove ALB TLSRoute finalizer")
	})

	t.Run("wraps next route programming errors", func(t *testing.T) {
		backendPort := gatewayv1.PortNumber(443)
		nextRoute := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "media",
				Name:              "next",
				CreationTimestamp: metav1.Unix(10, 0),
			},
			Spec: gatewayv1.TLSRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Hostnames: []gatewayv1.Hostname{"next.example.com"},
				Rules: []gatewayv1.TLSRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend",
						Port: &backendPort,
					}}},
				}},
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(nextRoute).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:         diag.RootTestLogger(),
			K8sClient:          k8sClient,
			OciLoadBalancerAPI: ociClient,
		})
		wantErr := errors.New("get load balancer failed")
		ociClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{}, wantErr)

		handoffDetails := details
		handoffDetails.matchedListener = gatewayv1.Listener{
			Name:     "rtmps",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
			},
		}

		err := model.deprovisionLoadBalancerRoute(t.Context(), handoffDetails)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to program next TLSRoute media/next")
	})
}

func TestTLSRouteModelClearNLBBackendSet(t *testing.T) {
	nlbClient := &stubNetworkLoadBalancerClient{}
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
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
	details := resolvedTLSRouteDetails{
		tlsRoute: gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls"}},
		matchedListener: gatewayv1.Listener{
			Name: "tls",
			Port: 443,
		},
	}

	err := model.clearNLBBackendSet(t.Context(), details)

	require.NoError(t, err)
	require.Len(t, nlbClient.updateBackendSetRequests, 1)
	update := nlbClient.updateBackendSetRequests[0]
	assert.Equal(t, "bs_tls", lo.FromPtr(update.BackendSetName))
	assert.Empty(t, update.UpdateBackendSetDetails.Backends)
	assert.Equal(t, 443, lo.FromPtr(update.UpdateBackendSetDetails.HealthChecker.Port))
}

func TestTLSRouteModelUpdateNLBBackendSet(t *testing.T) {
	backendPort := gatewayv1.PortNumber(443)
	route := gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls"},
		Spec: gatewayv1.TLSRouteSpec{Rules: []gatewayv1.TLSRouteRule{{
			BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "backend",
				Port: &backendPort,
			}}},
		}}},
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
			Port: 443,
		}}},
	}
	backends := []networkloadbalancer.BackendDetails{{
		IpAddress: new("10.0.0.10"),
		Port:      new(443),
		Weight:    new(1),
		IsDrain:   new(false),
	}}

	t.Run("skips matching backend set", func(t *testing.T) {
		healthChecker := networkLoadBalancerHealthCheckerDetails(gatewayv1.TCPProtocolType, new(443))
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(service).
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
							Name:             new("bs_tls"),
							IsPreserveSource: new(false),
							HealthChecker:    nlbHealthCheckerFromDetails(healthChecker),
							Backends: []networkloadbalancer.Backend{{
								IpAddress: new("10.0.0.10"),
								Port:      new(443),
								Weight:    new(1),
								IsDrain:   new(false),
							}},
						},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		err := model.updateNLBBackendSet(t.Context(), resolvedTLSRouteDetails{tlsRoute: route}, "bs_tls", backends)

		require.NoError(t, err)
		assert.Empty(t, nlbClient.updateBackendSetRequests)
	})

	t.Run("returns network load balancer errors", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(service).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				err: errors.New("nlb failed"),
			},
		})

		err := model.updateNLBBackendSet(t.Context(), resolvedTLSRouteDetails{tlsRoute: route}, "bs_tls", backends)

		require.ErrorContains(t, err, "nlb failed")
	})
}

func TestTLSRouteModelProgramNetworkLoadBalancerPassthroughRouteErrors(t *testing.T) {
	backendPort := gatewayv1.PortNumber(443)
	route := gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "iot",
			Name:       "tls",
			Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
		},
		Spec: gatewayv1.TLSRouteSpec{Rules: []gatewayv1.TLSRouteRule{{
			BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "missing",
				Port: &backendPort,
			}}},
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "iot"}}).
		Build()
	nlbClient := &stubNetworkLoadBalancerClient{}
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
			networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_tls": {Name: new("bs_tls")},
				},
			},
		},
		OciNetworkLoadBalancerAPI: nlbClient,
		NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
	})

	err := model.programNetworkLoadBalancerPassthroughRoute(t.Context(), resolvedTLSRouteDetails{
		tlsRoute: route,
		matchedListener: gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
		},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
		},
	})

	require.ErrorContains(t, err, "backendRef service iot/missing not found")
	require.Len(t, nlbClient.updateBackendSetRequests, 1)
	update := nlbClient.updateBackendSetRequests[0]
	assert.Equal(t, "bs_tls", lo.FromPtr(update.BackendSetName))
	assert.Empty(t, update.UpdateBackendSetDetails.Backends)
}

func TestTLSRouteModelBackendResolutionErrors(t *testing.T) {
	backendPort := gatewayv1.PortNumber(443)
	route := gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "tls"},
		Spec: gatewayv1.TLSRouteSpec{Rules: []gatewayv1.TLSRouteRule{{
			BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "backend",
				Port: &backendPort,
			}}},
		}}},
	}

	t.Run("returns status error when service is missing", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, err := model.endpointBackendsForRoute(t.Context(), route)

		require.ErrorContains(t, err, "backendRef service iot/backend not found")
	})

	t.Run("resolves same namespace backend without reference grant", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend"},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Port: 443,
					}}},
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
					Endpoints: []discoveryv1.Endpoint{{
						Addresses:  []string{"10.0.0.8"},
						Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
					}},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		backends, err := model.endpointBackendsForRoute(t.Context(), route)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "10.0.0.8", lo.FromPtr(backends[0].IpAddress))
		assert.Equal(t, 443, lo.FromPtr(backends[0].Port))
	})

	t.Run("rejects cross namespace backend without reference grant", func(t *testing.T) {
		backendNamespace := gatewayv1.Namespace("media")
		route := route.DeepCopy()
		route.Spec.Rules[0].BackendRefs[0].Namespace = &backendNamespace
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, err := model.endpointBackendsForRoute(t.Context(), *route)

		var statusErr tlsRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteConditionResolvedRefs, statusErr.conditionType)
		assert.Equal(t, gatewayv1.RouteReasonRefNotPermitted, statusErr.reason)
		require.ErrorContains(t, err, "backendRef media/backend is not permitted by a ReferenceGrant")
	})

	t.Run("resolves cross namespace backend with reference grant", func(t *testing.T) {
		backendNamespace := gatewayv1.Namespace("media")
		route := route.DeepCopy()
		route.Spec.Rules[0].BackendRefs[0].Namespace = &backendNamespace
		serviceName := gatewayv1.ObjectName("backend")
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&gatewayv1beta1.ReferenceGrant{
					ObjectMeta: metav1.ObjectMeta{Namespace: string(backendNamespace), Name: "allow-iot-tls"},
					Spec: gatewayv1beta1.ReferenceGrantSpec{
						From: []gatewayv1beta1.ReferenceGrantFrom{{
							Group:     gatewayv1.Group(gatewayAPIGroup),
							Kind:      gatewayv1.Kind("TLSRoute"),
							Namespace: gatewayv1.Namespace("iot"),
						}},
						To: []gatewayv1beta1.ReferenceGrantTo{{
							Group: "",
							Kind:  gatewayv1.Kind(serviceKind),
							Name:  &serviceName,
						}},
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: string(backendNamespace), Name: string(serviceName)},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Port: 443,
					}}},
				},
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: string(backendNamespace),
						Name:      "backend-a",
						Labels: map[string]string{
							discoveryv1.LabelServiceName: string(serviceName),
						},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints: []discoveryv1.Endpoint{{
						Addresses:  []string{"10.0.1.9"},
						Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
					}},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		backends, err := model.endpointBackendsForRoute(t.Context(), *route)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "10.0.1.9", lo.FromPtr(backends[0].IpAddress))
		assert.Equal(t, 443, lo.FromPtr(backends[0].Port))
	})

	t.Run("returns list errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "backend"}, &corev1.Service{}).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*corev1.Service) = corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "backend"},
					Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
						Port: 443,
					}}},
				}
				return nil
			})
		wantErr := errors.New("list failed")
		k8sClient.EXPECT().
			List(t.Context(), &discoveryv1.EndpointSliceList{},
				client.MatchingLabels{discoveryv1.LabelServiceName: "backend"},
				client.InNamespace("iot")).
			Return(wantErr)

		_, err := model.endpointBackendsForRoute(t.Context(), route)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to list endpoint slices")
	})

	t.Run("ignores zero weight backend refs", func(t *testing.T) {
		weight := int32(0)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger()})

		backends, err := model.endpointBackendsForBackendRef(t.Context(), route, gatewayv1.BackendRef{
			Weight: &weight,
		})

		require.NoError(t, err)
		assert.Empty(t, backends)
	})
}

func TestTLSRouteModelDeprovisionNetworkLoadBalancerRoute(t *testing.T) {
	k8sClient := NewMockk8sClient(t)
	nlbClient := &stubNetworkLoadBalancerClient{}
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
			networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_tls": {Name: new("bs_tls")},
				},
			},
		},
		OciNetworkLoadBalancerAPI: nlbClient,
		NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
	})
	details := resolvedTLSRouteDetails{
		tlsRoute: gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "tls",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			}},
		},
		matchedListener: gatewayv1.Listener{
			Name: "tls",
			Port: 443,
		},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			}},
		},
	}
	k8sClient.EXPECT().
		List(t.Context(), &gatewayv1.TLSRouteList{}).
		RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
			reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.TLSRoute{}))
			return nil
		})
	k8sClient.EXPECT().
		Update(t.Context(), mock.MatchedBy(func(updated *gatewayv1.TLSRoute) bool {
			return len(updated.Finalizers) == 0 &&
				updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation] == ""
		})).
		Return(nil)

	err := model.deprovisionRoute(t.Context(), details)

	require.NoError(t, err)
	require.Len(t, nlbClient.updateBackendSetRequests, 1)
	assert.Empty(t, nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.Backends)
}

func TestTLSRouteModelDeprovisionNetworkLoadBalancerRouteNextRouteError(t *testing.T) {
	backendPort := gatewayv1.PortNumber(443)
	nextRoute := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "iot",
			Name:              "next",
			CreationTimestamp: metav1.Unix(10, 0),
		},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			},
			Hostnames: []gatewayv1.Hostname{"next.example.com"},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "missing",
					Port: &backendPort,
				}}},
			}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(nextRoute).
		Build()
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient:  k8sClient,
		NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
			networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
				Id: new("nlb-id"),
				BackendSets: map[string]networkloadbalancer.BackendSet{
					"bs_tls": {Name: new("bs_tls")},
				},
			},
		},
		OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
	})

	err := model.deprovisionNetworkLoadBalancerRoute(t.Context(), resolvedTLSRouteDetails{
		tlsRoute: gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "tls",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
			},
		},
		matchedListener: gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: lo.ToPtr(gatewayv1.TLSModePassthrough),
			},
		},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			}},
		},
	})

	require.ErrorContains(t, err, "failed to program next TLSRoute iot/next")
	require.ErrorContains(t, err, "backendRef service iot/missing not found")
}

func TestTLSRouteModelResolveParentGatewayFailures(t *testing.T) {
	parentRef := gatewayv1.ParentReference{Name: "edge"}

	t.Run("wraps gateway get errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		wantErr := errors.New("gateway get failed")
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "media", Name: "edge"}, &gatewayv1.Gateway{}).
			Return(wantErr)

		_, _, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get Gateway")
	})

	t.Run("ignores missing gateway", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, resolved, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("wraps gateway class get errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "media", Name: "edge"}, &gatewayv1.Gateway{}).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.Gateway) = gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: "oke-alb",
					},
				}
				return nil
			})
		wantErr := errors.New("class get failed")
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: "oke-alb"}, &gatewayv1.GatewayClass{}).
			Return(wantErr)

		_, _, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get GatewayClass")
	})

	t.Run("ignores missing gateway class", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(&gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: "missing",
				},
			}).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, resolved, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("ignores unsupported controller", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: "unsupported",
					},
				},
				&gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "unsupported"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: "example.com/controller",
					},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, resolved, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("ignores gateway without GatewayConfig reference", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: "oke-alb",
					},
				},
				&gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: ControllerClassName,
					},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, resolved, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("wraps GatewayConfig get errors", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})
		gatewayName := apitypes.NamespacedName{Namespace: "media", Name: "edge"}
		k8sClient.EXPECT().
			Get(t.Context(), gatewayName, &gatewayv1.Gateway{}).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.Gateway) = gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: "oke-alb",
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{Name: "missing"},
						},
					},
				}
				return nil
			})
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: "oke-alb"}, &gatewayv1.GatewayClass{}).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.GatewayClass) = gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: ControllerClassName,
					},
				}
				return nil
			})
		wantErr := errors.New("api failed")
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "media", Name: "missing"}, &types.GatewayConfig{}).
			Return(wantErr)

		_, _, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get GatewayConfig")
	})

	t.Run("ignores missing GatewayConfig", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: "oke-alb",
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{Name: "missing"},
						},
					},
				},
				&gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: ControllerClassName,
					},
				},
			).
			Build()
		model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

		_, resolved, err := model.resolveParentGateway(t.Context(), "media", parentRef)

		require.NoError(t, err)
		assert.False(t, resolved)
	})
}

func TestTLSRouteModelResolveParentRefMatchesTLSListeners(t *testing.T) {
	mode := gatewayv1.TLSModeTerminate
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "oke-alb",
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
			},
			Listeners: []gatewayv1.Listener{
				{
					Name:     "rtmps",
					Protocol: gatewayv1.TLSProtocolType,
					Port:     443,
					TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
				},
				{
					Name:     "mqtts",
					Protocol: gatewayv1.TLSProtocolType,
					Port:     8883,
					TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
				},
				{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     8443,
					TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
				},
			},
		},
	}
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: ControllerClassName,
		},
	}
	gatewayConfig := &types.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
	}
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient: fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(gateway, gatewayClass, gatewayConfig).
			Build(),
	})
	route := gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"}}

	resolved, matchedParent, err := model.resolveParentRef(t.Context(), route, gatewayv1.ParentReference{Name: "edge"})

	require.NoError(t, err)
	assert.True(t, matchedParent)
	require.Len(t, resolved, 2)
	assert.ElementsMatch(t, []gatewayv1.SectionName{"rtmps", "mqtts"}, []gatewayv1.SectionName{
		resolved[0].matchedListener.Name,
		resolved[1].matchedListener.Name,
	})

	sectionName := gatewayv1.SectionName("mqtts")
	resolved, matchedParent, err = model.resolveParentRef(
		t.Context(),
		route,
		gatewayv1.ParentReference{Name: "edge", SectionName: &sectionName},
	)

	require.NoError(t, err)
	assert.True(t, matchedParent)
	require.Len(t, resolved, 1)
	assert.Equal(t, gatewayv1.SectionName("mqtts"), resolved[0].matchedListener.Name)
}

func TestTLSRouteModelProgramRouteOwnershipConflict(t *testing.T) {
	mode := gatewayv1.TLSModeTerminate
	backendPort := gatewayv1.PortNumber(443)
	currentRoute := &gatewayv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "zzz-current", CreationTimestamp: metav1.Unix(20, 0)},
		Spec: gatewayv1.TLSRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			},
			Hostnames: []gatewayv1.Hostname{"rtmps.example.com"},
			Rules: []gatewayv1.TLSRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "backend",
					Port: &backendPort,
				}}},
			}},
		},
	}
	olderRoute := currentRoute.DeepCopy()
	olderRoute.Name = "older"
	olderRoute.CreationTimestamp = metav1.Unix(10, 0)
	k8sClient := fake.NewClientBuilder().
		WithScheme(newL4TestScheme(t)).
		WithRuntimeObjects(currentRoute, olderRoute).
		Build()
	model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: k8sClient})

	err := model.programRoute(t.Context(), resolvedTLSRouteDetails{
		tlsRoute: *currentRoute,
		matchedListener: gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
		},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			}},
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "already has an attached TLSRoute")
}

func TestTLSRouteModelProgramRouteControllerTransitions(t *testing.T) {
	t.Run("clears stale NLB state before ALB programming", func(t *testing.T) {
		listener := gatewayv1.Listener{
			Name:     "rtmps",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
				CertificateRefs: []gatewayv1.SecretObjectReference{{
					Name: "rtmps-cert",
				}},
			},
		}
		backendPort := gatewayv1.PortNumber(1935)
		sectionName := gatewayv1.SectionName("tls")
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{NetworkLoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation: "bs_tls",
				},
			},
			Spec: gatewayv1.TLSRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{
						Name:        "edge",
						SectionName: lo.ToPtr(gatewayv1.SectionName("rtmps")),
					}},
				},
				Hostnames: []gatewayv1.Hostname{"rtmps.example.com"},
				Rules: []gatewayv1.TLSRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "rtmp",
							Port: &backendPort,
						},
					}},
				}},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef: gatewayv1.ParentReference{
						Name:        "old-nlb",
						SectionName: &sectionName,
					},
					ControllerName: NetworkLoadBalancerControllerClassName,
				}},
			}},
		}
		oldNLBGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "old-nlb"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		oldNLBGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		oldNLBGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		objects := append(albTLSRouteObjects(listener), route, oldNLBGateway, oldNLBGatewayClass, oldNLBGatewayConfig)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		ociModel := NewMockociLoadBalancerModel(t)
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:           diag.RootTestLogger(),
			K8sClient:            k8sClient,
			OciLoadBalancerAPI:   ociClient,
			OciLoadBalancerModel: ociModel,
			WorkRequestsWatcher:  &stubWorkRequestsWatcher{},
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_tls": {Name: new("bs_tls")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
		})
		loadBalancerID := "ocid1.loadbalancer.oc1..existing"
		workRequestID := "wr-tlsroute"
		certName := "media-rtmps-cert-rev-1"
		ociClient.EXPECT().
			GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
				LoadBalancerId: &loadBalancerID,
			}).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{
					BackendSets:  map[string]loadbalancer.BackendSet{},
					Listeners:    map[string]loadbalancer.Listener{},
					Certificates: map[string]loadbalancer.Certificate{},
				},
			}, nil)
		ociClient.EXPECT().
			CreateBackendSet(t.Context(), mock.Anything).
			Return(loadbalancer.CreateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
		ociModel.EXPECT().
			reconcileListenersCertificates(t.Context(), mock.Anything).
			Return(reconcileListenersCertificatesResult{
				certificatesByListener: map[string][]loadbalancer.Certificate{
					"rtmps": {{CertificateName: &certName}},
				},
			}, nil)
		ociClient.EXPECT().
			CreateListener(t.Context(), mock.MatchedBy(func(request loadbalancer.CreateListenerRequest) bool {
				return lo.FromPtr(request.CreateListenerDetails.Name) == "rtmps" &&
					lo.FromPtr(request.CreateListenerDetails.Port) == 443
			})).
			Return(loadbalancer.CreateListenerResponse{OpcWorkRequestId: &workRequestID}, nil)

		err := model.programRoute(t.Context(), resolvedTLSRouteDetails{
			tlsRoute:        *route,
			matchedListener: listener,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
				gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
					ControllerName: ControllerClassName,
				}},
				config: types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID}},
			},
		})

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.Equal(t, "bs_tls", lo.FromPtr(nlbClient.updateBackendSetRequests[0].BackendSetName))
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation])
	})

	t.Run("clears stale ALB state before NLB programming", func(t *testing.T) {
		backendPort := gatewayv1.PortNumber(1935)
		route := &gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "media",
				Name:       "rtmps",
				Finalizers: []string{LoadBalancerTLSRouteProgrammedFinalizer},
				Annotations: map[string]string{
					LoadBalancerTLSRouteProgrammedBackendSetAnnotation: "bs",
					LoadBalancerTLSRouteProgrammedResourcesAnnotation:  "rtmps/bs",
				},
			},
			Spec: gatewayv1.TLSRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{
						Name:        "edge",
						SectionName: lo.ToPtr(gatewayv1.SectionName("tls")),
					}},
				},
				Hostnames: []gatewayv1.Hostname{"rtmps.example.com"},
				Rules: []gatewayv1.TLSRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "rtmp",
							Port: &backendPort,
						},
					}},
				}},
			},
			Status: gatewayv1.TLSRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "old-alb"},
					ControllerName: ControllerClassName,
				}},
			}},
		}
		oldALBGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "old-alb"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-alb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
				},
			},
		}
		oldALBGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-alb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
		oldALBGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "alb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-id"},
		}
		currentNLBGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oke-nlb",
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				},
			},
		}
		currentNLBGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			},
		}
		currentNLBGatewayConfig := &types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nlb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "nlb-id"},
		}
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmp"},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{
					Name: "rtmp",
					Port: 1935,
				}},
			},
		}
		endpointSlice := &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "media",
				Name:      "rtmp-a",
				Labels: map[string]string{
					discoveryv1.LabelServiceName: "rtmp",
				},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{{
				Addresses:  []string{"10.0.1.10"},
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
			}},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(route, oldALBGateway, oldALBGatewayClass, oldALBGatewayConfig,
				currentNLBGateway, currentNLBGatewayClass, currentNLBGatewayConfig, service, endpointSlice).
			Build()
		ociClient := NewMockociLoadBalancerClient(t)
		watcher := NewMockworkRequestsWatcher(t)
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTLSRouteModel(tlsRouteModelDeps{
			RootLogger:          diag.RootTestLogger(),
			K8sClient:           k8sClient,
			OciLoadBalancerAPI:  ociClient,
			WorkRequestsWatcher: watcher,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			NLBWorkRequestsWatcher:    &stubWorkRequestsWatcher{},
		})
		deleteListenerID := "delete-listener"
		deleteBackendSetID := "delete-backend-set"
		ociClient.EXPECT().
			DeleteListener(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteListenerRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.ListenerName) == "rtmps"
			})).
			Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteListenerID).Return(nil)
		ociClient.EXPECT().
			DeleteBackendSet(t.Context(), mock.MatchedBy(func(request loadbalancer.DeleteBackendSetRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "lb-id" &&
					lo.FromPtr(request.BackendSetName) == "bs"
			})).
			Return(loadbalancer.DeleteBackendSetResponse{OpcWorkRequestId: &deleteBackendSetID}, nil)
		watcher.EXPECT().WaitFor(t.Context(), deleteBackendSetID).Return(nil)
		listener := gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: lo.ToPtr(gatewayv1.TLSModePassthrough),
			},
		}

		err := model.programRoute(t.Context(), resolvedTLSRouteDetails{
			tlsRoute:        *route,
			matchedListener: listener,
			gatewayDetails: resolvedGatewayDetails{
				gateway:      *currentNLBGateway,
				gatewayClass: *currentNLBGatewayClass,
				config:       *currentNLBGatewayConfig,
			},
		})

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.Equal(t, networkLoadBalancerBackendSetName(listener),
			lo.FromPtr(nlbClient.updateBackendSetRequests[0].BackendSetName))
		var updated gatewayv1.TLSRoute
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(route), &updated))
		assert.NotContains(t, updated.Finalizers, LoadBalancerTLSRouteProgrammedFinalizer)
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedBackendSetAnnotation])
		assert.Empty(t, updated.Annotations[LoadBalancerTLSRouteProgrammedResourcesAnnotation])
	})
}

func TestTLSRouteModelProgramRouteRejectedByListenerPolicy(t *testing.T) {
	mode := gatewayv1.TLSModePassthrough
	backendPort := gatewayv1.PortNumber(443)
	model := newTLSRouteModel(tlsRouteModelDeps{
		RootLogger: diag.RootTestLogger(),
		K8sClient: fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			Build(),
	})

	err := model.programRoute(t.Context(), resolvedTLSRouteDetails{
		tlsRoute: gatewayv1.TLSRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rtmps"},
			Spec: gatewayv1.TLSRouteSpec{
				Hostnames: []gatewayv1.Hostname{"rtmps.example.com"},
				Rules: []gatewayv1.TLSRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "backend",
						Port: &backendPort,
					}}},
				}},
			},
		},
		matchedListener: gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
			TLS:      &gatewayv1.ListenerTLSConfig{Mode: &mode},
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Namespaces: &gatewayv1.RouteNamespaces{
					From: lo.ToPtr(gatewayv1.NamespacesFromNone),
				},
			},
		},
		gatewayDetails: resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "gateway", Name: "edge"}},
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
				ControllerName: NetworkLoadBalancerControllerClassName,
			}},
		},
	})

	require.ErrorContains(t, err, "does not allow TLSRoute media/rtmps")
}
