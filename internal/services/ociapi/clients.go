package ociapi

import (
	"fmt"
	"log/slog"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"go.uber.org/dig"
)

type LoadBalancerConfigDeps struct {
	dig.In

	RootLogger *slog.Logger

	ConfigProvider common.ConfigurationProvider

	// This can be set via APP_OCIAPI_NOOP env variable
	Noop bool `name:"config.ociapi.noop"`
}

func newLoadBalancerClient(
	deps LoadBalancerConfigDeps,
) (loadbalancer.LoadBalancerClient, error) {
	if deps.Noop {
		deps.RootLogger.Warn("OCI API client is in noop mode")
		return loadbalancer.LoadBalancerClient{}, nil
	}

	client, err := loadbalancer.NewLoadBalancerClientWithConfigurationProvider(deps.ConfigProvider)
	if err != nil {
		return loadbalancer.LoadBalancerClient{}, fmt.Errorf("failed to create load balancer client: %w", err)
	}
	return client, nil
}

func newNetworkLoadBalancerClient(
	deps LoadBalancerConfigDeps,
) (networkloadbalancer.NetworkLoadBalancerClient, error) {
	if deps.Noop {
		deps.RootLogger.Warn("OCI API client is in noop mode")
		return networkloadbalancer.NetworkLoadBalancerClient{}, nil
	}

	client, err := networkloadbalancer.NewNetworkLoadBalancerClientWithConfigurationProvider(deps.ConfigProvider)
	if err != nil {
		return networkloadbalancer.NetworkLoadBalancerClient{}, fmt.Errorf(
			"failed to create network load balancer client: %w",
			err,
		)
	}
	return client, nil
}
