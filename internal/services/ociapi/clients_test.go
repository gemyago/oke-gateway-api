package ociapi

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type testConfigurationProvider struct {
	key *rsa.PrivateKey
}

func (p testConfigurationProvider) PrivateRSAKey() (*rsa.PrivateKey, error) {
	return p.key, nil
}

func (testConfigurationProvider) KeyID() (string, error) {
	return "ocid1.tenancy.oc1..example/ocid1.user.oc1..example/example-fingerprint", nil
}

func (testConfigurationProvider) TenancyOCID() (string, error) {
	return "ocid1.tenancy.oc1..example", nil
}

func (testConfigurationProvider) UserOCID() (string, error) {
	return "ocid1.user.oc1..example", nil
}

func (testConfigurationProvider) KeyFingerprint() (string, error) {
	return "example-fingerprint", nil
}

func (testConfigurationProvider) Region() (string, error) {
	return "us-ashburn-1", nil
}

func (testConfigurationProvider) AuthType() (common.AuthConfig, error) {
	return common.AuthConfig{AuthType: common.UserPrincipal}, nil
}

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

func TestClients(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	deps := LoadBalancerConfigDeps{
		RootLogger:     diag.RootTestLogger(),
		ConfigProvider: testConfigurationProvider{key: key},
	}

	t.Run("creates load balancer client", func(t *testing.T) {
		_, clientErr := newLoadBalancerClient(deps)

		require.NoError(t, clientErr)
	})

	t.Run("creates network load balancer client", func(t *testing.T) {
		_, clientErr := newNetworkLoadBalancerClient(deps)

		require.NoError(t, clientErr)
	})

	t.Run("creates certificates management client", func(t *testing.T) {
		_, clientErr := newCertificatesManagementClient(deps)

		require.NoError(t, clientErr)
	})
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
