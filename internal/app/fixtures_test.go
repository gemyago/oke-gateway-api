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

func newRandomGatewayClass() *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
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

func makeRandomLoadBalancer() loadbalancer.LoadBalancer {
	return loadbalancer.LoadBalancer{
		Id: lo.ToPtr(faker.UUIDHyphenated()),
	}
}
