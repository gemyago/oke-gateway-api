package e2e

import (
	"log/slog"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPPathExactVsPrefix(t *testing.T, sharedFixture *sharedHTTPRoutingFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)
	fixture := sharedFixture.Get(t, cfg)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	backendName := "path-backend-" + suffix
	exactRouteName := "path-route-exact-" + suffix
	prefixRouteName := "path-route-prefix-" + suffix
	exactPath := "/exact-" + suffix
	prefixPath := "/prefix-" + suffix
	exactPathExtra := exactPath + "/extra"
	prefixPathExtra := prefixPath + "/extra"
	exactPathMatchType := gatewayv1.PathMatchExact

	logger.InfoContext(ctx, "Loaded live HTTP path matching configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.String("exactPath", exactPath),
		slog.String("prefixPath", prefixPath),
	)

	logTestProgressContext(
		ctx,
		t,
		logger,
		"Using shared HTTP routing fixture",
		slog.String("namespace", fixture.namespaceName),
		slog.String("gateway", fixture.gatewayName),
	)

	registerHTTPRouteCleanup(t, fixture.kubeClient.WithWatch, fixture.namespaceName, exactRouteName, prefixRouteName)

	deployment := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: fixture.namespaceName,
		Name:      backendName,
	})
	service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
		Namespace: fixture.namespaceName,
		Name:      backendName,
	})
	logger.InfoContext(
		ctx,
		"Creating path-matching backend resources",
		slog.String("namespace", fixture.namespaceName),
		slog.String("deployment", backendName),
		slog.String("service", backendName),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, deployment))
	require.NoError(t, fixture.kubeClient.Create(ctx, service))

	_, err := e2ek8s.WaitForDeploymentReady(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		backendName,
		nil,
	)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		backendName,
		nil,
	)
	require.NoError(t, err)

	exactRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    fixture.namespaceName,
		Name:         exactRouteName,
		GatewayName:  fixture.gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathMatch: &gatewayv1.HTTPPathMatch{
			Type:  &exactPathMatchType,
			Value: &exactPath,
		},
	})
	logger.InfoContext(
		ctx,
		"Creating exact-path HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", exactRouteName),
		slog.String("path", exactPath),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, exactRoute))

	prefixRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    fixture.namespaceName,
		Name:         prefixRouteName,
		GatewayName:  fixture.gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   prefixPath,
	})
	logger.InfoContext(
		ctx,
		"Creating prefix-path HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", prefixRouteName),
		slog.String("path", prefixPath),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, prefixRoute))

	for _, routeName := range []string{exactRouteName, prefixRouteName} {
		_, err = e2ek8s.WaitForHTTPRouteAccepted(
			ctx,
			fixture.kubeClient.Client,
			fixture.namespaceName,
			routeName,
			fixture.gatewayName,
			nil,
		)
		require.NoError(t, err)

		_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
			ctx,
			fixture.kubeClient.Client,
			fixture.namespaceName,
			routeName,
			fixture.gatewayName,
			nil,
		)
		require.NoError(t, err)
	}
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Path-specific routes are accepted and resolved",
		slog.String("exactRoute", exactRouteName),
		slog.String("prefixRoute", prefixRouteName),
	)

	_, err = probe.WaitForEcho(ctx, fixture.probeClient, exactPath, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified exact path matches",
		slog.String("path", exactPath),
	)

	_, err = probe.WaitForEchoGone(ctx, fixture.probeClient, exactPathExtra, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified exact path does not match a nested path",
		slog.String("path", exactPathExtra),
	)

	_, err = probe.WaitForEcho(ctx, fixture.probeClient, prefixPath, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified prefix path matches its base path",
		slog.String("path", prefixPath),
	)

	_, err = probe.WaitForEcho(ctx, fixture.probeClient, prefixPathExtra, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified prefix path matches a nested path",
		slog.String("path", prefixPathExtra),
	)
}
