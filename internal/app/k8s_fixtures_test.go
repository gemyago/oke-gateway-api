package app

import (
	"context"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/go-faker/faker/v4"
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
	gc := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:            faker.DomainName(),
			Generation:      rand.Int64(),
			UID:             apitypes.UID(faker.UUIDHyphenated()), // Add UID for potential future use
			ResourceVersion: faker.Word(),                         // Add RV for potential future use
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(faker.UUIDHyphenated() + "." + faker.DomainName()),
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
	gw := gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       faker.DomainName(),
			Namespace:  faker.Username(), // Gateways are namespaced
			Generation: rand.Int64(),
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(faker.DomainName()),
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
	return gatewayv1.SecretObjectReference{
		Name:      gatewayv1.ObjectName("secret-" + faker.DomainName()),
		Namespace: lo.ToPtr(gatewayv1.Namespace("ns-" + faker.DomainName())),
	}
}

type randomListenerOpt func(*gatewayv1.Listener)

func makeRandomListener(
	opts ...randomListenerOpt,
) gatewayv1.Listener {
	listener := gatewayv1.Listener{
		Name:     gatewayv1.SectionName("listener-" + faker.UUIDHyphenated()),
		Port:     gatewayv1.PortNumber(rand.Int32N(4000)),
		Protocol: gatewayv1.ProtocolType(faker.Word()),
	}

	for _, opt := range opts {
		opt(&listener)
	}

	return listener
}

func randomListenerWithHTTPSParamsOpt() randomListenerOpt {
	return func(listener *gatewayv1.Listener) {
		listener.Protocol = gatewayv1.HTTPSProtocolType
		listener.TLS = &gatewayv1.GatewayTLSConfig{
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

func randomListenerWithHTTPSProtocolOpt() randomListenerOpt {
	return func(listener *gatewayv1.Listener) {
		listener.Protocol = gatewayv1.HTTPSProtocolType
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
	route := gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       faker.DomainName(),
			Namespace:  faker.Username(),
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
	ref := gatewayv1.HTTPBackendRef{
		BackendRef: gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name:      gatewayv1.ObjectName(faker.DomainName()),
				Namespace: lo.ToPtr(gatewayv1.Namespace(faker.DomainName())),
				Port:      lo.ToPtr(gatewayv1.PortNumber(rand.Int32N(65535))),
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
		ref.BackendObjectReference.Namespace = lo.ToPtr(gatewayv1.Namespace(namespace))
	}
}

type randomServiceOpt func(*corev1.Service)

func makeRandomService(
	opts ...randomServiceOpt,
) corev1.Service {
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      faker.DomainName(),
			Namespace: faker.Username(),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": faker.DomainName(),
			},
			ClusterIP: faker.IPv4(),
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
				Port:       int32(*ref.BackendObjectReference.Port),
				TargetPort: intstr.FromInt(rand.IntN(65535)),
			},
		}
	}
}

type randomParentRefOpt func(*gatewayv1.ParentReference)

func makeRandomParentRef(
	opts ...randomParentRefOpt,
) gatewayv1.ParentReference {
	ref := gatewayv1.ParentReference{
		Name:      gatewayv1.ObjectName(faker.DomainName()),
		Namespace: lo.ToPtr(gatewayv1.Namespace(faker.DomainName())),
	}

	for _, opt := range opts {
		opt(&ref)
	}

	return ref
}

func randomParentRefWithRandomSectionNameOpt() randomParentRefOpt {
	return func(ref *gatewayv1.ParentReference) {
		ref.SectionName = lo.ToPtr(gatewayv1.SectionName(faker.DomainName()))
	}
}

func randomParentRefWithRandomPortOpt() randomParentRefOpt {
	return func(ref *gatewayv1.ParentReference) {
		ref.Port = lo.ToPtr(gatewayv1.PortNumber(rand.Int32N(65535)))
	}
}

type randomRouteParentStatusOpt func(*gatewayv1.RouteParentStatus)

func makeRandomRouteParentStatus(
	opts ...randomRouteParentStatusOpt,
) gatewayv1.RouteParentStatus {
	status := gatewayv1.RouteParentStatus{
		ParentRef:      makeRandomParentRef(),
		ControllerName: gatewayv1.GatewayController(faker.Word() + "." + faker.DomainName()),
	}

	for _, opt := range opts {
		opt(&status)
	}

	return status
}

type randomEndpointSliceOpt func(*discoveryv1.EndpointSlice)

func makeRandomEndpointSlice(
	opts ...randomEndpointSliceOpt,
) discoveryv1.EndpointSlice {
	svcName := faker.Word() + "." + faker.DomainName()
	epSlice := discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      faker.DomainName(),
			Namespace: faker.Username(),
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
	ep := discoveryv1.Endpoint{
		Addresses: []string{faker.IPv4()},
		// Conditions are left nil by default, specific tests should set them.
	}

	for _, opt := range opts {
		opt(&ep)
	}

	return ep
}

func makeRandomSecret(opts ...randomSecretOpt) corev1.Secret {
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            faker.DomainName(),
			Namespace:       faker.Username(),
			ResourceVersion: faker.UUIDHyphenated(),
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
		secret.Data[corev1.TLSCertKey] = []byte(faker.UUIDHyphenated())
		secret.Data[corev1.TLSPrivateKeyKey] = []byte(faker.UUIDHyphenated())
	}
}

func setupClientGet(
	t *testing.T,
	cl k8sClient,
	wantName apitypes.NamespacedName,
	wantObj interface{},
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
