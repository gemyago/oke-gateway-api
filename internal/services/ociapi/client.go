package ociapi

import (
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

func newLoadBalancerClient(
	configProvider common.ConfigurationProvider,
) (loadbalancer.LoadBalancerClient, error) {
	return loadbalancer.NewLoadBalancerClientWithConfigurationProvider(configProvider)
}
