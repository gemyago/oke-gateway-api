package e2e

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPHeaderMatchingVariants(t *testing.T) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	headerName := gatewayv1.HTTPHeaderName("X-Route-" + suffix)
	exactType := gatewayv1.HeaderMatchExact
	regexType := gatewayv1.HeaderMatchRegularExpression
	exactHeaderValue := "exact-" + suffix + "-value"
	startsWithPrefix := "prefix-" + suffix
	startsWithHeaderValue := startsWithPrefix + "-tail"
	endsWithSuffix := "suffix-" + suffix
	endsWithHeaderValue := "head-" + endsWithSuffix
	missHeaderValue := "miss-" + suffix

	routes := []struct {
		name        string
		backendName string
		routeName   string
		response    string
		headerValue string
		headerMatch gatewayv1.HTTPHeaderMatch
	}{
		{
			name:        "exact",
			backendName: "header-exact-" + suffix,
			routeName:   "header-route-exact-" + suffix,
			response:    "backend-exact-" + suffix,
			headerValue: exactHeaderValue,
			headerMatch: gatewayv1.HTTPHeaderMatch{
				Type:  &exactType,
				Name:  headerName,
				Value: exactHeaderValue,
			},
		},
		{
			name:        "starts-with",
			backendName: "header-prefix-" + suffix,
			routeName:   "header-route-prefix-" + suffix,
			response:    "backend-prefix-" + suffix,
			headerValue: startsWithHeaderValue,
			headerMatch: gatewayv1.HTTPHeaderMatch{
				Type:  &regexType,
				Name:  headerName,
				Value: "^" + startsWithPrefix,
			},
		},
		{
			name:        "ends-with",
			backendName: "header-suffix-" + suffix,
			routeName:   "header-route-suffix-" + suffix,
			response:    "backend-suffix-" + suffix,
			headerValue: endsWithHeaderValue,
			headerMatch: gatewayv1.HTTPHeaderMatch{
				Type:  &regexType,
				Name:  headerName,
				Value: endsWithSuffix + "$",
			},
		},
	}

	logger.InfoContext(ctx, "Loaded live HTTP header matching configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.String("headerName", string(headerName)),
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

	for _, route := range routes {
		service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
			Namespace: namespace.Name,
			Name:      route.backendName,
		})
		logger.InfoContext(
			ctx,
			"Creating backend service",
			slog.String("namespace", namespace.Name),
			slog.String("service", route.backendName),
			slog.String("variant", route.name),
		)
		require.NoError(t, kubeClient.Create(ctx, service))

		deployment := e2ek8s.NewStaticHTTPDeployment(e2ek8s.StaticHTTPDeploymentOptions{
			Namespace:    namespace.Name,
			Name:         route.backendName,
			ResponseText: route.response,
		})
		logger.InfoContext(
			ctx,
			"Creating static backend deployment",
			slog.String("namespace", namespace.Name),
			slog.String("deployment", route.backendName),
			slog.String("variant", route.name),
		)
		require.NoError(t, kubeClient.Create(ctx, deployment))

		_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, route.backendName, nil)
		require.NoError(t, err)

		_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, route.backendName, nil)
		require.NoError(t, err)
	}
	logTestProgressContext(
		ctx,
		t,
		logger,
		"All header-matching backends are ready",
		slog.Int("backendCount", len(routes)),
	)

	for _, route := range routes {
		httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
			Namespace:     namespace.Name,
			Name:          route.routeName,
			GatewayName:   gatewayName,
			ListenerName:  e2ek8s.DefaultHTTPListenerName,
			ServiceName:   route.backendName,
			ServicePort:   e2ek8s.DefaultEchoPort,
			HeaderMatches: []gatewayv1.HTTPHeaderMatch{route.headerMatch},
		})
		logger.InfoContext(
			ctx,
			"Creating header-specific HTTPRoute",
			slog.String("namespace", namespace.Name),
			slog.String("httpRoute", route.routeName),
			slog.String("variant", route.name),
			slog.String("headerName", string(route.headerMatch.Name)),
			slog.String("headerValue", route.headerMatch.Value),
		)
		require.NoError(t, kubeClient.Create(ctx, httpRoute))

		_, err = e2ek8s.WaitForHTTPRouteAccepted(
			ctx,
			kubeClient.Client,
			namespace.Name,
			route.routeName,
			gatewayName,
			nil,
		)
		require.NoError(t, err)

		_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
			ctx,
			kubeClient.Client,
			namespace.Name,
			route.routeName,
			gatewayName,
			nil,
		)
		require.NoError(t, err)
	}
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Header-specific routes are accepted and resolved",
		slog.Int("routeCount", len(routes)),
	)

	knownBodies := make([]string, 0, len(routes))
	for _, route := range routes {
		knownBodies = append(knownBodies, route.response)
	}

	assertHeaderRoutesToBody := func(headerValue string, expectedBody string) {
		t.Helper()

		_, waitErr := probe.WaitForResponse(
			ctx,
			probeClient,
			"/",
			&probe.RequestOptions{
				Headers: http.Header{
					string(headerName): []string{headerValue},
				},
			},
			nil,
			fmt.Sprintf("wait for header %q to route to body %q", headerValue, expectedBody),
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

	assertDoesNotRouteToKnownBackend := func(requestHeaders http.Header, reason string) {
		t.Helper()

		_, waitErr := probe.WaitForResponse(
			ctx,
			probeClient,
			"/",
			&probe.RequestOptions{
				Headers: requestHeaders,
			},
			nil,
			reason,
			func(response *probe.Response) (bool, string) {
				if response == nil {
					return false, "no response received"
				}

				body := response.BodyString()
				if response.StatusCode == http.StatusOK && slices.Contains(knownBodies, body) {
					return false, fmt.Sprintf("unexpectedly matched backend body %q", body)
				}

				return true, ""
			},
		)
		require.NoError(t, waitErr)
	}

	for _, route := range routes {
		assertHeaderRoutesToBody(route.headerValue, route.response)
		logTestProgressContext(
			ctx,
			t,
			logger,
			"Verified header-based routing",
			slog.String("variant", route.name),
			slog.String("headerValue", route.headerValue),
			slog.String("expectedBody", route.response),
		)
	}

	assertDoesNotRouteToKnownBackend(
		nil,
		fmt.Sprintf("wait for missing header %q to avoid header-specific routes", headerName),
	)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified missing header does not hit any backend",
		slog.String("headerName", string(headerName)),
	)

	assertDoesNotRouteToKnownBackend(
		http.Header{
			string(headerName): []string{missHeaderValue},
		},
		fmt.Sprintf("wait for non-matching header %q=%q to avoid header-specific routes", headerName, missHeaderValue),
	)
	logTestProgressContext(
		ctx,
		t,
		logger,
		"Verified non-matching header value does not hit any backend",
		slog.String("headerName", string(headerName)),
		slog.String("headerValue", missHeaderValue),
	)
}
