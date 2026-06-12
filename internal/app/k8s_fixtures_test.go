package app

import (
	"context"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type randomGatewayClassOpt func(*gatewayv1.GatewayClass)

func newRandomGatewayClass(
	opts ...randomGatewayClassOpt,
) *gatewayv1.GatewayClass {
	fake := faker.New()
	gc := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:            fake.Internet().Domain(),
			Generation:      rand.Int64(),
			UID:             apitypes.UID(fake.UUID().V4()), // Add UID for potential future use
			ResourceVersion: fake.Lorem().Word(),            // Add RV for potential future use
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(
				fake.UUID().V4() + "." + fake.Internet().Domain(),
			),
		},
	}

	for _, opt := range opts {
		opt(gc)
	}

	return gc
}

func randomGatewayClassWithControllerNameOpt(controllerName gatewayv1.GatewayController) randomGatewayClassOpt {
	return func(gc *gatewayv1.GatewayClass) {
		gc.Spec.ControllerName = controllerName
	}
}

type randomGatewayOpt func(*gatewayv1.Gateway)

func newRandomGateway(
	opts ...randomGatewayOpt,
) *gatewayv1.Gateway {
	fake := faker.New()
	gw := gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fake.Internet().Domain(),
			Namespace:  fake.Internet().Slug(), // Gateways are namespaced
			Generation: rand.Int64(),
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(fake.Internet().Domain()),
			Listeners: []gatewayv1.Listener{
				{
					Name:     "http",
					Port:     80,
					Protocol: gatewayv1.HTTPProtocolType,
				},
			},
		},
		Status: gatewayv1.GatewayStatus{ // Initialize status
			Conditions: []metav1.Condition{},
		},
	}

	for _, opt := range opts {
		opt(&gw)
	}

	return &gw
}

func randomGatewayWithRandomListenersOpt() randomGatewayOpt {
	return func(gw *gatewayv1.Gateway) {
		gw.Spec.Listeners = make([]gatewayv1.Listener, 2+rand.IntN(3))
		for i := range gw.Spec.Listeners {
			gw.Spec.Listeners[i] = makeRandomListener()
		}
	}
}

func randomGatewayWithListenersOpt(
	listeners ...gatewayv1.Listener,
) randomGatewayOpt {
	return func(gw *gatewayv1.Gateway) {
		gw.Spec.Listeners = listeners
	}
}

func randomGatewayWithNameFromParentRefOpt(ref gatewayv1.ParentReference) randomGatewayOpt {
	return func(gw *gatewayv1.Gateway) {
		gw.Name = string(ref.Name)
		if ref.Namespace != nil {
			gw.Namespace = string(lo.FromPtr(ref.Namespace))
		}
	}
}

func randomSecretObjectReference() gatewayv1.SecretObjectReference {
	fake := faker.New()
	return gatewayv1.SecretObjectReference{
		Name:      gatewayv1.ObjectName("secret-" + fake.Internet().Domain()),
		Namespace: new(gatewayv1.Namespace("ns-" + fake.Internet().Domain())),
	}
}

type randomListenerOpt func(*gatewayv1.Listener)

func makeRandomListener(
	opts ...randomListenerOpt,
) gatewayv1.Listener {
	fake := faker.New()
	listener := gatewayv1.Listener{
		Name:     gatewayv1.SectionName("listener-" + fake.UUID().V4()),
		Port:     rand.Int32N(4000),
		Protocol: gatewayv1.ProtocolType(fake.Lorem().Word()),
	}

	for _, opt := range opts {
		opt(&listener)
	}

	return listener
}

func randomListenerWithHTTPSParamsOpt() randomListenerOpt {
	return func(listener *gatewayv1.Listener) {
		listener.Protocol = gatewayv1.HTTPSProtocolType
		listener.TLS = &gatewayv1.ListenerTLSConfig{
			CertificateRefs: []gatewayv1.SecretObjectReference{
				randomSecretObjectReference(),
				randomSecretObjectReference(),
			},
		}
	}
}
func randomListenerWithNameOpt(name gatewayv1.SectionName) randomListenerOpt {
	return func(listener *gatewayv1.Listener) {
		listener.Name = name
	}
}

func randomListenerWithHTTPProtocolOpt() randomListenerOpt {
	return func(listener *gatewayv1.Listener) {
		listener.Protocol = gatewayv1.HTTPProtocolType
	}
}

func makeFewRandomListeners() []gatewayv1.Listener {
	count := 2 + rand.IntN(3)
	listeners := make([]gatewayv1.Listener, count)
	for i := range listeners {
		listeners[i] = makeRandomListener()
	}
	return listeners
}

type randomHTTPRouteOpt func(*gatewayv1.HTTPRoute)

func makeRandomHTTPRoute(
	opts ...randomHTTPRouteOpt,
) gatewayv1.HTTPRoute {
	fake := faker.New()
	route := gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fake.Internet().Domain(),
			Namespace:  fake.Internet().Slug(),
			Generation: rand.Int64(),
		},
		Spec: gatewayv1.HTTPRouteSpec{},
	}

	for _, opt := range opts {
		opt(&route)
	}

	return route
}

