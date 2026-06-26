package app

import (
	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gemyago/oke-gateway-api/internal/di"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

func Register(container *dig.Container) error {
	return di.ProvideAll(container,
		func(c client.Client) k8sClient { return c },
		func(c loadbalancer.LoadBalancerClient) ociLoadBalancerClient { return c },
		func(c networkloadbalancer.NetworkLoadBalancerClient) ociNetworkLoadBalancerClient { return c },
		func(c certificatesmanagement.CertificatesManagementClient) ociCertificatesManagementClient { return c },
		func(w *ociapi.WorkRequestsWatcher) workRequestsWatcher { return w },
		di.ConstructorWithOpts{
			Constructor: func(w *ociapi.NetworkLoadBalancerWorkRequestsWatcher) workRequestsWatcher { return w },
			Options:     []dig.ProvideOption{dig.Name("networkLoadBalancerWorkRequestsWatcher")},
		},
		NewGatewayClassController,
		NewGatewayController,
		NewNetworkLoadBalancerGatewayController,
		NewHTTPRouteController,
		NewGRPCRouteController,
		NewTCPRouteController,
		NewUDPRouteController,
		NewTLSRouteController,
		NewBackendTLSPolicyController,
		newNetworkLoadBalancerOperationLocks,
		di.ProvideFactoryAs[resourcesModel](newResourcesModel),
		di.ProvideFactoryAs[gatewayModel](newGatewayModel),
		di.ProvideFactoryAs[networkLoadBalancerGatewayModel](newNetworkLoadBalancerGatewayModel),
		di.ProvideFactoryAs[httpRouteModel](newHTTPRouteModel),
		di.ProvideFactoryAs[grpcRouteModel](newGRPCRouteModel),
		di.ProvideFactoryAs[tcpRouteModel](newTCPRouteModel),
		di.ProvideFactoryAs[udpRouteModel](newUDPRouteModel),
		di.ProvideFactoryAs[tlsRouteModel](newTLSRouteModel),
		di.ProvideFactoryAs[ociLoadBalancerModel](newOciLoadBalancerModel),
		di.ProvideFactoryAs[backendTLSPolicyModel](newBackendTLSPolicyModel),
		newOciLoadBalancerRoutingRulesMapper,
		di.ProvideAs[*ociLoadBalancerRoutingRulesMapperImpl, ociLoadBalancerRoutingRulesMapper],
		di.ProvideFactoryAs[httpBackendModel](newHTTPBackendModel),
		NewWatchesModel,
	)
}
