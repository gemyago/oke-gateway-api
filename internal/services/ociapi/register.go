package ociapi

import (
	"github.com/gemyago/oke-gateway-api/internal/di"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"go.uber.org/dig"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		newConfigProvider,
		newLoadBalancerClient,
		NewWorkRequestsWatcher,
		func(c loadbalancer.LoadBalancerClient) workRequestsClient { return c },
	)
}
