package ociapi

import (
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"go.uber.org/dig"

	"github.com/gemyago/oke-gateway-api/internal/di"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		newConfigProvider,
		newLoadBalancerClient,
		newNetworkLoadBalancerClient,
		newCertificatesManagementClient,
		NewWorkRequestsWatcher,
		NewNetworkLoadBalancerWorkRequestsWatcher,
		func(c loadbalancer.LoadBalancerClient) workRequestsClient { return c },
		func(c networkloadbalancer.NetworkLoadBalancerClient) networkLoadBalancerWorkRequestsClient { return c },
	)
}