func randomHTTPRouteWithNameOpt(name string) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Name = name
	}
}

func randomHTTPRouteWithNamespaceOpt(namespace string) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Namespace = namespace
	}
}

func randomHTTPRouteWithRandomParentRefOpt(ref gatewayv1.ParentReference) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Spec.ParentRefs = append(route.Spec.ParentRefs, ref)
	}
}

func randomHTTPRouteWithRandomParentRefsOpt(refs ...gatewayv1.ParentReference) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Spec.ParentRefs = append(route.Spec.ParentRefs, refs...)
	}
}

func randomHTTPRouteWithRulesOpt(rules ...gatewayv1.HTTPRouteRule) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Spec.Rules = append(route.Spec.Rules, rules...)
	}
}

type randomHTTPRouteRuleOpt func(*gatewayv1.HTTPRouteRule)

func makeRandomHTTPRouteRule(
	opts ...randomHTTPRouteRuleOpt,
) gatewayv1.HTTPRouteRule {
	rule := gatewayv1.HTTPRouteRule{
		BackendRefs: []gatewayv1.HTTPBackendRef{},
	}

	for _, opt := range opts {
		opt(&rule)
	}

	return rule
}

func randomHTTPRouteRuleWithRandomBackendRefsOpt(
	refs ...gatewayv1.HTTPBackendRef,
) randomHTTPRouteRuleOpt {
	return func(rule *gatewayv1.HTTPRouteRule) {
		rule.BackendRefs = append(rule.BackendRefs, refs...)
	}
}

type randomBackendRefOpt func(*gatewayv1.HTTPBackendRef)

func makeRandomBackendRef(
	opts ...randomBackendRefOpt,
) gatewayv1.HTTPBackendRef {
	fake := faker.New()
	ref := gatewayv1.HTTPBackendRef{
		BackendRef: gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name:      gatewayv1.ObjectName(fake.Internet().Domain()),
				Namespace: new(gatewayv1.Namespace(fake.Internet().Domain())),
				Port:      new(rand.Int32N(65535)),
			},
		},
	}

	for _, opt := range opts {
		opt(&ref)
	}

	return ref
}

func randomBackendRefWithNillNamespaceOpt() randomBackendRefOpt {
	return func(ref *gatewayv1.HTTPBackendRef) {
		ref.BackendObjectReference.Namespace = nil
	}
}

func randomBackendRefWithNameOpt(name string) randomBackendRefOpt {
	return func(ref *gatewayv1.HTTPBackendRef) {
		ref.BackendObjectReference.Name = gatewayv1.ObjectName(name)
	}
}

func randomBackendRefWithNamespaceOpt(namespace string) randomBackendRefOpt {
	return func(ref *gatewayv1.HTTPBackendRef) {
		ref.BackendObjectReference.Namespace = new(gatewayv1.Namespace(namespace))
	}
}

type randomServiceOpt func(*corev1.Service)

func makeRandomService(
	opts ...randomServiceOpt,
) corev1.Service {
	fake := faker.New()
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fake.Internet().Domain(),
			Namespace: fake.Internet().Slug(),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": fake.Internet().Domain(),
			},
			ClusterIP: fake.Internet().Ipv4(),
			Ports: []corev1.ServicePort{
				{
					Port:       rand.Int32N(65535),
					TargetPort: intstr.FromInt(rand.IntN(65535)),
				},
			},
		},
	}

	for _, opt := range opts {
		opt(&svc)
	}

	return svc
}

func makeFewRandomServices() []corev1.Service {
	count := 2 + rand.IntN(3)
	services := make([]corev1.Service, count)
	for i := range services {
		services[i] = makeRandomService()
	}
	return services
}

func randomServiceFromBackendRef(ref gatewayv1.HTTPBackendRef, parent client.Object) randomServiceOpt {
	return func(svc *corev1.Service) {
		fullName := backendRefName(ref, parent.GetNamespace())
		svc.Name = fullName.Name
		svc.Namespace = fullName.Namespace
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Port:       *ref.BackendObjectReference.Port,
				TargetPort: intstr.FromInt(rand.IntN(65535)),
			},
		}
	}
}

type randomParentRefOpt func(*gatewayv1.ParentReference)

func makeRandomParentRef(
	opts ...randomParentRefOpt,
) gatewayv1.ParentReference {
	fake := faker.New()
	ref := gatewayv1.ParentReference{
		Name:      gatewayv1.ObjectName(fake.Internet().Domain()),
		Namespace: new(gatewayv1.Namespace(fake.Internet().Domain())),
	}

	for _, opt := range opts {
		opt(&ref)
	}

	return ref
}

func randomParentRefWithRandomSectionNameOpt() randomParentRefOpt {
	return func(ref *gatewayv1.ParentReference) {
		fake := faker.New()
		ref.SectionName = new(gatewayv1.SectionName(fake.Internet().Domain()))
	}
}

func randomParentRefWithRandomPortOpt() randomParentRefOpt {
	return func(ref *gatewayv1.ParentReference) {
		ref.Port = new(rand.Int32N(65535))
	}
}

