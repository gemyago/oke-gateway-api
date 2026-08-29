package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/gemyago/oke-gateway-api/internal/diag"
)

func TestGatewayObjectPredicate(t *testing.T) {
	fake := faker.New()

	t.Run("accepts removal of controller listener ownership annotation", func(t *testing.T) {
		oldGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.UUID().V4(),
				Name:       "gateway-" + fake.UUID().V4(),
				Generation: 1,
				Annotations: map[string]string{
					app.LoadBalancerGatewayProgrammedListenersAnnotation: "https",
				},
			},
		}
		newGateway := oldGateway.DeepCopy()
		delete(newGateway.Annotations, app.LoadBalancerGatewayProgrammedListenersAnnotation)

		result := gatewayObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldGateway,
			ObjectNew: newGateway,
		})

		assert.True(t, result)
	})

	t.Run("accepts changes to network load balancer ownership annotations", func(t *testing.T) {
		oldGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.UUID().V4(),
				Name:       "gateway-" + fake.UUID().V4(),
				Generation: 1,
				Annotations: map[string]string{
					app.NetworkLoadBalancerGatewayProgrammedListenersAnnotation:   "tcp",
					app.NetworkLoadBalancerGatewayProgrammedBackendSetsAnnotation: "bs_tcp",
				},
			},
		}
		newGateway := oldGateway.DeepCopy()
		newGateway.Annotations[app.NetworkLoadBalancerGatewayProgrammedListenersAnnotation] = "tcp,udp"

		result := gatewayObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldGateway,
			ObjectNew: newGateway,
		})

		assert.True(t, result)
	})

	t.Run("accepts removal of cleanup critical ALB annotations", func(t *testing.T) {
		oldGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.UUID().V4(),
				Name:       "gateway-" + fake.UUID().V4(),
				Generation: 1,
				Annotations: map[string]string{
					app.LoadBalancerGatewayIDAnnotation:         "ocid1.loadbalancer.oc1.." + fake.UUID().V4(),
					app.GatewayProgrammedCertificatesAnnotation: "cert-" + fake.UUID().V4(),
				},
			},
		}
		newGateway := oldGateway.DeepCopy()
		delete(newGateway.Annotations, app.LoadBalancerGatewayIDAnnotation)
		delete(newGateway.Annotations, app.GatewayProgrammedCertificatesAnnotation)

		result := gatewayObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldGateway,
			ObjectNew: newGateway,
		})

		assert.True(t, result)
	})

	t.Run("accepts removal of cleanup critical NLB annotations", func(t *testing.T) {
		oldGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.UUID().V4(),
				Name:       "gateway-" + fake.UUID().V4(),
				Generation: 1,
				Annotations: map[string]string{
					app.NetworkLoadBalancerGatewayIDAnnotation: "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4(),
				},
			},
		}
		newGateway := oldGateway.DeepCopy()
		delete(newGateway.Annotations, app.NetworkLoadBalancerGatewayIDAnnotation)

		result := gatewayObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldGateway,
			ObjectNew: newGateway,
		})

		assert.True(t, result)
	})

	t.Run("accepts deletion timestamp changes", func(t *testing.T) {
		oldGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.UUID().V4(),
				Name:       "gateway-" + fake.UUID().V4(),
				Generation: 1,
			},
		}
		newGateway := oldGateway.DeepCopy()
		now := metav1.Now()
		newGateway.DeletionTimestamp = &now

		result := gatewayObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldGateway,
			ObjectNew: newGateway,
		})

		assert.True(t, result)
	})

	t.Run("ignores unrelated annotation only updates", func(t *testing.T) {
		oldGateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.UUID().V4(),
				Name:       "gateway-" + fake.UUID().V4(),
				Generation: 1,
				Annotations: map[string]string{
					"example.com/checksum": "before",
				},
			},
		}
		newGateway := oldGateway.DeepCopy()
		newGateway.Annotations["example.com/checksum"] = "after"

		result := gatewayObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldGateway,
			ObjectNew: newGateway,
		})

		assert.False(t, result)
	})

	t.Run("ignores nil update event objects", func(t *testing.T) {
		assert.False(t, gatewayDeletionStarted(event.UpdateEvent{}))
		assert.False(t, gatewayControllerAnnotationChanged(event.UpdateEvent{}))
	})
}

