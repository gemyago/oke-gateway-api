package e2e

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPHeaderMatchingVariants(t *testing.T, sharedFixture *sharedHTTPRoutingFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)
	fixture := sharedFixture.Get(t, cfg)

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
		routeName   string
		response    string
		headerValue string
		headerMatch gatewayv1.HTTPHeaderMatch
	}{
		{
			name:        "exact",
			routeName:   "header-route-exact-" + suffix,
			response:    fixture.staticBackends[0].Response,
			headerValue: exactHeaderValue,
			headerMatch: gatewayv1.HTTPHeaderMatch{
				Type:  &exactType,
				Name:  headerName,
				Value: exactHeaderValue,
			},
		},
		{
			name:        "starts-with",
			routeName:   "header-route-prefix-" + suffix,
			response:    fixture.staticBackends[1].Response,
			headerValue: startsWithHeaderValue,
			headerMatch: gatewayv1.HTTPHeaderMatch{
				Type:  &regexType,
				Name:  headerName,
				Value: "^" + startsWithPrefix,
			},
		},
		{
			name:        "ends-with",
			routeName:   "header-route-suffix-" + suffix,
			response:    fixture.staticBackends[2].Response,
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

	logTestProgressContext(
		ctx,
		t,
		logger,
		"Using shared HTTP routing fixture",
		slog.String("namespace", fixture.namespaceName),
	)

	registerHTTPRouteCleanup(
		t,
		fixture.kubeClient.WithWatch,
		fixture.namespaceName,
		routes[0].routeName,
		routes[1].routeName,
		routes[2].routeName,
	)

	for i, route := range routes {
		backend := fixture.staticBackends[i]
		httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
			Namespace:     fixture.namespaceName,
			Name:          route.routeName,
			GatewayName:   fixture.gatewayName,
			ListenerName:  e2ek8s.DefaultHTTPListenerName,
			ServiceName:   backend.Name,
			ServicePort:   e2ek8s.DefaultEchoPort,
			HeaderMatches: []gatewayv1.HTTPHeaderMatch{route.headerMatch},
		})
		logger.InfoContext(
			ctx,
			"Creating header-specific HTTPRoute",
			slog.String("namespace", fixture.namespaceName),
			slog.String("httpRoute", route.routeName),
			slog.String("variant", route.name),
			slog.String("headerName", string(route.headerMatch.Name)),
			slog.String("headerValue", route.headerMatch.Value),
		)
		require.NoError(t, fixture.kubeClient.Create(ctx, httpRoute))

		_, err := e2ek8s.WaitForHTTPRouteAccepted(
			ctx,
			fixture.kubeClient.Client,
			fixture.namespaceName,
			route.routeName,
			fixture.gatewayName,
			nil,
		)
		require.NoError(t, err)

		_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
			ctx,
			fixture.kubeClient.Client,
			fixture.namespaceName,
			route.routeName,
			fixture.gatewayName,
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
			fixture.probeClient,
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
			fixture.probeClient,
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
