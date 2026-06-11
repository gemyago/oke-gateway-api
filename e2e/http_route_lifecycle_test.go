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
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPRouteLifecycle(t *testing.T, sharedFixture *sharedHTTPRoutingFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)
	fixture := sharedFixture.Get(t, cfg)
	fake := faker.New()
	suffix := randomDNSLabel(fake)
	routeName := "route-lifecycle-" + suffix
	backend := fixture.staticBackends[0]
	logger.InfoContext(ctx, "Loaded live HTTP route lifecycle configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.Int("httpPort", cfg.HTTPPort),
	)

	logTestProgress(
		ctx,
		t,
		logger,
		"Using shared HTTP routing fixture",
		slog.String("namespace", fixture.namespaceName),
		slog.String("gatewayClass", fixture.gatewayClassName),
		slog.String("gateway", fixture.gatewayName),
	)

	registerHTTPRouteCleanup(t, fixture.kubeClient.WithWatch, fixture.namespaceName, routeName)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    fixture.namespaceName,
		Name:         routeName,
		GatewayName:  fixture.gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backend.Name,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   "/",
	})
	logger.InfoContext(
		ctx,
		"Creating HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", routeName),
		slog.String("backend", backend.Name),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, httpRoute))

	_, err := e2ek8s.WaitForHTTPRouteAccepted(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		routeName,
		fixture.gatewayName,
		nil,
	)
	require.NoError(t, err)

	resolvedRoute, err := e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		routeName,
		fixture.gatewayName,
		nil,
	)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"HTTPRoute accepted and resolved",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", routeName),
	)

	_, err = probe.WaitForResponse(
		ctx,
		fixture.probeClient,
		"/",
		nil,
		nil,
		fmt.Sprintf("wait for route %q to serve backend %q", routeName, backend.Name),
		func(response *probe.Response) (bool, string) {
			switch {
			case response == nil:
				return false, "no response received"
			case response.StatusCode != http.StatusOK:
				return false, fmt.Sprintf("received status %d", response.StatusCode)
			case response.BodyString() != backend.Response:
				return false, fmt.Sprintf("received body %q", response.BodyString())
			default:
				return true, ""
			}
		},
	)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Probe received backend response",
		slog.String("backend", backend.Name),
	)

	programmedPolicyRules, err := waitForHTTPRouteProgrammedPolicyRuleNames(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		routeName,
		nil,
	)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRules)
	logger.InfoContext(ctx, "Captured programmed routing policy rules", slog.Any("ruleNames", programmedPolicyRules))

	logTestProgress(
		ctx,
		t,
		logger,
		"Deleting HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", routeName),
	)
	err = fixture.kubeClient.Delete(ctx, &gatewayv1.HTTPRoute{
		ObjectMeta: resolvedRoute.ObjectMeta,
	})
	require.NoError(t, err)

	err = e2ek8s.WaitForHTTPRouteDeleted(ctx, fixture.kubeClient.Client, fixture.namespaceName, routeName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForResponse(
		ctx,
		fixture.probeClient,
		"/",
		nil,
		nil,
		fmt.Sprintf("wait for route %q to stop serving backend %q", routeName, backend.Name),
		func(response *probe.Response) (bool, string) {
			if response == nil {
				return false, "no response received"
			}

			if response.StatusCode == http.StatusOK && response.BodyString() == backend.Response {
				return false, "expected backend response is still being served"
			}

			return true, ""
		},
	)
	require.NoError(t, err)

	err = e2eoci.WaitForRoutingPolicyRuleNamesAbsent(
		ctx,
		fixture.ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRules,
		nil,
	)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"HTTP route lifecycle completed",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", routeName),
	)
}