func TestL7RouteObjectPredicate(t *testing.T) {
	t.Run("accepts annotation only updates", func(t *testing.T) {
		oldRoute := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "demo",
				Name:       "api",
				Generation: 1,
				Annotations: map[string]string{
					"example.com/reconcile": "before",
				},
			},
		}
		newRoute := oldRoute.DeepCopy()
		newRoute.Annotations["example.com/reconcile"] = "after"

		result := l7RouteObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldRoute,
			ObjectNew: newRoute,
		})

		assert.True(t, result)
	})
}

func TestListenerSetRouteObjectPredicate(t *testing.T) {
	fake := faker.New()
	predicate := listenerSetRouteObjectPredicate()

	t.Run("accepts resource version changes", func(t *testing.T) {
		oldListenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       "demo",
				Name:            "extra-listeners",
				ResourceVersion: "1",
			},
		}
		newListenerSet := oldListenerSet.DeepCopy()
		newListenerSet.ResourceVersion = "2"
		newListenerSet.Status.Conditions = []metav1.Condition{{
			Type:               string(gatewayv1.ListenerSetConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.ListenerSetReasonAccepted),
			ObservedGeneration: 1,
		}}

		result := predicate.Update(event.UpdateEvent{
			ObjectOld: oldListenerSet,
			ObjectNew: newListenerSet,
		})

		assert.True(t, result)
	})

	t.Run("accepts status only updates", func(t *testing.T) {
		oldObj := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "listeners-" + fake.UUID().V4(),
				Namespace:       "ns-" + fake.UUID().V4(),
				Generation:      1,
				ResourceVersion: fake.UUID().V4(),
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = fake.UUID().V4()
		newObj.Status.Conditions = []metav1.Condition{{
			Type:               string(gatewayv1.ListenerSetConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             string(gatewayv1.ListenerSetReasonNotAllowed),
			ObservedGeneration: oldObj.Generation,
			Message:            "parent gateway no longer allows this ListenerSet",
		}}

		assert.True(t, predicate.Update(event.UpdateEvent{
			ObjectOld: oldObj,
			ObjectNew: newObj,
		}))
	})
}

func TestStartManager(t *testing.T) {
	t.Run("gatewaySecretPredicate", func(t *testing.T) {
		t.Run("allows TLS Secret create events to reach Gateway mapping", func(t *testing.T) {
			fake := faker.New()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tls-" + fake.Internet().Slug(),
					Namespace: "ns-" + fake.Internet().Slug(),
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					corev1.TLSCertKey:       []byte(fake.Lorem().Sentence(10)),
					corev1.TLSPrivateKeyKey: []byte(fake.Lorem().Sentence(10)),
				},
			}

			result := gatewaySecretPredicate().Create(event.CreateEvent{Object: secret})

			assert.True(t, result)
		})
	})
}

func TestRunControllerSetupTasks(t *testing.T) {
	fake := faker.New()

	t.Run("skips disabled tasks and runs enabled tasks", func(t *testing.T) {
		calls := make([]string, 0, 1)
		err := runControllerSetupTasks(t.Context(), diag.RootTestLogger(), []controllerSetupTask{
			{
				enabled:     false,
				disabledLog: "disabled " + fake.Lorem().Word(),
				setup: func() error {
					t.Fatal("disabled task should not run")
					return nil
				},
				setupErr: "disabled failed: %w",
			},
			{
				enabled:     true,
				disabledLog: "enabled " + fake.Lorem().Word(),
				setup: func() error {
					calls = append(calls, "enabled")
					return nil
				},
				setupErr: "enabled failed: %w",
			},
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"enabled"}, calls)
	})

	t.Run("wraps enabled task setup errors", func(t *testing.T) {
		wantErr := errors.New("setup failed")

		err := runControllerSetupTasks(t.Context(), diag.RootTestLogger(), []controllerSetupTask{{
			enabled: true,
			setup: func() error {
				return wantErr
			},
			setupErr: "failed to setup controller: %w",
		}})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to setup controller")
	})
}

