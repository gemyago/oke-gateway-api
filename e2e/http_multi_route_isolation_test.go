package e2e

import (
	"log/slog"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPMultiRouteIsolation(t *testing.T, live *liveFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	backendAName := "echo-a-" + suffix
	backendBName := "echo-b-" + suffix
	httpRouteAName := "echo-route-a-" + suffix
	httpRouteBName := "echo-route-b-" + suffix
	probePathA := "/echo-a-" + suffix
	probePathB := "/echo-b-" + suffix

	logger.InfoContext(ctx, "Loaded live HTTP multi-route isolation configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.String("suffix", suffix),
	)

	gatewayFixture, err := createIsolatedHTTPGateway(ctx, t, live, cfg, suffix)
	require.NoError(t, err)
	kubeClient := live.kubeClient
	ociClient := live.ociClient
	probeClient := gatewayFixture.probeClient
	namespaceName := gatewayFixture.namespaceName
	gatewayName := gatewayFixture.gatewayName
	logTestProgress(
		ctx,
		t,
		logger,
		"Isolated HTTP gateway is ready",
		slog.String("namespace", namespaceName),
		slog.String("gateway", gatewayName),
	)

	for _, backendName := range []string{backendAName, backendBName} {
		service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
			Namespace: namespaceName,
			Name:      backendName,
		})
		logger.InfoContext(
			ctx,
			"Creating backend service",
			slog.String("namespace", namespaceName),
			slog.String("service", backendName),
		)
		require.NoError(t, kubeClient.Create(ctx, service))
	}

	deploymentA := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespaceName,
		Name:      backendAName,
	})
	logger.InfoContext(
		ctx,
		"Creating first backend deployment",
		slog.String("namespace", namespaceName),
		slog.String("deployment", backendAName),
	)
	require.NoError(t, kubeClient.Create(ctx, deploymentA))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespaceName, backendAName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespaceName, backendAName, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"First backend is ready",
		slog.String("namespace", namespaceName),
		slog.String("backend", backendAName),
	)

	httpRouteA := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespaceName,
		Name:         httpRouteAName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendAName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePathA,
	})
	logger.InfoContext(
		ctx,
		"Creating first HTTPRoute",
		slog.String("namespace", namespaceName),
		slog.String("httpRoute", httpRouteAName),
		slog.String("probePath", probePathA),
	)
	require.NoError(t, kubeClient.Create(ctx, httpRouteA))

	httpRouteB := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespaceName,
		Name:         httpRouteBName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendBName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePathB,
	})
	logger.InfoContext(
		ctx,
		"Creating second HTTPRoute before backend is ready",
		slog.String("namespace", namespaceName),
		slog.String("httpRoute", httpRouteBName),
		slog.String("probePath", probePathB),
	)
	require.NoError(t, kubeClient.Create(ctx, httpRouteB))

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespaceName, httpRouteAName, gatewayName, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"First route accepted and resolved",
		slog.String("httpRoute", httpRouteAName),
	)
	resolvedRouteA, err := e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespaceName,
		httpRouteAName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespaceName, httpRouteBName, gatewayName, nil)
	require.NoError(t, err)
	_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespaceName,
		httpRouteBName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Second route accepted and resolved",
		slog.String("httpRoute", httpRouteBName),
	)

	_, err = probe.WaitForEcho(ctx, probeClient, probePathA, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"First route is serving traffic",
		slog.String("probePath", probePathA),
	)

	_, err = probe.WaitForEchoGone(ctx, probeClient, probePathB, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Second route correctly remains isolated until its backend is ready",
		slog.String("probePath", probePathB),
	)

	deploymentB := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespaceName,
		Name:      backendBName,
	})
	logger.InfoContext(
		ctx,
		"Creating second backend deployment",
		slog.String("namespace", namespaceName),
		slog.String("deployment", backendBName),
	)
	require.NoError(t, kubeClient.Create(ctx, deploymentB))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespaceName, backendBName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespaceName, backendBName, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Second backend is ready",
		slog.String("namespace", namespaceName),
		slog.String("backend", backendBName),
	)

	_, err = probe.WaitForEcho(ctx, probeClient, probePathB, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Second route is now serving traffic",
		slog.String("probePath", probePathB),
	)

	programmedPolicyRulesA, err := waitForHTTPRouteProgrammedPolicyRuleNames(
		ctx,
		kubeClient.Client,
		namespaceName,
		httpRouteAName,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRulesA)

	programmedPolicyRulesB, err := waitForHTTPRouteProgrammedPolicyRuleNames(
		ctx,
		kubeClient.Client,
		namespaceName,
		httpRouteBName,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRulesB)
	logger.InfoContext(ctx, "Captured routing policy rules for both routes",
		slog.Any("routeARules", programmedPolicyRulesA),
		slog.Any("routeBRules", programmedPolicyRulesB),
	)

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

	logTestProgress(
		ctx,
		t,
		logger,
		"Deleting first HTTPRoute to verify rule isolation",
		slog.String("httpRoute", httpRouteAName),
	)
	err = kubeClient.Delete(ctx, &gatewayv1.HTTPRoute{
		ObjectMeta: resolvedRouteA.ObjectMeta,
	})
	require.NoError(t, err)

	err = e2ek8s.WaitForHTTPRouteDeleted(ctx, kubeClient.Client, namespaceName, httpRouteAName, nil)
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
	logTestProgress(
		ctx,
		t,
		logger,
		"Multi-route isolation validated successfully",
		slog.String("removedRoute", httpRouteAName),
		slog.String("remainingRoute", httpRouteBName),
	)
}