type randomRouteParentStatusOpt func(*gatewayv1.RouteParentStatus)

func makeRandomRouteParentStatus(
	opts ...randomRouteParentStatusOpt,
) gatewayv1.RouteParentStatus {
	fake := faker.New()
	status := gatewayv1.RouteParentStatus{
		ParentRef:      makeRandomParentRef(),
		ControllerName: gatewayv1.GatewayController(fake.Lorem().Word() + "." + fake.Internet().Domain()),
	}

	for _, opt := range opts {
		opt(&status)
	}

	return status
}

func randomRouteParentStatusWithConditionOpt(
	conditionType string,
	conditionStatus metav1.ConditionStatus,
) randomRouteParentStatusOpt {
	return func(status *gatewayv1.RouteParentStatus) {
		status.Conditions = append(status.Conditions, metav1.Condition{
			Type:   conditionType,
			Status: conditionStatus,
		})
	}
}

func randomRouteParentStatusWithControllerNameOpt(controllerName string) randomRouteParentStatusOpt {
	return func(status *gatewayv1.RouteParentStatus) {
		status.ControllerName = gatewayv1.GatewayController(controllerName)
	}
}

type randomEndpointSliceOpt func(*discoveryv1.EndpointSlice)

func makeRandomEndpointSlice(
	opts ...randomEndpointSliceOpt,
) discoveryv1.EndpointSlice {
	fake := faker.New()
	svcName := fake.Lorem().Word() + "." + fake.Internet().Domain()
	epSlice := discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fake.Internet().Domain(),
			Namespace: fake.Internet().Slug(),
			Labels: map[string]string{
				discoveryv1.LabelServiceName: svcName,
			},
		},
	}

	for _, opt := range opts {
		opt(&epSlice)
	}

	return epSlice
}

func randomEndpointSliceWithNamespaceOpt(namespace string) randomEndpointSliceOpt {
	return func(ep *discoveryv1.EndpointSlice) {
		ep.Namespace = namespace
	}
}

func randomEndpointSliceWithServiceNameOpt(serviceName string) randomEndpointSliceOpt {
	return func(ep *discoveryv1.EndpointSlice) {
		if ep.Labels == nil {
			ep.Labels = make(map[string]string)
		}
		ep.Labels[discoveryv1.LabelServiceName] = serviceName
	}
}

type randomEndpointOpt func(*discoveryv1.Endpoint)

// makeFewRandomEndpoints generates a slice of random endpoints.
func makeFewRandomEndpoints(count int, opts ...randomEndpointOpt) []discoveryv1.Endpoint {
	endpoints := make([]discoveryv1.Endpoint, count)
	for i := range endpoints {
		endpoints[i] = makeRandomEndpoint(opts...)
	}
	return endpoints
}

// randomEndpointWithConditionsOpt sets the Ready and Terminating conditions.
func randomEndpointWithConditionsOpt(ready *bool, terminating *bool) randomEndpointOpt {
	return func(ep *discoveryv1.Endpoint) {
		ep.Conditions.Ready = ready
		ep.Conditions.Terminating = terminating
	}
}

func makeRandomEndpoint(opts ...randomEndpointOpt) discoveryv1.Endpoint {
	fake := faker.New()
	ep := discoveryv1.Endpoint{
		Addresses: []string{fake.Internet().Ipv4()},
		// Conditions are left nil by default, specific tests should set them.
	}

	for _, opt := range opts {
		opt(&ep)
	}

	return ep
}

func makeRandomSecret(opts ...randomSecretOpt) corev1.Secret {
	fake := faker.New()
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            fake.Internet().Domain(),
			Namespace:       fake.Internet().Slug(),
			ResourceVersion: fake.UUID().V4(),
			UID:             apitypes.UID(fake.UUID().V4()),
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{},
	}

	for _, opt := range opts {
		opt(&secret)
	}

	return secret
}

type randomSecretOpt func(*corev1.Secret)

func randomSecretWithNameOpt(name string) randomSecretOpt {
	return func(secret *corev1.Secret) {
		secret.Name = name
	}
}

func randomSecretWithTLSDataOpt() randomSecretOpt {
	return func(secret *corev1.Secret) {
		fake := faker.New()
		secret.Data[corev1.TLSCertKey] = []byte(fake.UUID().V4())
		secret.Data[corev1.TLSPrivateKeyKey] = []byte(fake.UUID().V4())
	}
}

func setupClientGet(
	t *testing.T,
	cl k8sClient,
	wantName apitypes.NamespacedName,
	wantObj any,
) *mock.Call {
	mockK8sClient, _ := cl.(*Mockk8sClient)
	result := mockK8sClient.EXPECT().Get(
		t.Context(),
		wantName,
		mock.Anything,
	).RunAndReturn(func(
		_ context.Context,
		name apitypes.NamespacedName,
		obj client.Object,
		_ ...client.GetOption,
	) error {
		assert.Equal(t, wantName, name)
		reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(wantObj))
		return nil
	})

	return result.Call
}
