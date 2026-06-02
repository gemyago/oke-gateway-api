package app

import (
	"net"
	"sort"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	gatewayStatusAddressRankPublicIP = iota
	gatewayStatusAddressRankNonPublicIP
	gatewayStatusAddressRankOther
)

func gatewayStatusAddressesFromValues(values []string) []gatewayv1.GatewayStatusAddress {
	if len(values) == 0 {
		return nil
	}

	addressType := gatewayv1.IPAddressType
	addressesByValue := make(map[string]gatewayv1.GatewayStatusAddress, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		addressesByValue[value] = gatewayv1.GatewayStatusAddress{
			Type:  &addressType,
			Value: value,
		}
	}
	if len(addressesByValue) == 0 {
		return nil
	}

	addresses := make([]gatewayv1.GatewayStatusAddress, 0, len(addressesByValue))
	for _, address := range addressesByValue {
		addresses = append(addresses, address)
	}
	sortGatewayStatusAddresses(addresses)
	return addresses
}

func sortGatewayStatusAddresses(addresses []gatewayv1.GatewayStatusAddress) {
	sort.Slice(addresses, func(i, j int) bool {
		leftRank := gatewayStatusAddressRank(addresses[i])
		rightRank := gatewayStatusAddressRank(addresses[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return addresses[i].Value < addresses[j].Value
	})
}

func gatewayStatusAddressRank(address gatewayv1.GatewayStatusAddress) int {
	if address.Type == nil || *address.Type != gatewayv1.IPAddressType {
		return gatewayStatusAddressRankOther
	}

	ip := net.ParseIP(address.Value)
	if ip == nil {
		return gatewayStatusAddressRankOther
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return gatewayStatusAddressRankNonPublicIP
	}
	return gatewayStatusAddressRankPublicIP
}
