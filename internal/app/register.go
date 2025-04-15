package app

import (
	"github.com/gemyago/oke-gateway-api/internal/di"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		func(c client.Client) k8sClient { return c },
		func(c loadbalancer.LoadBalancerClient) ociLoadBalancerClient { return c },
		NewGatewayClassController,
		NewGatewayController,
		newResourcesModel,
		newGatewayModel,
		newOciLoadBalancerModel,
		newOkeWorkRequests,
	)
}
