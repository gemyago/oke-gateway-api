package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayStatusAddressesFromValues(t *testing.T) {
	addressType := gatewayv1.IPAddressType

	t.Run("converts raw IP values to deduplicated status addresses", func(t *testing.T) {
		assert.Equal(t, []gatewayv1.GatewayStatusAddress{
			{Type: &addressType, Value: "192.0.2.10"},
			{Type: &addressType, Value: "198.51.100.20"},
			{Type: &addressType, Value: "10.0.0.12"},
		}, gatewayStatusAddressesFromValues([]string{
			"10.0.0.12",
			"192.0.2.10",
			"198.51.100.20",
			"192.0.2.10",
			"",
		}))

		assert.Nil(t, gatewayStatusAddressesFromValues(nil))
	})

	t.Run("sorts caller supplied typed addresses by public IP first", func(t *testing.T) {
		hostNameType := gatewayv1.HostnameAddressType
		addresses := []gatewayv1.GatewayStatusAddress{
			{Type: &addressType, Value: "10.0.0.12"},
			{Type: &hostNameType, Value: "lb.example.com"},
			{Value: "unset-type"},
			{Type: &addressType, Value: "192.0.2.10"},
			{Type: &addressType, Value: "127.0.0.1"},
			{Type: &addressType, Value: "169.254.10.10"},
		}
		sortGatewayStatusAddresses(addresses)

		assert.Equal(t, []gatewayv1.GatewayStatusAddress{
			{Type: &addressType, Value: "192.0.2.10"},
			{Type: &addressType, Value: "10.0.0.12"},
			{Type: &addressType, Value: "127.0.0.1"},
			{Type: &addressType, Value: "169.254.10.10"},
			{Type: &hostNameType, Value: "lb.example.com"},
			{Value: "unset-type"},
		}, addresses)
	})
}
