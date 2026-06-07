package e2e

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/controllerproc"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

const (
	httpRouteProgrammedPolicyRulesAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programmed-lb-policy-rules"
	liveHTTPTestTimeout                      = 20 * time.Minute
	cleanupTimeout                           = 2 * time.Minute
	probePath                                = "/echo"
	gatewayName                              = "gateway"
	gatewayConfigName                        = "gateway-config"
	backendName                              = "echo"
	httpRouteName                            = "echo-route"
)

func TestHTTP(t *testing.T) {
	cfg := requireLiveHTTPConfig(t)

	ctx, cancel := context.WithTimeout(t.Context(), liveHTTPTestTimeout)
	t.Cleanup(cancel)

	kubeClient, err := e2ek8s.NewClient(cfg.Kubernetes, nil)
	require.NoError(t, err)

	ociClient, err := e2eoci.NewLoadBalancerClient(cfg.OCI, nil)
	require.NoError(t, err)

	inspector := e2eoci.NewLoadBalancerCleaner(ociClient, slog.New(slog.DiscardHandler), nil)
	loadBalancer, err := inspector.Inspect(ctx, cfg.OCI.LoadBalancerID)
	require.NoError(t, err)

	probeClient, err := probe.NewClient(loadBalancer.PublicIP, cfg.HTTPPort, nil)
	require.NoError(t, err)

	_, err = controllerproc.Start(t, *cfg, nil)
	require.NoError(t, err)

	namespace, err := e2ek8s.CreateUniqueNamespace(ctx, kubeClient.Client, cfg.NamespacePrefix)
	require.NoError(t, err)

	var cleanupOnce sync.Once
	gatewayClassName := uniqueGatewayClassName(cfg.GatewayClassName, namespace.Name)
	registerCleanup(t, &cleanupOnce, kubeClient.Client, namespace.Name, gatewayClassName)

	gatewayClass := e2ek8s.NewGatewayClass(e2ek8s.GatewayClassOptions{
		Name: gatewayClassName,
	})
	require.NoError(t, kubeClient.Create(ctx, gatewayClass))

	_, err = e2ek8s.WaitForGatewayClassAccepted(ctx, kubeClient.Client, gatewayClassName, nil)
	require.NoError(t, err)

	gatewayConfig := e2ek8s.NewGatewayConfig(e2ek8s.GatewayConfigOptions{
		Namespace:      namespace.Name,
		Name:           gatewayConfigName,
		LoadBalancerID: cfg.OCI.LoadBalancerID,
	})
	require.NoError(t, kubeClient.Create(ctx, gatewayConfig))

	gateway := e2ek8s.NewHTTPGateway(e2ek8s.HTTPGatewayOptions{
		Namespace:         namespace.Name,
		Name:              gatewayName,
		GatewayClassName:  gatewayClassName,
		GatewayConfigName: gatewayConfigName,
		Port:              gatewayv1.PortNumber(cfg.HTTPPort),
	})
	require.NoError(t, kubeClient.Create(ctx, gateway))

	_, err = e2ek8s.WaitForGatewayAccepted(ctx, kubeClient.Client, namespace.Name, gatewayName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForGatewayProgrammed(ctx, kubeClient.Client, namespace.Name, gatewayName, nil)
	require.NoError(t, err)

	deployment := e2ek8s.NewEchoDeployment(e2ek8s.EchoDeploymentOptions{
		Namespace: namespace.Name,
		Name:      backendName,
	})
	require.NoError(t, kubeClient.Create(ctx, deployment))

	service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
		Namespace: namespace.Name,
		Name:      backendName,
	})
	require.NoError(t, kubeClient.Create(ctx, service))

	_, err = e2ek8s.WaitForDeploymentReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)

	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, kubeClient.Client, namespace.Name, backendName, nil)
	require.NoError(t, err)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    namespace.Name,
		Name:         httpRouteName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPListenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   probePath,
	})
	require.NoError(t, kubeClient.Create(ctx, httpRoute))

	_, err = e2ek8s.WaitForHTTPRouteAccepted(ctx, kubeClient.Client, namespace.Name, httpRouteName, gatewayName, nil)
	require.NoError(t, err)

	resolvedRoute, err := e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		kubeClient.Client,
		namespace.Name,
		httpRouteName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	_, err = probe.WaitForEcho(ctx, probeClient, probePath, nil)
	require.NoError(t, err)

	programmedPolicyRules, err := programmedPolicyRuleNames(resolvedRoute)
	require.NoError(t, err)
	require.NotEmpty(t, programmedPolicyRules)

	err = kubeClient.Delete(ctx, &gatewayv1.HTTPRoute{
		ObjectMeta: resolvedRoute.ObjectMeta,
	})
	require.NoError(t, err)

	err = e2ek8s.WaitForHTTPRouteDeleted(ctx, kubeClient.Client, namespace.Name, httpRouteName, nil)
	require.NoError(t, err)

	_, err = probe.WaitForEchoGone(ctx, probeClient, probePath, nil)
	require.NoError(t, err)

	err = e2eoci.WaitForRoutingPolicyRuleNamesAbsent(
		ctx,
		ociClient,
		cfg.OCI.LoadBalancerID,
		string(e2ek8s.DefaultHTTPListenerName),
		programmedPolicyRules,
		nil,
	)
	require.NoError(t, err)
}

