package ociapi

import (
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

func newLoadBalancerClient(
	configProvider common.ConfigurationProvider,
) (loadbalancer.LoadBalancerClient, error) {
	client, err := loadbalancer.NewLoadBalancerClientWithConfigurationProvider(configProvider)
	if err != nil {
		return loadbalancer.LoadBalancerClient{}, fmt.Errorf("failed to create load balancer client: %w", err)
	}
	return client, nil
}
