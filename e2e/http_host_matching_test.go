package e2e

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPHostMatching(t *testing.T) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	backendAName := "host-a-" + suffix
	backendBName := "host-b-" + suffix
	httpRouteAName := "host-route-a-" + suffix
	httpRouteBName := "host-route-b-" + suffix
	hostA := gatewayv1.Hostname("a-" + suffix + ".example.test")
	hostB := gatewayv1.Hostname("b-" + suffix + ".example.test")
	hostMiss := "miss-" + randomDNSLabel(fake) + ".example.test"
	responseA := "backend-a-" + suffix
	responseB := "backend-b-" + suffix

	logger.InfoContext(ctx, "Loaded live HTTP host matching configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.String("hostA", string(hostA)),
		slog.String("hostB", string(hostB)),
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
	registerCleanup(t, &cleanupOnce, kubeClient.Client, namespace.Name, gatewayClassName)

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

	for _, backend := range []struct {
		name     string
		response string
	}{
		{name: backendAName, response: responseA},
		{name: backendBName, response: responseB},
	} {
		service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
			Namespace: namespace.Name,
			Name:      backend.name,
		})
		logger.InfoContext(
			ctx,
			"Creating backend service",
			slog.String("namespace", namespace.Name),
			slog.String("service", backend.name),
		)
		require.NoError(t, kubeClient.Create(ctx, service))

		deployment := e2ek8s.NewStaticHTTPDeployment(e2ek8s.StaticHTTPDeploymentOptions{
			Namespace:    namespace.Name,
			Name:         backend.name,
			ResponseText: backend.response,
		})
		logger.InfoContext(
			ctx,
			"Creating static backend deployment",
			slog.String("namespace", namespace.Name),
			slog.String("deployment", backend.name),
		)
		require.NoError(t, kubeClient.Create(ctx, deployment))

		_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backend.name, nil)
		require.NoError(t, err)

		_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backend.name, nil)
		require.NoError(t, err)
	}
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Both host-specific backends are ready",
		slog.String("backendA", backendAName),
		slog.String("backendB", backendBName),
	)

	httpRouteA := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         httpRouteAName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendAName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		Hostnames:    []gatewayv1.Hostname{hostA},
		PathPrefix:   "/",
	})
	logger.InfoContext(
		ctx,
		"Creating host-specific HTTPRoute",
		slog.String("namespace", namespace.Name),
		slog.String("httpRoute", httpRouteAName),
		slog.String("hostname", string(hostA)),
	)
	require.NoError(t, kubeClient.Create(ctx, httpRouteA))

	httpRouteB := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         httpRouteBName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendBName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		Hostnames:    []gatewayv1.Hostname{hostB},
		PathPrefix:   "/",
	})
	logger.InfoContext(
		ctx,
		"Creating host-specific HTTPRoute",
		slog.String("namespace", namespace.Name),
		slog.String("httpRoute", httpRouteBName),
		slog.String("hostname", string(hostB)),
	)
	require.NoError(t, kubeClient.Create(ctx, httpRouteB))

	for _, routeName := range []string{httpRouteAName, httpRouteBName} {
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
		"Host-specific routes are accepted and resolved",
		slog.String("routeA", httpRouteAName),
		slog.String("routeB", httpRouteBName),
	)

	assertHostRoutesToBody := func(host string, expectedBody string) {
		t.Helper()

		_, waitErr := probe.WaitForResponse(
			ctx,
			probeClient,
			"/",
			&probe.RequestOptions{Host: host},
			nil,
			fmt.Sprintf("wait for host %q to route to body %q", host, expectedBody),
			func(response *probe.Response) (bool, string) {
				switch {
				case response == nil:
					return false, "no response received"
				case response.StatusCode != http.StatusOK:
					return false, fmt.Sprintf("received status %d", response.StatusCode)
				case response.BodyString() != expectedBody:
					return false, fmt.Sprintf("received body %q", response.BodyString())
				default:
					return true, ""
				}
			},
		)
		require.NoError(t, waitErr)
	}

	assertHostRoutesToBody(string(hostA), responseA)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified host A routing",
		slog.String("hostname", string(hostA)),
		slog.String("expectedBody", responseA),
	)

	assertHostRoutesToBody(string(hostB), responseB)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified host B routing",
		slog.String("hostname", string(hostB)),
		slog.String("expectedBody", responseB),
	)

	_, err = probe.WaitForResponse(
		ctx,
		probeClient,
		"/",
		&probe.RequestOptions{Host: hostMiss},
		nil,
		fmt.Sprintf("wait for host %q to avoid host-specific routes", hostMiss),
		func(response *probe.Response) (bool, string) {
			if response == nil {
				return false, "no response received"
			}

			body := response.BodyString()
			if response.StatusCode == http.StatusOK && (body == responseA || body == responseB) {
				return false, fmt.Sprintf("unexpectedly matched backend body %q", body)
			}

			return true, ""
		},
	)
	require.NoError(t, err)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified non-matching host does not hit either backend",
		slog.String("hostname", hostMiss),
	)
}

func randomDNSLabel(fake faker.Faker) string {
	token := strings.ToLower(strings.ReplaceAll(fake.UUID().V4(), "-", ""))
	if len(token) > 12 {
		return token[:12]
	}

	return token
}
