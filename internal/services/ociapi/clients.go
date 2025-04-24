package ociapi

import (
	"fmt"
	"log/slog"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
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
