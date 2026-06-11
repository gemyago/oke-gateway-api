package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jaswdr/faker/v2"
	appsv1 "k8s.io/api/apps/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPBackendEndpointChange(t *testing.T, live *liveFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)
	fake := faker.New()
	suffix := randomDNSLabel(fake)
	backendName := "echo-" + suffix
	routeName := "echo-route-" + suffix
	logger.InfoContext(ctx, "Loaded live HTTP backend endpoint change configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.Int("httpPort", cfg.HTTPPort),
	)

	gatewayFixture, err := createIsolatedHTTPGateway(ctx, t, live, cfg, suffix)
	require.NoError(t, err)
	kubeClient := live.kubeClient
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

	deployment := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespaceName,
		Name:      backendName,
	})
	logger.InfoContext(
		ctx,
		"Creating echo backend deployment",
		slog.String("namespace", namespaceName),
		slog.String("deployment", backendName),
	)
	require.NoError(t, kubeClient.Create(ctx, deployment))

	service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
		Namespace: namespaceName,
		Name:      backendName,
	})
	logger.InfoContext(
		ctx,
		"Creating echo backend service",
		slog.String("namespace", namespaceName),
		slog.String("service", backendName),
	)
	require.NoError(t, kubeClient.Create(ctx, service))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespaceName, backendName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespaceName, backendName, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Echo backend is ready",
		slog.String("namespace", namespaceName),
		slog.String("backend", backendName),
	)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespaceName,
		Name:         routeName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePath,
	})
	logger.InfoContext(
		ctx,
		"Creating HTTPRoute",
		slog.String("namespace", namespaceName),
		slog.String("httpRoute", routeName),
		slog.String("probePath", probePath),
	)
	require.NoError(t, kubeClient.Create(ctx, httpRoute))

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespaceName, routeName, gatewayName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespaceName,
		routeName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePath, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Initial backend is serving traffic",
		slog.String("probePath", probePath),
	)

	logTestProgress(
		ctx,
		t,
		logger,
		"Scaling backend deployment down to zero",
		slog.String("namespace", namespaceName),
		slog.String("deployment", backendName),
	)
	require.NoError(t, setDeploymentReplicas(ctx, kubeClient.Client, namespaceName, backendName, 0))

	_, err = e2ek8s.WaitForServiceEndpointsGone(ctx, kubeClient.Client, namespaceName, backendName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEchoGone(ctx, probeClient, probePath, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Traffic stopped after ready endpoints were removed",
		slog.String("probePath", probePath),
	)

	logTestProgress(
		ctx,
		t,
		logger,
		"Scaling backend deployment back up",
		slog.String("namespace", namespaceName),
		slog.String("deployment", backendName),
	)
	require.NoError(t, setDeploymentReplicas(ctx, kubeClient.Client, namespaceName, backendName, 1))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespaceName, backendName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespaceName, backendName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePath, nil)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"Traffic recovered after ready endpoints returned",
		slog.String("probePath", probePath),
	)
}

func setDeploymentReplicas(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	replicas int32,
) error {
	deployment := &appsv1.Deployment{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}
	if err := kubeClient.Get(ctx, key, deployment); err != nil {
		return fmt.Errorf("get Deployment %s/%s: %w", namespace, name, err)
	}

	patch := ctrlclient.MergeFrom(deployment.DeepCopy())
	deployment.Spec.Replicas = &replicas
	if err := kubeClient.Patch(ctx, deployment, patch); err != nil {
		return fmt.Errorf("set Deployment %s/%s replicas to %d: %w", namespace, name, replicas, err)
	}

	return nil
}