func TestControllerSetupTasks(t *testing.T) {
	t.Run("core controllers follow feature flags", func(t *testing.T) {
		tasks := coreControllerSetupTasks(nil, StartManagerDeps{
			ReconcileGatewayClass:               true,
			ReconcileGateway:                    false,
			ReconcileNetworkLoadBalancerGateway: true,
		}, resolvedExperimentalRouteCapabilities{listenerSetAvailable: true}, nil)

		require.Len(t, tasks, 3)
		assert.True(t, tasks[0].enabled)
		assert.False(t, tasks[1].enabled)
		assert.True(t, tasks[2].enabled)
	})

	t.Run("ListenerSet controller follows CRD availability", func(t *testing.T) {
		disabledTasks := listenerSetControllerSetupTasks(
			nil,
			StartManagerDeps{},
			resolvedExperimentalRouteCapabilities{},
			nil,
		)
		enabledTasks := listenerSetControllerSetupTasks(
			nil,
			StartManagerDeps{},
			resolvedExperimentalRouteCapabilities{listenerSetAvailable: true},
			nil,
		)

		require.Len(t, disabledTasks, 1)
		require.Len(t, enabledTasks, 1)
		assert.False(t, disabledTasks[0].enabled)
		assert.True(t, enabledTasks[0].enabled)
	})

	t.Run("L7 and TLS controllers follow feature flags and optional CRDs", func(t *testing.T) {
		tasks := l7AndTLSControllerSetupTasks(nil, StartManagerDeps{
			ReconcileHTTPRoute:        true,
			ReconcileGRPCRoute:        false,
			ReconcileTLSRoute:         true,
			ReconcileBackendTLSPolicy: true,
		}, resolvedExperimentalRouteCapabilities{
			reconcileTLSRoute:         true,
			reconcileBackendTLSPolicy: true,
			backendTLSPolicyAvailable: true,
			listenerSetAvailable:      true,
		}, nil)

		require.Len(t, tasks, 4)
		assert.True(t, tasks[0].enabled)
		assert.False(t, tasks[1].enabled)
		assert.True(t, tasks[2].enabled)
		assert.True(t, tasks[3].enabled)
	})

	t.Run("L4 route controllers follow optional CRDs", func(t *testing.T) {
		tasks := l4RouteControllerSetupTasks(nil, StartManagerDeps{
			ReconcileTCPRoute: true,
			ReconcileUDPRoute: true,
		}, resolvedExperimentalRouteCapabilities{
			reconcileTCPRoute:    true,
			reconcileUDPRoute:    false,
			listenerSetAvailable: true,
		}, nil)

		require.Len(t, tasks, 2)
		assert.True(t, tasks[0].enabled)
		assert.False(t, tasks[1].enabled)
	})

	t.Run("ListenerSet mapper is only set when enabled", func(t *testing.T) {
		mapper := func(context.Context, client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: "ns", Name: "name"}}}
		}

		require.Nil(t, listenerSetMapper(false, mapper))
		require.NotNil(t, listenerSetMapper(true, mapper))
	})
}

