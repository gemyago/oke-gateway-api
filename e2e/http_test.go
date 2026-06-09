package e2e

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
)

const (
	httpRouteProgrammedPolicyRulesAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programmed-lb-policy-rules"
	liveHTTPTestTimeout                      = 20 * time.Minute
	cleanupTimeout                           = 2 * time.Minute
	controllerStartupTimeout                 = 2 * time.Minute
	probePath                                = "/echo"
	gatewayName                              = "gateway"
	gatewayConfigName                        = "gateway-config"
	backendName                              = "echo"
	httpRouteName                            = "echo-route"
)

func TestHTTP(t *testing.T) {
	t.Run("Startup", testHTTPStartup)
	t.Run("RouteLifecycle", testHTTPRouteLifecycle)
	t.Run("MultiRouteIsolation", testHTTPMultiRouteIsolation)
}

func requireLiveHTTPConfig(t *testing.T) *config.Config {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping live HTTP e2e in short mode")
	}

	cfg, err := config.LoadFromEnv(nil)
	require.NoError(
		t,
		err,
		"set the required live e2e inputs in e2e/.envrc.local before running `direnv exec . make -C e2e run-e2e-tests`",
	)

	return cfg
}

func newLiveHTTPContext(t *testing.T) (context.Context, *config.Config) {
	t.Helper()

	cfg := requireLiveHTTPConfig(t)

	ctx, cancel := context.WithTimeout(t.Context(), liveHTTPTestTimeout)
	t.Cleanup(cancel)

	return ctx, cfg
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

func waitForHTTPRouteProgrammedPolicyRuleNames(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	opts *e2ek8s.WaitOptions,
) ([]string, error) {
	pollInterval := 2 * time.Second
	if opts != nil && opts.PollInterval > 0 {
		pollInterval = opts.PollInterval
	}

	resource := &gatewayv1.HTTPRoute{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}
	var lastErr error

	for {
		if err := kubeClient.Get(ctx, key, resource); err != nil {
			if apierrors.IsNotFound(err) {
				lastErr = err
			} else {
				return nil, err
			}
		} else {
			ruleNames, ruleErr := programmedPolicyRuleNames(resource)
			if ruleErr == nil {
				return ruleNames, nil
			}

			lastErr = ruleErr
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return nil, fmt.Errorf(
					"wait for HTTPRoute %s/%s programmed policy rule names: %w",
					namespace,
					name,
					lastErr,
				)
			}

			return nil, fmt.Errorf(
				"wait for HTTPRoute %s/%s programmed policy rule names: %w",
				namespace,
				name,
				ctx.Err(),
			)
		case <-timer.C:
		}
	}
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
