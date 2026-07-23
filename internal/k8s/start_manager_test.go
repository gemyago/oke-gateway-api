package k8s

import (
	"errors"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

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
	t.Run("detects TCPRoute UDPRoute BackendTLSPolicy and ListenerSet", func(t *testing.T) {
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: gatewayv1.GroupName, Version: "v1"},
		})
		for _, kind := range []string{"TCPRoute", "UDPRoute", "BackendTLSPolicy", "ListenerSet"} {
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
		assert.False(t, got.BackendTLSPolicy)
		assert.False(t, got.ListenerSet)
	})

	t.Run("returns non discovery errors", func(t *testing.T) {
		wantErr := errors.New("discovery failed")

		got, err := detectExperimentalRouteCapabilities(failingRESTMapper{err: wantErr})

		require.ErrorIs(t, err, wantErr)
		assert.False(t, got.TCPRoute)
		assert.False(t, got.UDPRoute)
		assert.False(t, got.BackendTLSPolicy)
		assert.False(t, got.ListenerSet)
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
			},
		)

		require.NoError(t, err)
		assert.False(t, got.reconcileTCPRoute)
		assert.False(t, got.reconcileUDPRoute)
		assert.False(t, got.listenerSetAvailable)
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