func TestL4RouteObjectPredicate(t *testing.T) {
	fake := faker.New()
	newPod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "pod-" + fake.UUID().V4(),
				Namespace:       "ns-" + fake.UUID().V4(),
				Generation:      1,
				ResourceVersion: fake.UUID().V4(),
				Labels:          map[string]string{"app": "route-test"},
				Annotations:     map[string]string{"revision": "one"},
			},
		}
	}
	updateEvent := func(oldObj, newObj *corev1.Pod) event.UpdateEvent {
		return event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}
	}

	predicate := l4RouteObjectPredicate()

	t.Run("accepts generation changes", func(t *testing.T) {
		oldObj := newPod()
		newObj := oldObj.DeepCopy()
		newObj.Generation = oldObj.Generation + 1

		assert.True(t, predicate.Update(updateEvent(oldObj, newObj)))
	})

	t.Run("accepts label changes", func(t *testing.T) {
		oldObj := newPod()
		newObj := oldObj.DeepCopy()
		newObj.Labels["app"] = "route-test-" + fake.UUID().V4()

		assert.True(t, predicate.Update(updateEvent(oldObj, newObj)))
	})

	t.Run("accepts annotation changes", func(t *testing.T) {
		oldObj := newPod()
		newObj := oldObj.DeepCopy()
		newObj.Annotations["revision"] = "two-" + fake.UUID().V4()

		assert.True(t, predicate.Update(updateEvent(oldObj, newObj)))
	})

	t.Run("ignores resource version only changes", func(t *testing.T) {
		oldObj := newPod()
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = fake.UUID().V4()

		assert.False(t, predicate.Update(updateEvent(oldObj, newObj)))
	})

	t.Run("ignores ListenerSet status only updates", func(t *testing.T) {
		oldObj := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "listeners-" + fake.UUID().V4(),
				Namespace:       "ns-" + fake.UUID().V4(),
				Generation:      1,
				ResourceVersion: fake.UUID().V4(),
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = fake.UUID().V4()
		newObj.Status.Conditions = []metav1.Condition{{
			Type:               string(gatewayv1.ListenerSetConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             string(gatewayv1.ListenerSetReasonNotAllowed),
			ObservedGeneration: oldObj.Generation,
			Message:            "parent gateway no longer allows this ListenerSet",
		}}

		assert.False(t, predicate.Update(event.UpdateEvent{
			ObjectOld: oldObj,
			ObjectNew: newObj,
		}))
	})
}

func TestDetectExperimentalRouteCapabilities(t *testing.T) {
	t.Run("detects optional Gateway API resources", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
		})
		for _, kind := range []string{"TCPRoute", "UDPRoute", "TLSRoute", "BackendTLSPolicy", "ListenerSet"} {
			mapper.Add(schema.GroupVersionKind{
				Group:   gatewayv1.GroupName,
				Version: "v1",
				Kind:    kind,
			}, meta.RESTScopeNamespace)
		}

		got, err := detectExperimentalRouteCapabilities(mapper)

		require.NoError(t, err)
		assert.True(t, got.TCPRoute)
		assert.True(t, got.UDPRoute)
		assert.True(t, got.TLSRoute)
		assert.True(t, got.BackendTLSPolicy)
		assert.True(t, got.ListenerSet)
	})

	t.Run("treats missing optional Gateway API resources as unavailable", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
		})

		got, err := detectExperimentalRouteCapabilities(mapper)

		require.NoError(t, err)
		assert.False(t, got.TCPRoute)
		assert.False(t, got.UDPRoute)
		assert.False(t, got.TLSRoute)
		assert.False(t, got.BackendTLSPolicy)
		assert.False(t, got.ListenerSet)
	})

	t.Run("returns non discovery errors", func(t *testing.T) {
		wantErr := errors.New("discovery failed")

		got, err := detectExperimentalRouteCapabilities(failingRESTMapper{err: wantErr})

		require.ErrorIs(t, err, wantErr)
		assert.False(t, got.TCPRoute)
		assert.False(t, got.UDPRoute)
		assert.False(t, got.TLSRoute)
		assert.False(t, got.BackendTLSPolicy)
		assert.False(t, got.ListenerSet)
	})

	t.Run("wraps discovery errors for each optional resource", func(t *testing.T) {
		wantErr := errors.New("discovery failed")
		for _, tc := range []struct {
			kind    string
			message string
		}{
			{kind: "UDPRoute", message: "failed to detect UDPRoute availability"},
			{kind: "TLSRoute", message: "failed to detect TLSRoute availability"},
			{kind: "BackendTLSPolicy", message: "failed to detect BackendTLSPolicy availability"},
			{kind: "ListenerSet", message: "failed to detect ListenerSet availability"},
		} {
			t.Run(tc.kind, func(t *testing.T) {
				got, err := detectExperimentalRouteCapabilities(selectiveRESTMapper{
					failKind: tc.kind,
					err:      wantErr,
				})

				require.ErrorIs(t, err, wantErr)
				require.ErrorContains(t, err, tc.message)
				assert.False(t, got.TCPRoute)
				assert.False(t, got.UDPRoute)
				assert.False(t, got.TLSRoute)
				assert.False(t, got.BackendTLSPolicy)
				assert.False(t, got.ListenerSet)
			})
		}
	})
}

