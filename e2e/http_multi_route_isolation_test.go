package e2e

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPMultiRouteIsolation(t *testing.T) {
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := fake.UUID().V4()
	backendAName := "echo-a-" + suffix
	backendBName := "echo-b-" + suffix
	httpRouteAName := "echo-route-a-" + suffix
	httpRouteBName := "echo-route-b-" + suffix
	probePathA := "/echo-a-" + suffix
	probePathB := "/echo-b-" + suffix

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

	for _, backendName := range []string{backendAName, backendBName} {
		service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
			Namespace: namespace.Name,
			Name:      backendName,
		})
		require.NoError(t, kubeClient.Create(ctx, service))
	}

	deploymentA := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespace.Name,
		Name:      backendAName,
	})
	require.NoError(t, kubeClient.Create(ctx, deploymentA))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backendAName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backendAName, nil)
	require.NoError(t, err)

	httpRouteA := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         httpRouteAName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendAName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePathA,
	})
	require.NoError(t, kubeClient.Create(ctx, httpRouteA))

	httpRouteB := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         httpRouteBName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendBName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePathB,
	})
	require.NoError(t, kubeClient.Create(ctx, httpRouteB))

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespace.Name, httpRouteAName, gatewayName, nil)
	require.NoError(t, err)
	resolvedRouteA, err := e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteAName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespace.Name, httpRouteBName, gatewayName, nil)
	require.NoError(t, err)
	_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteBName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePathA, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEchoGone(ctx, probeClient, probePathB, nil)
	require.NoError(t, err)

	deploymentB := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespace.Name,
		Name:      backendBName,
	})
	require.NoError(t, kubeClient.Create(ctx, deploymentB))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backendBName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backendBName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePathB, nil)
	require.NoError(t, err)

	programmedPolicyRulesA, err := waitForHTTPRouteProgrammedPolicyRuleNames(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteAName,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRulesA)

	programmedPolicyRulesB, err := waitForHTTPRouteProgrammedPolicyRuleNames(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteBName,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRulesB)

	err = e2eoci.WaitForRoutingPolicyRuleNamesPresent(
		ctx,
		ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRulesA,
		nil,
	)
	require.NoError(t, err)

	err = e2eoci.WaitForRoutingPolicyRuleNamesPresent(
		ctx,
		ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRulesB,
		nil,
	)
	require.NoError(t, err)

	err = kubeClient.Delete(ctx, &gatewayv1.HTTPRoute{
		ObjectMeta: resolvedRouteA.ObjectMeta,
	})
	require.NoError(t, err)

	err = e2ek8s.WaitForHTTPRouteDeleted(ctx, kubeClient.Client, namespace.Name, httpRouteAName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEchoGone(ctx, probeClient, probePathA, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePathB, nil)
	require.NoError(t, err)

	err = e2eoci.WaitForRoutingPolicyRuleNamesAbsent(
		ctx,
		ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRulesA,
		nil,
	)
	require.NoError(t, err)

	err = e2eoci.WaitForRoutingPolicyRuleNamesPresent(
		ctx,
		ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRulesB,
		nil,
	)
	require.NoError(t, err)
}
