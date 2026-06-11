package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/jaswdr/faker/v2"
	appsv1 "k8s.io/api/apps/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPBackendEndpointChange(t *testing.T) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)
	fake := faker.New()
	suffix := randomDNSLabel(fake)
	gatewayName := "gateway-" + suffix
	gatewayConfigName := "gateway-config-" + suffix
	backendName := "echo-" + suffix
	routeName := "echo-route-" + suffix
	logger.InfoContext(ctx, "Loaded live HTTP backend endpoint change configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
		slog.Int("httpPort", cfg.HTTPPort),
	)

	logTestProgress(ctx, t, logger, "Creating Kubernetes and OCI clients")
	kubeClient, err := e2ek8s.NewClient(cfg.Kubernetes, nil)
	require.NoError(t, err)

	ociClient, err := e2eoci.NewLoadBalancerClient(cfg.OCI, nil)
	require.NoError(t, err)

	inspector := e2eoci.NewLoadBalancerCleaner(ociClient, slog.New(slog.DiscardHandler), nil)
	loadBalancer, err := inspector.Inspect(ctx, cfg.OCI.LoadBalancerID)
	require.NoError(t, err)

	probeClient, err := probe.NewClient(loadBalancer.PublicIP, cfg.HTTPPort, nil)
	require.NoError(t, err)

	logTestProgress(ctx, t, logger, "Starting controller and waiting for readiness")
	startHTTPController(t, cfg, logger)

	namespace, err := e2ek8s.CreateUniqueNamespace(ctx, kubeClient.Client, cfg.NamespacePrefix)
	require.NoError(t, err)
	logTestProgress(
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
	logTestProgress(
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
	logTestProgress(
		ctx,
		t,
		logger,
		"Echo backend is ready",
		slog.String("namespace", namespace.Name),
		slog.String("backend", backendName),
	)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
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
		slog.String("namespace", namespace.Name),
		slog.String("httpRoute", routeName),
		slog.String("probePath", probePath),
	)
	require.NoError(t, kubeClient.Create(ctx, httpRoute))

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
		slog.String("namespace", namespace.Name),
		slog.String("deployment", backendName),
	)
	require.NoError(t, setDeploymentReplicas(ctx, kubeClient.Client, namespace.Name, backendName, 0))

	_, err = e2ek8s.WaitForServiceEndpointsGone(ctx, kubeClient.Client, namespace.Name, backendName, nil)
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
		slog.String("namespace", namespace.Name),
		slog.String("deployment", backendName),
	)
	require.NoError(t, setDeploymentReplicas(ctx, kubeClient.Client, namespace.Name, backendName, 1))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
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
