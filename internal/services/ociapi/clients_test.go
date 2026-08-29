package ociapi

import (
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

func TestNoopClients(t *testing.T) {
	deps := LoadBalancerConfigDeps{
		RootLogger: diag.RootTestLogger(),
		Noop:       true,
	}

	_, err := newLoadBalancerClient(deps)
	require.NoError(t, err)

	_, err = newNetworkLoadBalancerClient(deps)
	require.NoError(t, err)

	_, err = newCertificatesManagementClient(deps)
	require.NoError(t, err)
}

func TestClientConfigErrors(t *testing.T) {
	deps := LoadBalancerConfigDeps{
		RootLogger:     diag.RootTestLogger(),
		ConfigProvider: common.NewRawConfigurationProvider("", "", "", "", "", nil),
		Noop:           false,
	}

	_, err := newLoadBalancerClient(deps)
	require.ErrorContains(t, err, "failed to create load balancer client")

	_, err = newNetworkLoadBalancerClient(deps)
	require.ErrorContains(t, err, "failed to create network load balancer client")

	_, err = newCertificatesManagementClient(deps)
	require.ErrorContains(t, err, "failed to create certificates management client")
}
