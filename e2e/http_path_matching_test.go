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

func testHTTPPathExactVsPrefix(t *testing.T) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

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

	logTestProgressContext(ctx, t, logger, "Creating Kubernetes and OCI clients")
	kubeClient, err := e2ek8s.NewClient(cfg.Kubernetes, nil)
	require.NoError(t, err)

	ociClient, err := e2eoci.NewLoadBalancerClient(cfg.OCI, nil)
	require.NoError(t, err)

	inspector := e2eoci.NewLoadBalancerCleaner(ociClient, slog.New(slog.DiscardHandler), nil)
	loadBalancer, err := inspector.Inspect(ctx, cfg.OCI.LoadBalancerID)
	require.NoError(t, err)

	probeClient, err := probe.NewClient(loadBalancer.PublicIP, cfg.HTTPPort, nil)
	require.NoError(t, err)

	logTestProgressContext(ctx, t, logger, "Starting controller and waiting for readiness")
	_ = startHTTPController(t, cfg, logger)

	namespace, err := e2ek8s.CreateUniqueNamespace(ctx, kubeClient.Client, cfg.NamespacePrefix)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Created isolated test namespace",
		slog.String("namespace", namespace.Name),
	)

	var cleanupOnce sync.Once
	gatewayClassName := uniqueGatewayClassName(cfg.GatewayClassName, namespace.Name)
	registerCleanup(t, &cleanupOnce, kubeClient.WithWatch, namespace.Name, gatewayClassName)

	gatewayClass := e2ek8s.NewGatewayClass(e2ek8s.GatewayClassOptions{
		Name: gatewayClassName,
	})
	logger.InfoContext(ctx, "Creating GatewayClass", slog.String("gatewayClass", gatewayClassName))
	require.NoError(t, kubeClient.Create(ctx, gatewayClass))

	_, err = e2ek8s.WaitForGatewayClassAccepted(ctx, kubeClient.Client, gatewayClassName, nil)
	require.NoError(t, err)

	gatewayConfig := e2ek8s.NewGatewayConfig(e2ek8s.GatewayConfigOptions{
		Namespace:      namespace.Name,
		Name:           gatewayConfigName,
		LoadBalancerID: cfg.OCI.LoadBalancerID,
	})
	logger.InfoContext(
		ctx,
		"Creating GatewayConfig",
		slog.String("namespace", namespace.Name),
		slog.String("gatewayConfig", gatewayConfigName),
	)
	require.NoError(t, kubeClient.Create(ctx, gatewayConfig))

	gateway := e2ek8s.NewHTTPGateway(e2ek8s.HTTPGatewayOptions{
		Namespace:         namespace.Name,
		Name:              gatewayName,
		GatewayClassName:  gatewayClassName,
		GatewayConfigName: gatewayConfigName,
		Port:              gatewayv1.PortNumber(cfg.HTTPPort),
	})
	logger.InfoContext(
		ctx,
		"Creating Gateway",
		slog.String("namespace", namespace.Name),
		slog.String("gateway", gatewayName),
	)
	require.NoError(t, kubeClient.Create(ctx, gateway))

	_, err = e2ek8s.WaitForGatewayAccepted(ctx, kubeClient.Client, namespace.Name, gatewayName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForGatewayProgrammed(ctx, kubeClient.Client, namespace.Name, gatewayName, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Gateway accepted and programmed",
		slog.String("namespace", namespace.Name),
		slog.String("gateway", gatewayName),
	)

	deployment := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespace.Name,
		Name:      backendName,
	})
	logger.InfoContext(
		ctx,
		"Creating echo backend deployment",
		slog.String("namespace", namespace.Name),
		slog.String("deployment", backendName),
	)
	require.NoError(t, kubeClient.Create(ctx, deployment))

	service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
		Namespace: namespace.Name,
		Name:      backendName,
	})
	logger.InfoContext(
		ctx,
		"Creating echo backend service",
		slog.String("namespace", namespace.Name),
		slog.String("service", backendName),
	)
	require.NoError(t, kubeClient.Create(ctx, service))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Echo backend is ready",
		slog.String("namespace", namespace.Name),
		slog.String("backend", backendName),
	)

	exactRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         exactRouteName,
		GatewayName:  gatewayName,
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
		slog.String("namespace", namespace.Name),
		slog.String("httpRoute", exactRouteName),
		slog.String("path", exactPath),
	)
	require.NoError(t, kubeClient.Create(ctx, exactRoute))

	prefixRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         prefixRouteName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   prefixPath,
	})
	logger.InfoContext(
		ctx,
		"Creating prefix-path HTTPRoute",
		slog.String("namespace", namespace.Name),
		slog.String("httpRoute", prefixRouteName),
		slog.String("path", prefixPath),
	)
	require.NoError(t, kubeClient.Create(ctx, prefixRoute))

	for _, routeName := range []string{exactRouteName, prefixRouteName} {
		_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespace.Name, routeName, gatewayName, nil)
		require.NoError(t, err)

		_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
			ctx,
			kubeClient.Client,
			namespace.Name,
			routeName,
			gatewayName,
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

	_, err = probe.WaitForEcho(ctx, probeClient, exactPath, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified exact path matches",
		slog.String("path", exactPath),
	)

	_, err = probe.WaitForEchoGone(ctx, probeClient, exactPathExtra, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified exact path does not match a nested path",
		slog.String("path", exactPathExtra),
	)

	_, err = probe.WaitForEcho(ctx, probeClient, prefixPath, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified prefix path matches its base path",
		slog.String("path", prefixPath),
	)

	_, err = probe.WaitForEcho(ctx, probeClient, prefixPathExtra, nil)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified prefix path matches a nested path",
		slog.String("path", prefixPathExtra),
	)
}
