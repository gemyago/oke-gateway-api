package e2e

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPRouteLifecycle(t *testing.T) {
	ctx, cfg := newLiveHTTPContext(t)

	kubeClient, err := e2ek8s.NewClient(cfg.Kubernetes, nil)
	require.NoError(t, err)

	ociClient, err := e2eoci.NewLoadBalancerClient(cfg.OCI, nil)
	require.NoError(t, err)

	inspector := e2eoci.NewLoadBalancerCleaner(ociClient, slog.New(slog.DiscardHandler), nil)
	loadBalancer, err := inspector.Inspect(ctx, cfg.OCI.LoadBalancerID)
	require.NoError(t, err)

	probeClient, err := probe.NewClient(loadBalancer.PublicIP, cfg.HTTPPort, nil)
	require.NoError(t, err)

	_ = startHTTPController(t, cfg)

	namespace, err := e2ek8s.CreateUniqueNamespace(ctx, kubeClient.Client, cfg.NamespacePrefix)
	require.NoError(t, err)

	var cleanupOnce sync.Once
	gatewayClassName := uniqueGatewayClassName(cfg.GatewayClassName, namespace.Name)
	registerCleanup(t, &cleanupOnce, kubeClient.Client, namespace.Name, gatewayClassName)

	gatewayClass := e2ek8s.NewGatewayClass(e2ek8s.GatewayClassOptions{
		Name: gatewayClassName,
	})
	require.NoError(t, kubeClient.Create(ctx, gatewayClass))

	_, err = e2ek8s.WaitForGatewayClassAccepted(ctx, kubeClient.Client, gatewayClassName, nil)
	require.NoError(t, err)

	gatewayConfig := e2ek8s.NewGatewayConfig(e2ek8s.GatewayConfigOptions{
		Namespace:      namespace.Name,
		Name:           gatewayConfigName,
		LoadBalancerID: cfg.OCI.LoadBalancerID,
	})
	require.NoError(t, kubeClient.Create(ctx, gatewayConfig))

	gateway := e2ek8s.NewHTTPGateway(e2ek8s.HTTPGatewayOptions{
		Namespace:         namespace.Name,
		Name:              gatewayName,
		GatewayClassName:  gatewayClassName,
		GatewayConfigName: gatewayConfigName,
		Port:              gatewayv1.PortNumber(cfg.HTTPPort),
	})
	require.NoError(t, kubeClient.Create(ctx, gateway))

	_, err = e2ek8s.WaitForGatewayAccepted(ctx, kubeClient.Client, namespace.Name, gatewayName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForGatewayProgrammed(ctx, kubeClient.Client, namespace.Name, gatewayName, nil)
	require.NoError(t, err)

	deployment := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespace.Name,
		Name:      backendName,
	})
	require.NoError(t, kubeClient.Create(ctx, deployment))

	service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
		Namespace: namespace.Name,
		Name:      backendName,
	})
	require.NoError(t, kubeClient.Create(ctx, service))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         httpRouteName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePath,
	})
	require.NoError(t, kubeClient.Create(ctx, httpRoute))

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespace.Name, httpRouteName, gatewayName, nil)
	require.NoError(t, err)

	resolvedRoute, err := e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePath, nil)
	require.NoError(t, err)

	programmedPolicyRules, err := waitForHTTPRouteProgrammedPolicyRuleNames(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteName,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRules)

	err = kubeClient.Delete(ctx, &gatewayv1.HTTPRoute{
		ObjectMeta: resolvedRoute.ObjectMeta,
	})
	require.NoError(t, err)

	err = e2ek8s.WaitForHTTPRouteDeleted(ctx, kubeClient.Client, namespace.Name, httpRouteName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEchoGone(ctx, probeClient, probePath, nil)
	require.NoError(t, err)

	err = e2eoci.WaitForRoutingPolicyRuleNamesAbsent(
		ctx,
		ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRules,
		nil,
	)
	require.NoError(t, err)
}
