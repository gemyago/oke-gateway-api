package e2eoci

import (
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

const defaultOCIProfile = "DEFAULT"

type ClientFactoryOptions struct {
	defaultConfigProvider       func() common.ConfigurationProvider
	customProfileConfigProvider func(string, string) common.ConfigurationProvider
	newLoadBalancerClient       func(common.ConfigurationProvider) (loadbalancer.LoadBalancerClient, error)
}

func NewClientFactoryOptions() *ClientFactoryOptions {
	return &ClientFactoryOptions{
		defaultConfigProvider:       common.DefaultConfigProvider,
		customProfileConfigProvider: common.CustomProfileConfigProvider,
		newLoadBalancerClient:       loadbalancer.NewLoadBalancerClientWithConfigurationProvider,
	}
}

func NewLoadBalancerClient(
	cfg config.OCIConfig,
	opts *ClientFactoryOptions,
) (*loadbalancer.LoadBalancerClient, error) {
	if opts == nil {
		opts = NewClientFactoryOptions()
	}

	var provider common.ConfigurationProvider
	if cfg.ConfigFile == "" && cfg.ConfigProfile == "" {
		provider = opts.defaultConfigProvider()
	} else {
		profile := cfg.ConfigProfile
		if profile == "" {
			profile = defaultOCIProfile
		}

		provider = opts.customProfileConfigProvider(cfg.ConfigFile, profile)
	}

	client, err := opts.newLoadBalancerClient(provider)
	if err != nil {
		return nil, fmt.Errorf("create OCI load balancer client: %w", err)
	}

	return &client, nil
}
