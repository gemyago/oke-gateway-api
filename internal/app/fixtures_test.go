package app

import (
	"math/rand/v2"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
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
			ControllerName: ControllerClassName,
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

func makeRandomGatewayConfig() types.GatewayConfig {
	return types.GatewayConfig{
		Spec: types.GatewayConfigSpec{
			LoadBalancerID: faker.UUIDHyphenated(),
		},
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
			gw.Spec.Listeners[i] = makeRandomHTTPListener()
		}
	}
}

func makeRandomHTTPListener() gatewayv1.Listener {
	listener := gatewayv1.Listener{
		Name:     gatewayv1.SectionName("listener-" + faker.UUIDHyphenated()),
		Port:     gatewayv1.PortNumber(rand.Int32N(4000)),
		Protocol: gatewayv1.HTTPProtocolType,
	}

	return listener
}

func makeRandomAcceptedGatewayDetails() *resolvedGatewayDetails {
	return &resolvedGatewayDetails{
		gateway:      *newRandomGateway(),
		gatewayClass: *newRandomGatewayClass(),
		config:       makeRandomGatewayConfig(),
	}
}

func makeRandomOCIBackendSet() loadbalancer.BackendSet {
	return loadbalancer.BackendSet{
		Name: lo.ToPtr(faker.DomainName()),
	}
}

type randomOCIListenerOpt func(*loadbalancer.Listener)

func makeRandomOCIListener(
	opts ...randomOCIListenerOpt,
) loadbalancer.Listener {
	listener := loadbalancer.Listener{
		Name: lo.ToPtr(faker.DomainName()),
	}

	for _, opt := range opts {
		opt(&listener)
	}

	return listener
}

type randomOCILoadBalancerOpt func(*loadbalancer.LoadBalancer)

func makeRandomOCILoadBalancer(
	opts ...randomOCILoadBalancerOpt,
) loadbalancer.LoadBalancer {
	lb := loadbalancer.LoadBalancer{
		Id:        lo.ToPtr(faker.UUIDHyphenated()),
		Listeners: map[string]loadbalancer.Listener{},
	}

	for _, opt := range opts {
		opt(&lb)
	}

	return lb
}

func randomOCILoadBalancerWithRandomBackendSetsOpt() randomOCILoadBalancerOpt {
	return func(lb *loadbalancer.LoadBalancer) {
		lb.BackendSets = map[string]loadbalancer.BackendSet{}
		for range lb.BackendSets {
			bs := makeRandomOCIBackendSet()
			lb.BackendSets[*bs.Name] = bs
		}
	}
}

func randomOCILoadBalancerWithRandomListenersOpt() randomOCILoadBalancerOpt {
	return func(lb *loadbalancer.LoadBalancer) {
		for range rand.IntN(3) {
			lb.Listeners[faker.UUIDHyphenated()] = makeRandomOCIListener()
		}
	}
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

func randomHTTPRouteWithRandomParentRefOpt(ref gatewayv1.ParentReference) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Spec.ParentRefs = append(route.Spec.ParentRefs, ref)
	}
}

func randomHTTPRouteWithRandomRulesOpt(rules ...gatewayv1.HTTPRouteRule) randomHTTPRouteOpt {
	return func(route *gatewayv1.HTTPRoute) {
		route.Spec.Rules = append(route.Spec.Rules, rules...)
	}
}

type randomHTTPRouteRuleOpt func(*gatewayv1.HTTPRouteRule)

func randomHTTPRouteRule(
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

func randomHTTPRouteRuleWithRandomNameOpt() randomHTTPRouteRuleOpt {
	return func(rule *gatewayv1.HTTPRouteRule) {
		rule.Name = lo.ToPtr(gatewayv1.SectionName(faker.DomainName()))
	}
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

func makeRandomParentRef() gatewayv1.ParentReference {
	return gatewayv1.ParentReference{
		Name:      gatewayv1.ObjectName(faker.DomainName()),
		Namespace: lo.ToPtr(gatewayv1.Namespace(faker.DomainName())),
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

type randomBackendSetOpt func(*loadbalancer.BackendSet)

func makeRandomBackendSet(
	opts ...randomBackendSetOpt,
) loadbalancer.BackendSet {
	bs := loadbalancer.BackendSet{
		Name: lo.ToPtr(faker.DomainName()),
	}

	for _, opt := range opts {
		opt(&bs)
	}

	return bs
}

func randomBackendSetWithNameOpt(name string) randomBackendSetOpt {
	return func(bs *loadbalancer.BackendSet) {
		bs.Name = lo.ToPtr(name)
	}
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
