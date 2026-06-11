package e2e

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/watch"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/controllerproc"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

const (
	httpRouteProgrammedPolicyRulesAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programmed-lb-policy-rules"
	liveHTTPTestTimeout                      = 20 * time.Minute
	cleanupTimeout                           = 4 * time.Minute
	controllerStartupTimeout                 = 2 * time.Minute
	probePath                                = "/echo"
)

func TestHTTP(t *testing.T) {
	routingFixture := newSharedHTTPRoutingFixture(t)

	t.Run("RouteLifecycle", func(t *testing.T) {
		testHTTPRouteLifecycle(t, routingFixture)
	})
	t.Run("CertificateLifecycle", testHTTPCertificateLifecycle)
	t.Run("MultiRouteIsolation", testHTTPMultiRouteIsolation)
	t.Run("BackendEndpointChange", testHTTPBackendEndpointChange)
	t.Run("HostMatching", func(t *testing.T) {
		testHTTPHostMatching(t, routingFixture)
	})
	t.Run("HeaderMatchingVariants", func(t *testing.T) {
		testHTTPHeaderMatchingVariants(t, routingFixture)
	})
	t.Run("PathExactVsPrefix", func(t *testing.T) {
		testHTTPPathExactVsPrefix(t, routingFixture)
	})
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

func startHTTPController(t *testing.T, cfg *config.Config, logger *slog.Logger) {
	t.Helper()

	logTestProgress(t.Context(), t, logger, "Starting controller process")
	proc, err := controllerproc.Start(newSlogTestLogSink(t, logger), *cfg, nil)
	require.NoError(t, err)

	if proc.Skipped() {
		logTestProgress(t.Context(), t, logger, "Controller startup skipped by configuration")
		return
	}

	startupCtx, cancel := context.WithTimeout(t.Context(), controllerStartupTimeout)
	defer cancel()

	logTestProgress(
		startupCtx,
		t,
		logger,
		"Waiting for controller startup log",
		slog.String("fragment", "Starting controller manager"),
	)
	require.NoError(t, proc.WaitForLog(startupCtx, "Starting controller manager"))
	logTestProgress(startupCtx, t, logger, "Observed controller startup log")
}

type sharedHTTPRoutingFixture struct {
	parentT *testing.T

	mu       sync.Mutex
	fixture  *httpRoutingFixture
	setupErr error
}

type httpRoutingFixture struct {
	kubeClient       *e2ek8s.RuntimeClient
	ociClient        *loadbalancer.LoadBalancerClient
	probeClient      *probe.Client
	namespaceName    string
	gatewayClassName string
	gatewayName      string
	staticBackends   []httpStaticBackend
}

type httpStaticBackend struct {
	Name     string
	Response string
}

func newSharedHTTPRoutingFixture(parentT *testing.T) *sharedHTTPRoutingFixture {
	parentT.Helper()

	return &sharedHTTPRoutingFixture{
		parentT: parentT,
	}
}

func (f *sharedHTTPRoutingFixture) Get(t *testing.T, cfg *config.Config) *httpRoutingFixture {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fixture != nil {
		return f.fixture
	}

	if f.setupErr != nil {
		require.NoError(t, f.setupErr)
		return nil
	}

	fixture, err := createHTTPRoutingFixture(f.parentT, cfg)
	f.setupErr = err
	require.NoError(t, err)

	f.fixture = fixture
	return fixture
}

func createHTTPRoutingFixture(parentT *testing.T, cfg *config.Config) (*httpRoutingFixture, error) {
	logger := startTestLogger(parentT).With(slog.String("fixture", "shared-routing"))
	fake := faker.New()
	suffix := randomDNSLabel(fake)
	gatewayName := "gateway-" + suffix
	gatewayConfigName := "gateway-config-" + suffix

	setupCtx, cancel := context.WithTimeout(context.Background(), liveHTTPTestTimeout)
	defer cancel()

	logger.InfoContext(setupCtx, "Creating shared routing fixture clients")
	kubeClient, err := e2ek8s.NewClient(cfg.Kubernetes, nil)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client for shared routing fixture: %w", err)
	}

	ociClient, err := e2eoci.NewLoadBalancerClient(cfg.OCI, nil)
	if err != nil {
		return nil, fmt.Errorf("create OCI client for shared routing fixture: %w", err)
	}

	inspector := e2eoci.NewLoadBalancerCleaner(ociClient, slog.New(slog.DiscardHandler), nil)
	loadBalancer, err := inspector.Inspect(setupCtx, cfg.OCI.LoadBalancerID)
	if err != nil {
		return nil, fmt.Errorf("inspect OCI load balancer for shared routing fixture: %w", err)
	}

	probeClient, err := probe.NewClient(loadBalancer.PublicIP, cfg.HTTPPort, nil)
	if err != nil {
		return nil, fmt.Errorf("create probe client for shared routing fixture: %w", err)
	}

	logger.InfoContext(setupCtx, "Starting shared controller process")
	startHTTPController(parentT, cfg, logger)

	namespace, err := e2ek8s.CreateUniqueNamespace(setupCtx, kubeClient.Client, cfg.NamespacePrefix)
	if err != nil {
		return nil, fmt.Errorf("create shared routing fixture namespace: %w", err)
	}

	gatewayClassName := uniqueGatewayClassName(cfg.GatewayClassName, namespace.Name)
	var cleanupOnce sync.Once
	registerCleanup(parentT, &cleanupOnce, kubeClient.WithWatch, namespace.Name, gatewayClassName)

	logger.InfoContext(
		setupCtx,
		"Created shared routing fixture namespace",
		slog.String("namespace", namespace.Name),
		slog.String("gatewayClass", gatewayClassName),
	)

	gatewayClass := e2ek8s.NewGatewayClass(e2ek8s.GatewayClassOptions{
		Name: gatewayClassName,
	})
	err = kubeClient.Create(setupCtx, gatewayClass)
	if err != nil {
		return nil, fmt.Errorf("create shared routing fixture GatewayClass %q: %w", gatewayClassName, err)
	}

	logTestProgress(setupCtx, parentT, logger, "Waiting for shared routing fixture GatewayClass acceptance")
	_, err = e2ek8s.WaitForGatewayClassAccepted(setupCtx, kubeClient.Client, gatewayClassName, nil)
	if err != nil {
		return nil, fmt.Errorf("wait for shared routing fixture GatewayClass %q acceptance: %w", gatewayClassName, err)
	}

	gatewayConfig := e2ek8s.NewGatewayConfig(e2ek8s.GatewayConfigOptions{
		Namespace:      namespace.Name,
		Name:           gatewayConfigName,
		LoadBalancerID: cfg.OCI.LoadBalancerID,
	})
	err = kubeClient.Create(setupCtx, gatewayConfig)
	if err != nil {
		return nil, fmt.Errorf(
			"create shared routing fixture GatewayConfig %s/%s: %w",
			namespace.Name,
			gatewayConfigName,
			err,
		)
	}

	gateway := e2ek8s.NewHTTPGateway(e2ek8s.HTTPGatewayOptions{
		Namespace:         namespace.Name,
		Name:              gatewayName,
		GatewayClassName:  gatewayClassName,
		GatewayConfigName: gatewayConfigName,
		Port:              gatewayv1.PortNumber(cfg.HTTPPort),
	})
	err = kubeClient.Create(setupCtx, gateway)
	if err != nil {
		return nil, fmt.Errorf(
			"create shared routing fixture Gateway %s/%s: %w",
			namespace.Name,
			gatewayName,
			err,
		)
	}

	_, err = e2ek8s.WaitForGatewayAccepted(setupCtx, kubeClient.Client, namespace.Name, gatewayName, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"wait for shared routing fixture Gateway %s/%s acceptance: %w",
			namespace.Name,
			gatewayName,
			err,
		)
	}

	_, err = e2ek8s.WaitForGatewayProgrammed(setupCtx, kubeClient.Client, namespace.Name, gatewayName, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"wait for shared routing fixture Gateway %s/%s programming: %w",
			namespace.Name,
			gatewayName,
			err,
		)
	}

	staticBackends := []httpStaticBackend{
		{Name: "shared-static-a-" + suffix, Response: "shared-static-a-response-" + suffix},
		{Name: "shared-static-b-" + suffix, Response: "shared-static-b-response-" + suffix},
		{Name: "shared-static-c-" + suffix, Response: "shared-static-c-response-" + suffix},
	}

	for _, backend := range staticBackends {
		service := e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
			Namespace: namespace.Name,
			Name:      backend.Name,
		})
		err = kubeClient.Create(setupCtx, service)
		if err != nil {
			return nil, fmt.Errorf("create shared static Service %s/%s: %w", namespace.Name, backend.Name, err)
		}

		deployment := e2ek8s.NewStaticHTTPDeployment(e2ek8s.StaticHTTPDeploymentOptions{
			Namespace:    namespace.Name,
			Name:         backend.Name,
			ResponseText: backend.Response,
		})
		err = kubeClient.Create(setupCtx, deployment)
		if err != nil {
			return nil, fmt.Errorf("create shared static Deployment %s/%s: %w", namespace.Name, backend.Name, err)
		}
	}

	backendNames := make([]string, 0, len(staticBackends))
	for _, backend := range staticBackends {
		backendNames = append(backendNames, backend.Name)
	}
	for _, backendName := range backendNames {
		_, err = e2ek8s.WaitForDeploymentReady(setupCtx, kubeClient.Client, namespace.Name, backendName, nil)
		if err != nil {
			return nil, fmt.Errorf(
				"wait for shared backend Deployment %s/%s readiness: %w",
				namespace.Name,
				backendName,
				err,
			)
		}

		_, err = e2ek8s.WaitForServiceEndpointsReady(setupCtx, kubeClient.Client, namespace.Name, backendName, nil)
		if err != nil {
			return nil, fmt.Errorf(
				"wait for shared backend Service %s/%s endpoints: %w",
				namespace.Name,
				backendName,
				err,
			)
		}
	}

	logger.InfoContext(
		setupCtx,
		"Shared routing fixture is ready",
		slog.String("namespace", namespace.Name),
		slog.Int("sharedBackendCount", len(backendNames)),
	)

	return &httpRoutingFixture{
		kubeClient:       kubeClient,
		ociClient:        ociClient,
		probeClient:      probeClient,
		namespaceName:    namespace.Name,
		gatewayClassName: gatewayClassName,
		gatewayName:      gatewayName,
		staticBackends:   staticBackends,
	}, nil
}

