package app

import (
	"math/rand/v2"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
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

func randomGatewayClassWithRandomControllerNameOpt() randomGatewayClassOpt {
	return func(gc *gatewayv1.GatewayClass) {
		gc.Spec.ControllerName = gatewayv1.GatewayController(faker.DomainName())
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
		gw.Spec.Listeners = make([]gatewayv1.Listener, rand.IntN(3))
		for i := range gw.Spec.Listeners {
			gw.Spec.Listeners[i] = makeRandomHTTPListener()
		}
	}
}

type randomHTTPListenerOpt func(*gatewayv1.Listener)

func makeRandomHTTPListener(
	opts ...randomHTTPListenerOpt,
) gatewayv1.Listener {
	listener := gatewayv1.Listener{
		Name:     gatewayv1.SectionName("listener-" + faker.UUIDHyphenated()),
		Port:     gatewayv1.PortNumber(rand.Int32N(4000)),
		Protocol: gatewayv1.HTTPProtocolType,
	}

	for _, opt := range opts {
		opt(&listener)
	}

	return listener
}

func makeRandomAcceptedGatewayDetails() *acceptedGatewayDetails {
	return &acceptedGatewayDetails{
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
			Name:      faker.DomainName(),
			Namespace: faker.Username(),
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

func makeRandomParentRef() gatewayv1.ParentReference {
	return gatewayv1.ParentReference{
		Name:      gatewayv1.ObjectName(faker.DomainName()),
		Namespace: lo.ToPtr(gatewayv1.Namespace(faker.DomainName())),
	}
}
