package app

import (
	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/go-faker/faker/v4"
)

func makeRandomGatewayConfig() types.GatewayConfig {
	return types.GatewayConfig{
		Spec: types.GatewayConfigSpec{
			LoadBalancerID: faker.UUIDHyphenated(),
		},
	}
}

type randomResolvedGatewayDetailsOpt func(*resolvedGatewayDetails)

func makeRandomAcceptedGatewayDetails(
	opts ...randomResolvedGatewayDetailsOpt,
) *resolvedGatewayDetails {
	details := &resolvedGatewayDetails{
		gateway:      *newRandomGateway(),
		gatewayClass: *newRandomGatewayClass(),
		config:       makeRandomGatewayConfig(),
	}

	for _, opt := range opts {
		opt(details)
	}

	return details
}

func randomResolvedGatewayDetailsWithGatewayOpts(
	opts ...randomGatewayOpt,
) randomResolvedGatewayDetailsOpt {
	return func(details *resolvedGatewayDetails) {
		details.gateway = *newRandomGateway(opts...)
	}
}