func requireLiveHTTPConfig(t *testing.T) *config.Config {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping live HTTP e2e in short mode")
	}

	missing := missingLiveHTTPInputs()
	if len(missing) > 0 {
		t.Skipf(
			"live HTTP e2e requires %s; set them in e2e/.envrc.local and rerun `direnv exec . make -C e2e test`",
			strings.Join(missing, ", "),
		)
	}

	cfg, err := config.LoadFromEnv(nil)
	require.NoError(t, err)

	return cfg
}

func missingLiveHTTPInputs() []string {
	required := []string{
		"KUBECONFIG",
		"OKE_E2E_LOAD_BALANCER_ID",
	}

	missing := make([]string, 0, len(required))
	for _, key := range required {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}

	return missing
}

func programmedPolicyRuleNames(route *gatewayv1.HTTPRoute) ([]string, error) {
	if route == nil {
		return nil, errors.New("route is required")
	}

	annotationValue := strings.TrimSpace(route.Annotations[httpRouteProgrammedPolicyRulesAnnotation])
	if annotationValue == "" {
		return nil, fmt.Errorf(
			"route %s/%s is missing annotation %q",
			route.Namespace,
			route.Name,
			httpRouteProgrammedPolicyRulesAnnotation,
		)
	}

	parts := strings.Split(annotationValue, ",")
	ruleNames := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		ruleNames = append(ruleNames, part)
	}

	if len(ruleNames) == 0 {
		return nil, fmt.Errorf(
			"route %s/%s annotation %q did not contain any rule names",
			route.Namespace,
			route.Name,
			httpRouteProgrammedPolicyRulesAnnotation,
		)
	}

	return ruleNames, nil
}

func uniqueGatewayClassName(prefix string, namespaceName string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "oke-gateway-api-e2e"
	}

	namespaceName = strings.TrimSpace(namespaceName)
	namespaceName = strings.TrimPrefix(namespaceName, prefix)
	namespaceName = strings.Trim(namespaceName, "-")

	if namespaceName == "" {
		return prefix
	}

	return prefix + "-" + namespaceName
}

func registerCleanup(
	t *testing.T,
	cleanupOnce *sync.Once,
	kubeClient ctrlclient.Client,
	namespaceName string,
	gatewayClassName string,
) {
	t.Helper()

	t.Cleanup(func() {
		cleanupOnce.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()

			if err := deleteNamespace(cleanupCtx, kubeClient, namespaceName); err != nil {
				t.Errorf("delete namespace %q: %v", namespaceName, err)
			}

			if err := deleteGatewayClass(cleanupCtx, kubeClient, gatewayClassName); err != nil {
				t.Errorf("delete GatewayClass %q: %v", gatewayClassName, err)
			}
		})
	})
}

func deleteNamespace(ctx context.Context, kubeClient ctrlclient.Client, namespaceName string) error {
	if strings.TrimSpace(namespaceName) == "" {
		return nil
	}

	return deleteObject(ctx, kubeClient, ctrlclient.ObjectKey{Name: namespaceName}, &corev1.Namespace{})
}

func deleteGatewayClass(ctx context.Context, kubeClient ctrlclient.Client, gatewayClassName string) error {
	if strings.TrimSpace(gatewayClassName) == "" {
		return nil
	}

	return deleteObject(ctx, kubeClient, ctrlclient.ObjectKey{Name: gatewayClassName}, &gatewayv1.GatewayClass{})
}

func deleteObject(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	key ctrlclient.ObjectKey,
	object ctrlclient.Object,
) error {
	if err := kubeClient.Get(ctx, key, object); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return err
	}

	if err := kubeClient.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}
