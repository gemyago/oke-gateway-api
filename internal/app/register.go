package app

import (
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gemyago/oke-gateway-api/internal/di"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		func(c client.Client) k8sClient { return c },
		func(c loadbalancer.LoadBalancerClient) ociLoadBalancerClient { return c },
		func(w *ociapi.WorkRequestsWatcher) workRequestsWatcher { return w },
		NewGatewayClassController,
		NewGatewayController,
		NewHTTPRouteController,
		di.ConstructorWithOpts{
			Constructor: newResourcesModel,
			Options:     []dig.ProvideOption{dig.As(new(resourcesModel))},
		},
		di.ConstructorWithOpts{
			Constructor: newGatewayModel,
			Options:     []dig.ProvideOption{dig.As(new(gatewayModel))},
		},
		di.ConstructorWithOpts{
			Constructor: newHTTPRouteModel,
			Options:     []dig.ProvideOption{dig.As(new(httpRouteModel))},
		},
		di.ConstructorWithOpts{
			Constructor: newOciLoadBalancerModel,
			Options:     []dig.ProvideOption{dig.As(new(ociLoadBalancerModel))},
		},
		di.ConstructorWithOpts{
			Constructor: newOciLoadBalancerRoutingRulesMapper,
			Options:     []dig.ProvideOption{dig.As(new(ociLoadBalancerRoutingRulesMapper))},
		},
		di.ConstructorWithOpts{
			Constructor: newHTTPBackendModel,
			Options:     []dig.ProvideOption{dig.As(new(httpBackendModel))},
		},
		NewWatchesModel,
	)
}
