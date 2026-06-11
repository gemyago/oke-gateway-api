package e2e

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPHostMatching(t *testing.T, fixture *httpRoutingFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	httpRouteAName := "host-route-a-" + suffix
	httpRouteBName := "host-route-b-" + suffix
	hostA := gatewayv1.Hostname("a-" + suffix + ".example.test")
	hostB := gatewayv1.Hostname("b-" + suffix + ".example.test")
	hostMiss := "miss-" + randomDNSLabel(fake) + ".example.test"
	backendA := fixture.staticBackends[0]
	backendB := fixture.staticBackends[1]

	logger.InfoContext(ctx, "Loaded live HTTP host matching configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.String("hostA", string(hostA)),
		slog.String("hostB", string(hostB)),
	)

	logTestProgress(
		ctx,
		t,
		logger,
		"Using shared HTTP routing fixture",
		slog.String("namespace", fixture.namespaceName),
		slog.String("backendA", backendA.Name),
		slog.String("backendB", backendB.Name),
	)

	registerHTTPRouteCleanup(t, fixture.kubeClient.WithWatch, fixture.namespaceName, httpRouteAName, httpRouteBName)

	httpRouteA := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    fixture.namespaceName,
		Name:         httpRouteAName,
		GatewayName:  fixture.gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendA.Name,
		ServicePort:  e2ek8s.DefaultEchoPort,
		Hostnames:    []gatewayv1.Hostname{hostA},
		PathPrefix:   "/",
	})
	logger.InfoContext(
		ctx,
		"Creating host-specific HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", httpRouteAName),
		slog.String("hostname", string(hostA)),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, httpRouteA))

	httpRouteB := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    fixture.namespaceName,
		Name:         httpRouteBName,
		GatewayName:  fixture.gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendB.Name,
		ServicePort:  e2ek8s.DefaultEchoPort,
		Hostnames:    []gatewayv1.Hostname{hostB},
		PathPrefix:   "/",
	})
	logger.InfoContext(
		ctx,
		"Creating host-specific HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", httpRouteBName),
		slog.String("hostname", string(hostB)),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, httpRouteB))

	for _, routeName := range []string{httpRouteAName, httpRouteBName} {
		_, err := e2ek8s.WaitForHTTPRouteAccepted(
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
	logTestProgress(
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
			fixture.probeClient,
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

	assertHostRoutesToBody(string(hostA), backendA.Response)
	logTestProgress(
		ctx,
		t,
		logger,
		"Verified host A routing",
		slog.String("hostname", string(hostA)),
		slog.String("expectedBody", backendA.Response),
	)

	assertHostRoutesToBody(string(hostB), backendB.Response)
	logTestProgress(
		ctx,
		t,
		logger,
		"Verified host B routing",
		slog.String("hostname", string(hostB)),
		slog.String("expectedBody", backendB.Response),
	)

	_, err := probe.WaitForResponse(
		ctx,
		fixture.probeClient,
		"/",
		&probe.RequestOptions{Host: hostMiss},
		nil,
		fmt.Sprintf("wait for host %q to avoid host-specific routes", hostMiss),
		func(response *probe.Response) (bool, string) {
			if response == nil {
				return false, "no response received"
			}

			body := response.BodyString()
			if response.StatusCode == http.StatusOK && (body == backendA.Response || body == backendB.Response) {
				return false, fmt.Sprintf("unexpectedly matched backend body %q", body)
			}

			return true, ""
		},
	)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Verified non-matching host does not hit either backend",
		slog.String("hostname", hostMiss),
	)
}
