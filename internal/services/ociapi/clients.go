package ociapi

import (
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/workrequests"
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

func newWorkRequestsClient(
	configProvider common.ConfigurationProvider,
) (workrequests.WorkRequestClient, error) {
	client, err := workrequests.NewWorkRequestClientWithConfigurationProvider(configProvider)
	if err != nil {
		return workrequests.WorkRequestClient{}, fmt.Errorf("failed to create work requests client: %w", err)
	}
	return client, nil
}