func randomDNSLabel(fake faker.Faker) string {
	token := strings.ToLower(strings.ReplaceAll(fake.UUID().V4(), "-", ""))
	if len(token) > 12 {
		return token[:12]
	}

	return token
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

func registerHTTPRouteCleanup(
	t *testing.T,
	kubeClient ctrlclient.WithWatch,
	namespace string,
	names ...string,
) {
	t.Helper()

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		if err := deleteHTTPRoutes(cleanupCtx, kubeClient, namespace, names...); err != nil {
			t.Errorf("delete HTTPRoutes in namespace %q: %v", namespace, err)
		}
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

func deleteHTTPRoutes(
	ctx context.Context,
	kubeClient ctrlclient.WithWatch,
	namespace string,
	names ...string,
) error {
	var errs []error

	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		route := &gatewayv1.HTTPRoute{}
		key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}
		if err := kubeClient.Get(ctx, key, route); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			errs = append(errs, fmt.Errorf("get HTTPRoute %s/%s: %w", namespace, name, err))
			continue
		}

		if err := kubeClient.Delete(ctx, route); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete HTTPRoute %s/%s: %w", namespace, name, err))
			continue
		}

		if err := e2ek8s.WaitForHTTPRouteDeleted(ctx, kubeClient, namespace, name, nil); err != nil {
			errs = append(errs, fmt.Errorf("wait for HTTPRoute %s/%s deletion: %w", namespace, name, err))
		}
	}

	return errors.Join(errs...)
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
