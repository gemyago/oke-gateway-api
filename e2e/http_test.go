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
	"k8s.io/apimachinery/pkg/watch"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
)

const (
	httpRouteProgrammedPolicyRulesAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programmed-lb-policy-rules"
	liveHTTPTestTimeout                      = 20 * time.Minute
	cleanupTimeout                           = 4 * time.Minute
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
	t.Run("CertificateLifecycle", testHTTPCertificateLifecycle)
	t.Run("MultiRouteIsolation", testHTTPMultiRouteIsolation)
	t.Run("BackendEndpointChange", testHTTPBackendEndpointChange)
	t.Run("HostMatching", testHTTPHostMatching)
	t.Run("HeaderMatchingVariants", testHTTPHeaderMatchingVariants)
	t.Run("PathExactVsPrefix", testHTTPPathExactVsPrefix)
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
	kubeClient ctrlclient.WithWatch,
	namespace string,
	name string,
	opts *e2ek8s.WaitOptions,
) ([]string, error) {
	_ = opts

	resource := &gatewayv1.HTTPRoute{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}
	var lastErr error
	description := fmt.Sprintf("wait for HTTPRoute %s/%s programmed policy rule names", namespace, name)
	progressLogger := diag.NewWaitProgressLogger(nil, description, 0)

	check := func() ([]string, bool, error) {
		if err := kubeClient.Get(ctx, key, resource); err != nil {
			if apierrors.IsNotFound(err) {
				lastErr = err
				return nil, false, nil
			}

			return nil, false, err
		}

		ruleNames, ruleErr := programmedPolicyRuleNames(resource)
		if ruleErr == nil {
			return ruleNames, true, nil
		}

		lastErr = ruleErr
		return nil, false, nil
	}

	ruleNames, done, err := check()
	if err != nil {
		return nil, err
	}
	if done {
		return ruleNames, nil
	}
	progressLogger.Log(ctx, errorString(lastErr))

	watcher, err := kubeClient.Watch(
		ctx,
		&gatewayv1.HTTPRouteList{},
		ctrlclient.InNamespace(namespace),
		ctrlclient.MatchingFields{"metadata.name": name},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"wait for HTTPRoute %s/%s programmed policy rule names: start watch: %w",
			namespace,
			name,
			err,
		)
	}
	defer func() {
		watcher.Stop()
	}()

	ruleNames, done, err = check()
	if err != nil {
		return nil, err
	}
	if done {
		return ruleNames, nil
	}
	progressLogger.Log(ctx, errorString(lastErr))

	progressTicker := time.NewTicker(diag.DefaultWaitProgressLogInterval)
	defer progressTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf(
					"%s: %w",
					description,
					lastErr,
				)
			}

			return nil, fmt.Errorf(
				"%s: %w",
				description,
				ctx.Err(),
			)
		case <-progressTicker.C:
			progressLogger.Log(ctx, errorString(lastErr))
		case event, ok := <-watcher.ResultChan():
			if !ok {
				watcher.Stop()
				watcher, err = kubeClient.Watch(
					ctx,
					&gatewayv1.HTTPRouteList{},
					ctrlclient.InNamespace(namespace),
					ctrlclient.MatchingFields{"metadata.name": name},
				)
				if err != nil {
					return nil, fmt.Errorf(
						"wait for HTTPRoute %s/%s programmed policy rule names: restart watch: %w",
						namespace,
						name,
						err,
					)
				}

				ruleNames, done, err = check()
				if err != nil {
					return nil, err
				}
				if done {
					return ruleNames, nil
				}
				progressLogger.Log(ctx, errorString(lastErr))

				continue
			}

			if event.Type == watch.Error {
				statusErr := apierrors.FromObject(event.Object)
				if statusErr != nil {
					return nil, fmt.Errorf(
						"wait for HTTPRoute %s/%s programmed policy rule names: watch error: %w",
						namespace,
						name,
						statusErr,
					)
				}

				return nil, fmt.Errorf(
					"wait for HTTPRoute %s/%s programmed policy rule names: watch returned an unknown error event",
					namespace,
					name,
				)
			}

			object, ok := event.Object.(ctrlclient.Object)
			if !ok || ctrlclient.ObjectKeyFromObject(object) != key {
				continue
			}

			ruleNames, done, err = check()
			if err != nil {
				return nil, err
			}
			if done {
				return ruleNames, nil
			}
			progressLogger.Log(ctx, errorString(lastErr))
		}
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
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
	kubeClient ctrlclient.WithWatch,
	namespaceName string,
	gatewayClassName string,
) {
	t.Helper()

	t.Cleanup(func() {
		cleanupOnce.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()

			if err := deleteNamespaceHTTPRoutes(cleanupCtx, kubeClient, namespaceName); err != nil {
				t.Errorf("delete namespace %q HTTPRoutes: %v", namespaceName, err)
			}

			if err := deleteNamespace(cleanupCtx, kubeClient, namespaceName); err != nil {
				t.Errorf("delete namespace %q: %v", namespaceName, err)
			}

			if strings.TrimSpace(namespaceName) != "" {
				if err := e2ek8s.WaitForNamespacesDeleted(
					cleanupCtx,
					kubeClient,
					[]string{namespaceName},
					nil,
				); err != nil {
					t.Errorf("wait for namespace %q deletion: %v", namespaceName, err)
				}
			}

			if err := deleteGatewayClass(cleanupCtx, kubeClient, gatewayClassName); err != nil {
				t.Errorf("delete GatewayClass %q: %v", gatewayClassName, err)
			}
		})
	})
}

func deleteNamespaceHTTPRoutes(
	ctx context.Context,
	kubeClient ctrlclient.WithWatch,
	namespaceName string,
) error {
	if strings.TrimSpace(namespaceName) == "" {
		return nil
	}

	routeList := &gatewayv1.HTTPRouteList{}
	if err := kubeClient.List(ctx, routeList, ctrlclient.InNamespace(namespaceName)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("list HTTPRoutes in namespace %q: %w", namespaceName, err)
	}

	for i := range routeList.Items {
		route := &routeList.Items[i]
		if err := kubeClient.Delete(ctx, route); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete HTTPRoute %s/%s: %w", route.Namespace, route.Name, err)
		}
	}

	for _, route := range routeList.Items {
		if err := e2ek8s.WaitForHTTPRouteDeleted(ctx, kubeClient, route.Namespace, route.Name, nil); err != nil {
			return fmt.Errorf("wait for HTTPRoute %s/%s deletion: %w", route.Namespace, route.Name, err)
		}
	}

	return nil
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