func TestResolveExperimentalRouteCapabilities(t *testing.T) {
	t.Run("disables L4 routes when only standard CRDs are installed", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
			{Group: gatewayv1.GroupName, Version: "v1beta1"},
		})
		for _, kind := range []string{"GatewayClass", "Gateway", "HTTPRoute"} {
			mapper.Add(schema.GroupVersionKind{
				Group:   gatewayv1.GroupName,
				Version: "v1",
				Kind:    kind,
			}, meta.RESTScopeNamespace)
		}
		mapper.Add(schema.GroupVersionKind{
			Group:   gatewayv1.GroupName,
			Version: "v1beta1",
			Kind:    "ReferenceGrant",
		}, meta.RESTScopeNamespace)

		got, err := resolveExperimentalRouteCapabilities(
			t.Context(),
			diag.RootTestLogger(),
			mapper,
			StartManagerDeps{
				ReconcileTCPRoute: true,
				ReconcileUDPRoute: true,
				ReconcileTLSRoute: true,
			},
		)

		require.NoError(t, err)
		assert.False(t, got.reconcileTCPRoute)
		assert.False(t, got.reconcileUDPRoute)
		assert.False(t, got.reconcileTLSRoute)
		assert.False(t, got.listenerSetAvailable)
	})

	t.Run("enables TLSRoute only when configured and installed", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
		})
		mapper.Add(schema.GroupVersionKind{
			Group:   gatewayv1.GroupName,
			Version: "v1",
			Kind:    "TLSRoute",
		}, meta.RESTScopeNamespace)

		got, err := resolveExperimentalRouteCapabilities(
			t.Context(),
			diag.RootTestLogger(),
			mapper,
			StartManagerDeps{ReconcileTLSRoute: true},
		)

		require.NoError(t, err)
		assert.True(t, got.reconcileTLSRoute)
	})

	t.Run("keeps BackendTLSPolicy controller available for cleanup when feature is disabled", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
		})
		mapper.Add(schema.GroupVersionKind{
			Group:   gatewayv1.GroupName,
			Version: "v1",
			Kind:    "BackendTLSPolicy",
		}, meta.RESTScopeNamespace)

		got, err := resolveExperimentalRouteCapabilities(
			t.Context(),
			diag.RootTestLogger(),
			mapper,
			StartManagerDeps{
				ReconcileBackendTLSPolicy: false,
			},
		)

		require.NoError(t, err)
		assert.False(t, got.reconcileBackendTLSPolicy)
		assert.True(t, got.backendTLSPolicyAvailable)
	})

	t.Run("enables ListenerSet support when the CRD is installed", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
		})
		mapper.Add(schema.GroupVersionKind{
			Group:   gatewayv1.GroupName,
			Version: "v1",
			Kind:    "ListenerSet",
		}, meta.RESTScopeNamespace)

		got, err := resolveExperimentalRouteCapabilities(
			t.Context(),
			diag.RootTestLogger(),
			mapper,
			StartManagerDeps{},
		)

		require.NoError(t, err)
		assert.True(t, got.listenerSetAvailable)
	})
}

type failingRESTMapper struct {
	meta.RESTMapper

	err error
}

func (m failingRESTMapper) RESTMapping(_ schema.GroupKind, _ ...string) (*meta.RESTMapping, error) {
	return nil, m.err
}

type selectiveRESTMapper struct {
	meta.RESTMapper

	failKind string
	err      error
}

func (m selectiveRESTMapper) RESTMapping(groupKind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if groupKind.Kind == m.failKind {
		return nil, m.err
	}
	version := gatewayv1.GroupVersion.Version
	if len(versions) > 0 {
		version = versions[0]
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: groupKind.Group, Version: version},
	})
	mapper.Add(schema.GroupVersionKind{
		Group:   groupKind.Group,
		Version: version,
		Kind:    groupKind.Kind,
	}, meta.RESTScopeNamespace)
	return mapper.RESTMapping(groupKind, versions...)
}
