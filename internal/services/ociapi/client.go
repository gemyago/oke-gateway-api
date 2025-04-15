package ociapi

import (
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

func newLoadBalancerClient() (loadbalancer.LoadBalancerClient, error) {
	// TODO: This needs more advanced setup and support in cluster config
	configProvider := common.DefaultConfigProvider()
	return loadbalancer.NewLoadBalancerClientWithConfigurationProvider(configProvider)
}
