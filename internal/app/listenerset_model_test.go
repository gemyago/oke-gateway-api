package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	v1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestListenerSetModel(t *testing.T) {
	fake := faker.New()

	makeListenerSet := func(opts ...func(*v1.ListenerSet)) v1.ListenerSet {
		listenerSet := v1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "ls-ns-" + fake.Lorem().Word(),
				Name:              "ls-" + fake.Lorem().Word(),
				CreationTimestamp: metav1.NewTime(time.Now().Add(time.Duration(fake.IntBetween(1, 100)) * time.Minute)),
			},
			Spec: v1.ListenerSetSpec{
				ParentRef: v1.ParentGatewayReference{Name: "gw-" + v1.ObjectName(fake.Lorem().Word())},
				Listeners: []v1.ListenerEntry{{
					Name:     "http-" + v1.SectionName(fake.Lorem().Word()),
					Port:     80,
					Protocol: v1.HTTPProtocolType,
				}},
			},
		}
		for _, opt := range opts {
			opt(&listenerSet)
		}
		return listenerSet
	}

	makeGateway := func(opts ...func(*v1.Gateway)) v1.Gateway {
		gateway := v1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "gw-ns-" + fake.Lorem().Word(),
				Name:      "gw-" + fake.Lorem().Word(),
			},
			Spec: v1.GatewaySpec{
				Listeners: []v1.Listener{{
					Name:     "web",
					Port:     80,
					Protocol: v1.HTTPProtocolType,
				}},
			},
		}
		for _, opt := range opts {
			opt(&gateway)
		}
		return gateway
	}

	t.Run("listenerSetParentGatewayName", func(t *testing.T) {
		parentNamespace := v1.Namespace("infra-" + fake.Lorem().Word())
		parentName := v1.ObjectName("edge-" + fake.Lorem().Word())
		listenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "apps-" + fake.Lorem().Word()
			listenerSet.Spec.ParentRef = v1.ParentGatewayReference{
				Namespace: &parentNamespace,
				Name:      parentName,
			}
		})

		got, ok := listenerSetParentGatewayName(listenerSet)

		require.True(t, ok)
		assert.Equal(t, fmt.Sprintf("%s/%s", parentNamespace, parentName), got)

		otherKind := v1.Kind("Service")
		listenerSet.Spec.ParentRef.Kind = &otherKind
		_, ok = listenerSetParentGatewayName(listenerSet)
		assert.False(t, ok)

		listenerSet.Spec.ParentRef.Kind = nil
		otherGroup := v1.Group("example.com")
		listenerSet.Spec.ParentRef.Group = &otherGroup
		_, ok = listenerSetParentGatewayName(listenerSet)
		assert.False(t, ok)
	})

	t.Run("listenerSetParentGatewayTarget ignores malformed parent gateway names", func(t *testing.T) {
		listenerSetKind := v1.Kind("ListenerSet")
		listenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "apps-" + fake.Lorem().Word()
			listenerSet.Name = "extra-" + fake.Lorem().Word()
			listenerSet.Spec.ParentRef = v1.ParentGatewayReference{}
		})
		k8sClient := crfake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&listenerSet).
			Build()

		_, resolved, err := listenerSetParentGatewayTarget(
			t.Context(),
			k8sClient,
			listenerSet.Namespace,
			v1.ParentReference{Kind: &listenerSetKind, Name: v1.ObjectName(listenerSet.Name)},
		)

		require.NoError(t, err)
		assert.False(t, resolved)
	})

	t.Run("listenerSetAllowedByGateway", func(t *testing.T) {
		listenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "apps-" + fake.Lorem().Word()
		})
		namespace := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   listenerSet.Namespace,
				Labels: map[string]string{"team": "media"},
			},
		}

		gateway := makeGateway()
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		gateway.Spec.AllowedListeners = &v1.AllowedListeners{
			Namespaces: &v1.ListenerNamespaces{From: lo.ToPtr(v1.NamespacesFromAll)},
		}
		assert.True(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		gateway.Spec.AllowedListeners.Namespaces.From = lo.ToPtr(v1.NamespacesFromSame)
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))
		listenerSet.Namespace = gateway.Namespace
		namespace.Name = listenerSet.Namespace
		assert.True(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		listenerSet.Namespace = "selected-" + fake.Lorem().Word()
		namespace.Name = listenerSet.Namespace
		gateway.Spec.AllowedListeners.Namespaces.From = lo.ToPtr(v1.NamespacesFromSelector)
		gateway.Spec.AllowedListeners.Namespaces.Selector = &metav1.LabelSelector{
			MatchLabels: map[string]string{"team": "media"},
		}
		assert.True(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))
		namespace.Labels["team"] = "payments"
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		gateway.Spec.AllowedListeners.Namespaces.Selector = nil
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		gateway.Spec.AllowedListeners.Namespaces.Selector = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "team",
				Operator: "unknown",
				Values:   []string{"media"},
			}},
		}
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		gateway.Spec.AllowedListeners.Namespaces.From = lo.ToPtr(v1.NamespacesFromNone)
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))

		gateway.Spec.AllowedListeners.Namespaces.From = lo.ToPtr(v1.FromNamespaces("invalid"))
		assert.False(t, listenerSetAllowedByGateway(gateway, listenerSet, namespace))
	})

	t.Run("listenerSetOCIListenerName", func(t *testing.T) {
		gateway := makeGateway(func(gateway *v1.Gateway) {
			gateway.Namespace = strings.Repeat("g", 20)
			gateway.Name = strings.Repeat("w", 20)
		})
		listenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = strings.Repeat("s", 20)
			listenerSet.Name = strings.Repeat("l", 20)
		})
		listener := v1.Listener{Name: v1.SectionName(strings.Repeat("https", 10))}

		got := listenerSetOCIListenerName(gateway, listenerSet, listener)
		other := listenerSetOCIListenerName(gateway, listenerSet, v1.Listener{Name: "http"})

		assert.LessOrEqual(t, len(got), maxOCIListenerNameLength)
		assert.True(t, strings.HasPrefix(got, "ls_"))
		assert.NotEqual(t, other, got)
	})

	t.Run("effectiveListenersForGateway", func(t *testing.T) {
		gateway := makeGateway(func(gateway *v1.Gateway) {
			gateway.Namespace = "infra"
			gateway.Name = "edge"
			hostname := v1.Hostname("api.example.com")
			gateway.Spec.Listeners = []v1.Listener{{
				Name:     "https",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: &hostname,
			}}
		})
		older := metav1.NewTime(time.Now().Add(-2 * time.Hour))
		newer := metav1.NewTime(time.Now().Add(-1 * time.Hour))
		conflictingHostname := v1.Hostname("api.example.com")
		listenerSet1 := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "team-a"
			listenerSet.Name = "edge-extra"
			listenerSet.CreationTimestamp = older
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "grpc",
				Port:     8443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: lo.ToPtr(v1.Hostname("grpc.example.com")),
			}}
		})
		listenerSet2 := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "team-b"
			listenerSet.Name = "edge-extra"
			listenerSet.CreationTimestamp = newer
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "api",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: &conflictingHostname,
			}}
		})
		listenerSet3 := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "team-c"
			listenerSet.Name = "edge-extra"
			listenerSet.CreationTimestamp = metav1.NewTime(time.Now())
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "admin",
				Port:     8443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: lo.ToPtr(v1.Hostname("admin.example.com")),
			}}
		})

		got := effectiveListenersForGateway(gateway, []v1.ListenerSet{listenerSet3, listenerSet2, listenerSet1})

		require.Len(t, got, 4)
		assert.Equal(t, effectiveListenerSourceGateway, got[0].sourceKind)
		assert.Equal(t, effectiveListenerSourceListenerSet, got[1].sourceKind)
		assert.Equal(t, "team-a", got[1].sourceNamespace)
		assert.False(t, got[1].conflicted)
		assert.True(t, got[2].conflicted)
		assert.Equal(t, v1.ListenerReasonHostnameConflict, got[2].conflictReason)
		assert.True(t, got[3].conflicted)
		assert.Equal(t, v1.ListenerReasonPortUnavailable, got[3].conflictReason)

		reversed := effectiveListenersForGateway(gateway, []v1.ListenerSet{listenerSet1, listenerSet2, listenerSet3})
		require.Len(t, reversed, 4)
		assert.Equal(t, got[1].sourceNamespace, reversed[1].sourceNamespace)
		assert.Equal(t, got[1].sourceName, reversed[1].sourceName)
		assert.Equal(t, got[2].conflictReason, reversed[2].conflictReason)
		assert.Equal(t, got[3].conflictReason, reversed[3].conflictReason)

		conflictingProtocol := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "team-d"
			listenerSet.Name = "edge-tcp"
			listenerSet.CreationTimestamp = metav1.NewTime(time.Now())
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "tcp",
				Port:     443,
				Protocol: v1.TCPProtocolType,
			}}
		})
		got = effectiveListenersForGateway(gateway, []v1.ListenerSet{conflictingProtocol})
		require.Len(t, got, 2)
		assert.True(t, got[1].conflicted)
		assert.Equal(t, v1.ListenerReasonProtocolConflict, got[1].conflictReason)
	})

	t.Run("effectiveListenersForParentRef", func(t *testing.T) {
		gateway := makeGateway(func(gateway *v1.Gateway) {
			gateway.Namespace = "infra"
			gateway.Name = "edge"
			gateway.Spec.Listeners = []v1.Listener{{
				Name:     "gateway-http",
				Port:     80,
				Protocol: v1.HTTPProtocolType,
			}}
		})
		listenerSetKind := v1.Kind("ListenerSet")
		listenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "apps"
			listenerSet.Name = "extra"
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "ls-http",
				Port:     8080,
				Protocol: v1.HTTPProtocolType,
			}}
		})
		gatewayDetails := resolvedGatewayDetails{
			gateway:            gateway,
			effectiveListeners: effectiveListenersForGateway(gateway, []v1.ListenerSet{listenerSet}),
		}

		got := effectiveListenersForParentRef(
			gatewayDetails,
			v1.ParentReference{Name: v1.ObjectName(gateway.Name)},
			gateway.Namespace,
			func(v1.ParentReference, v1.Listener) bool { return true },
		)
		require.Len(t, got, 1)
		assert.Equal(t, v1.SectionName("gateway-http"), got[0].Name)

		listenerSetNamespace := v1.Namespace(listenerSet.Namespace)
		got = effectiveListenersForParentRef(
			gatewayDetails,
			v1.ParentReference{
				Kind:      &listenerSetKind,
				Namespace: &listenerSetNamespace,
				Name:      v1.ObjectName(listenerSet.Name),
			},
			gateway.Namespace,
			func(v1.ParentReference, v1.Listener) bool { return true },
		)
		require.Len(t, got, 1)
		assert.NotEqual(t, v1.SectionName("ls-http"), got[0].Name)
		assert.True(t, strings.HasPrefix(string(got[0].Name), "ls_"))

		serviceKind := v1.Kind("Service")
		got = effectiveListenersForParentRef(
			gatewayDetails,
			v1.ParentReference{Kind: &serviceKind, Name: "edge"},
			gateway.Namespace,
			func(v1.ParentReference, v1.Listener) bool { return true },
		)
		assert.Empty(t, got)

		conflicted := gatewayDetails
		conflicted.effectiveListeners = []effectiveListener{{
			listener:   v1.Listener{Name: "conflicted", Port: 80, Protocol: v1.HTTPProtocolType},
			conflicted: true,
		}}
		assert.Empty(t, effectiveOCIListenersForGateway(&conflicted))
		assert.Empty(t, effectiveListenersForParentRef(
			conflicted,
			v1.ParentReference{Name: v1.ObjectName(gateway.Name)},
			gateway.Namespace,
			func(v1.ParentReference, v1.Listener) bool { return true },
		))
	})

	t.Run("effective OCI listeners omit conflicted gateway listeners for each load balancer type", func(t *testing.T) {
		tlsPassthroughMode := v1.TLSModePassthrough
		tlsTerminateMode := v1.TLSModeTerminate
		gateway := makeGateway(func(gateway *v1.Gateway) {
			gateway.Namespace = "infra-" + fake.Lorem().Word()
			gateway.Name = "edge-" + fake.Lorem().Word()
			gateway.Spec.Listeners = []v1.Listener{
				{Name: "http", Port: 80, Protocol: v1.HTTPProtocolType},
				{Name: "grpc", Port: 80, Protocol: v1.HTTPProtocolType},
				{Name: "rtmp", Port: 1935, Protocol: v1.TCPProtocolType},
				{Name: "srt", Port: 1935, Protocol: v1.UDPProtocolType},
				{
					Name:     "rtmps",
					Port:     443,
					Protocol: v1.TLSProtocolType,
					TLS:      &v1.ListenerTLSConfig{Mode: &tlsPassthroughMode},
				},
				{
					Name:     "https",
					Port:     8443,
					Protocol: v1.TLSProtocolType,
					TLS:      &v1.ListenerTLSConfig{Mode: &tlsTerminateMode},
				},
			}
		})
		details := resolvedGatewayDetails{
			gateway:            gateway,
			effectiveListeners: effectiveListenersForGateway(gateway, nil),
		}

		albListeners := gatewayManagedOCIListenersForLoadBalancer(&details)
		nlbListeners := gatewayManagedOCIListenersForNetworkLoadBalancer(&details)

		assert.Equal(t, []v1.SectionName{"http", "rtmp"}, lo.Map(
			albListeners,
			func(listener v1.Listener, _ int) v1.SectionName { return listener.Name },
		))
		assert.Equal(t, []v1.SectionName{"rtmp"}, lo.Map(
			nlbListeners,
			func(listener v1.Listener, _ int) v1.SectionName { return listener.Name },
		))
	})

	t.Run("markConflictedEffectiveListeners skips already conflicted listeners", func(t *testing.T) {
		listeners := []effectiveListener{
			{
				listener:   v1.Listener{Name: "https", Port: 443, Protocol: v1.HTTPSProtocolType},
				conflicted: true,
			},
			{
				listener: v1.Listener{Name: "other", Port: 443, Protocol: v1.TCPProtocolType},
			},
		}

		markConflictedEffectiveListeners(listeners)

		assert.True(t, listeners[0].conflicted)
		assert.True(t, listeners[1].conflicted)
		assert.Equal(t, v1.ListenerReasonProtocolConflict, listeners[1].conflictReason)
	})

	t.Run("effectiveListenersForGateway orders equal timestamp listenersets by name", func(t *testing.T) {
		gateway := makeGateway(func(gateway *v1.Gateway) {
			gateway.Namespace = "infra-" + fake.Lorem().Word()
			gateway.Name = "edge-" + fake.Lorem().Word()
			gateway.Spec.Listeners = nil
		})
		createdAt := metav1.NewTime(time.Now())
		laterByName := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "team-b-" + fake.Lorem().Word()
			listenerSet.Name = "extra-b-" + fake.Lorem().Word()
			listenerSet.CreationTimestamp = createdAt
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "https",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: lo.ToPtr(v1.Hostname("api.example.com")),
			}}
		})
		earlierByName := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "team-a-" + fake.Lorem().Word()
			listenerSet.Name = "extra-a-" + fake.Lorem().Word()
			listenerSet.CreationTimestamp = createdAt
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "https",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: lo.ToPtr(v1.Hostname("api.example.com")),
			}}
		})

		got := effectiveListenersForGateway(gateway, []v1.ListenerSet{laterByName, earlierByName})

		require.Len(t, got, 2)
		assert.Equal(t, earlierByName.Namespace, got[0].sourceNamespace)
		assert.Equal(t, earlierByName.Name, got[0].sourceName)
		assert.False(t, got[0].conflicted)
		assert.Equal(t, laterByName.Namespace, got[1].sourceNamespace)
		assert.Equal(t, laterByName.Name, got[1].sourceName)
		assert.True(t, got[1].conflicted)
		assert.Equal(t, v1.ListenerReasonHostnameConflict, got[1].conflictReason)
	})

	t.Run("effectiveListenerOCIListener", func(t *testing.T) {
		secretName := v1.ObjectName("tls-cert")
		listener := effectiveListener{
			listener: v1.Listener{
				Name:     "https",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				TLS: &v1.ListenerTLSConfig{
					CertificateRefs: []v1.SecretObjectReference{{Name: secretName}},
				},
			},
			sourceKind:      effectiveListenerSourceListenerSet,
			sourceNamespace: "apps",
			ociName:         "ls_apps_edge_https",
		}

		got := effectiveListenerOCIListener(listener)

		require.NotNil(t, got.TLS)
		require.Len(t, got.TLS.CertificateRefs, 1)
		assert.Equal(t, v1.SectionName("ls_apps_edge_https"), got.Name)
		assert.Equal(t, lo.ToPtr(v1.Namespace("apps")), got.TLS.CertificateRefs[0].Namespace)
		assert.Nil(t, listener.listener.TLS.CertificateRefs[0].Namespace)

		gatewayDetails := resolvedGatewayDetails{
			gateway: makeGateway(func(gateway *v1.Gateway) {
				gateway.Namespace = "infra"
				gateway.Name = "edge"
			}),
			effectiveListeners: []effectiveListener{{
				listener:        v1.Listener{Name: "https", Port: 443, Protocol: v1.HTTPSProtocolType},
				sourceKind:      effectiveListenerSourceListenerSet,
				sourceNamespace: "ignored",
				sourceName:      "conflicted",
				ociName:         "ls_apps_edge_https",
				conflicted:      true,
			}, {
				listener:        v1.Listener{Name: "https", Port: 443, Protocol: v1.HTTPSProtocolType},
				sourceKind:      effectiveListenerSourceListenerSet,
				sourceNamespace: "apps",
				sourceName:      "accepted",
				ociName:         "ls_apps_edge_https",
			}},
		}
		assert.Equal(t, "apps", effectiveListenerSourceNamespaceForOCIListener(gatewayDetails, got))
	})

	t.Run("listenerSetStatusForGateway", func(t *testing.T) {
		gateway := makeGateway(func(gateway *v1.Gateway) {
			gateway.Namespace = "infra"
			gateway.Name = "edge"
			gateway.Spec.Listeners = []v1.Listener{{
				Name:     "https",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: lo.ToPtr(v1.Hostname("api.example.com")),
			}}
		})
		listenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "apps"
			listenerSet.Name = "extra"
			listenerSet.Generation = 7
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "api",
				Port:     443,
				Protocol: v1.HTTPSProtocolType,
				Hostname: lo.ToPtr(v1.Hostname("api.example.com")),
			}}
		})
		effectiveListeners := effectiveListenersForGateway(gateway, []v1.ListenerSet{listenerSet})

		got := listenerSetStatusForGateway(
			gateway,
			listenerSet,
			effectiveListeners,
			v1.GatewayController(ControllerClassName),
			nil,
		)

		require.Len(t, got.Conditions, 2)
		require.Len(t, got.Listeners, 1)
		accepted := lo.FindOrElse(got.Listeners[0].Conditions, metav1.Condition{},
			func(condition metav1.Condition) bool {
				return condition.Type == string(v1.ListenerConditionAccepted)
			})
		assert.Equal(t, metav1.ConditionFalse, accepted.Status)
		assert.Equal(t, string(v1.ListenerReasonHostnameConflict), accepted.Reason)
		assert.Equal(t, int64(7), accepted.ObservedGeneration)
		assert.ElementsMatch(t, []v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "HTTPRoute",
		}, {
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "GRPCRoute",
		}}, got.Listeners[0].SupportedKinds)
		assert.True(t, listenerSetStatusSemanticallyEqual(got, got))
		reordered := got.DeepCopy()
		if len(reordered.Conditions) > 1 {
			reordered.Conditions[0], reordered.Conditions[1] = reordered.Conditions[1], reordered.Conditions[0]
		}
		if len(reordered.Listeners[0].Conditions) > 1 {
			reordered.Listeners[0].Conditions[0], reordered.Listeners[0].Conditions[1] =
				reordered.Listeners[0].Conditions[1], reordered.Listeners[0].Conditions[0]
		}
		assert.True(t, listenerSetStatusSemanticallyEqual(got, *reordered))

		changed := got.DeepCopy()
		changed.Listeners[0].Conditions[0].Reason = "Other"
		assert.False(t, listenerSetStatusSemanticallyEqual(got, *changed))
		changed = got.DeepCopy()
		changed.Listeners[0].AttachedRoutes++
		assert.False(t, listenerSetStatusSemanticallyEqual(got, *changed))
		changed = got.DeepCopy()
		changed.Listeners[0].Name = "other"
		assert.False(t, listenerSetStatusSemanticallyEqual(got, *changed))
		changed = got.DeepCopy()
		changed.Listeners = append(changed.Listeners, v1.ListenerEntryStatus{Name: "extra"})
		assert.False(t, listenerSetStatusSemanticallyEqual(got, *changed))

		acceptedListenerSet := makeListenerSet(func(listenerSet *v1.ListenerSet) {
			listenerSet.Namespace = "apps"
			listenerSet.Name = "accepted"
			listenerSet.Generation = 3
			listenerSet.Spec.Listeners = []v1.ListenerEntry{{
				Name:     "grpc",
				Port:     8443,
				Protocol: v1.HTTPSProtocolType,
				AllowedRoutes: &v1.AllowedRoutes{Kinds: []v1.RouteGroupKind{{
					Group: lo.ToPtr(v1.Group(v1.GroupName)),
					Kind:  "GRPCRoute",
				}}},
			}, {
				Name:     "tcp",
				Port:     9000,
				Protocol: v1.TCPProtocolType,
			}, {
				Name:     "udp",
				Port:     9001,
				Protocol: v1.UDPProtocolType,
			}, {
				Name:     "tls",
				Port:     9443,
				Protocol: v1.TLSProtocolType,
			}, {
				Name:     "raw",
				Port:     9999,
				Protocol: v1.ProtocolType("CUSTOM"),
			}}
		})
		status := listenerSetStatusForGateway(
			gateway,
			acceptedListenerSet,
			effectiveListenersForGateway(gateway, []v1.ListenerSet{acceptedListenerSet}),
			v1.GatewayController(ControllerClassName),
			nil,
		)
		require.Len(t, status.Listeners, 5)
		assert.ElementsMatch(t, []v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "GRPCRoute",
		}}, status.Listeners[0].SupportedKinds)
		assert.ElementsMatch(t, []v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "TCPRoute",
		}}, status.Listeners[1].SupportedKinds)
		assert.ElementsMatch(t, []v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "UDPRoute",
		}}, status.Listeners[2].SupportedKinds)
		assert.ElementsMatch(t, []v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "TLSRoute",
		}}, status.Listeners[3].SupportedKinds)
		assert.Empty(t, status.Listeners[4].SupportedKinds)

		unsupportedListeners := effectiveListenersForGateway(gateway, []v1.ListenerSet{acceptedListenerSet})
		markUnsupportedListenerSetListeners(
			unsupportedListeners,
			v1.GatewayController(ControllerClassName),
		)
		status = listenerSetStatusForGateway(
			gateway,
			acceptedListenerSet,
			unsupportedListeners,
			v1.GatewayController(ControllerClassName),
			nil,
		)
		assert.True(t, meta.IsStatusConditionTrue(status.Conditions, string(v1.ListenerSetConditionAccepted)))
		listenerSetAccepted := lo.FindOrElse(
			status.Conditions,
			metav1.Condition{},
			func(condition metav1.Condition) bool {
				return condition.Type == string(v1.ListenerSetConditionAccepted)
			},
		)
		assert.Equal(t, string(v1.ListenerSetReasonAccepted), listenerSetAccepted.Reason)
		tcpAccepted := lo.FindOrElse(status.Listeners[1].Conditions, metav1.Condition{},
			func(condition metav1.Condition) bool {
				return condition.Type == string(v1.ListenerConditionAccepted)
			})
		assert.Equal(t, metav1.ConditionFalse, tcpAccepted.Status)
		assert.Equal(t, string(v1.ListenerReasonUnsupportedProtocol), tcpAccepted.Reason)
		assert.NotContains(t,
			lo.Map(effectiveOCIListenersForGateway(&resolvedGatewayDetails{
				gateway:            gateway,
				effectiveListeners: unsupportedListeners,
			}), func(listener v1.Listener, _ int) v1.SectionName {
				return listener.Name
			}),
			v1.SectionName("tcp"),
		)

		nlbUnsupportedListeners := effectiveListenersForGateway(gateway, []v1.ListenerSet{acceptedListenerSet})
		markUnsupportedListenerSetListeners(
			nlbUnsupportedListeners,
			v1.GatewayController(NetworkLoadBalancerControllerClassName),
		)
		status = listenerSetStatusForGateway(
			gateway,
			acceptedListenerSet,
			nlbUnsupportedListeners,
			v1.GatewayController(NetworkLoadBalancerControllerClassName),
			nil,
		)
		httpAccepted := lo.FindOrElse(status.Listeners[0].Conditions, metav1.Condition{},
			func(condition metav1.Condition) bool {
				return condition.Type == string(v1.ListenerConditionAccepted)
			})
		assert.Equal(t, metav1.ConditionFalse, httpAccepted.Status)
		assert.Equal(t, string(v1.ListenerReasonUnsupportedProtocol), httpAccepted.Reason)

		passthrough := v1.TLSModePassthrough
		reason, message, unsupported := listenerSetListenerUnsupported(
			v1.Listener{
				Name:     "tls-passthrough",
				Port:     9443,
				Protocol: v1.TLSProtocolType,
				TLS:      &v1.ListenerTLSConfig{Mode: &passthrough},
			},
			v1.GatewayController(NetworkLoadBalancerControllerClassName),
		)
		assert.False(t, unsupported)
		assert.Empty(t, reason)
		assert.Empty(t, message)

		assert.False(t, listenerSetStatusSemanticallyEqual(got, status))
		assert.False(t, routeGroupKindsEqual(status.Listeners[0].SupportedKinds, nil))
		assert.False(t, routeGroupKindsEqual([]v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group("example.com")),
			Kind:  "GRPCRoute",
		}}, []v1.RouteGroupKind{{
			Group: lo.ToPtr(v1.Group(v1.GroupName)),
			Kind:  "GRPCRoute",
		}}))
		assert.False(t, conditionsSemanticallyEqual(got.Conditions, nil))
	})

	t.Run("listener entry reason helpers map Gateway listener reasons", func(t *testing.T) {
		assert.Equal(
			t,
			v1.ListenerEntryReasonHostnameConflict,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonHostnameConflict),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonProtocolConflict,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonProtocolConflict),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonInvalidCertificateRef,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonInvalidCertificateRef),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonInvalidRouteKinds,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonInvalidRouteKinds),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonRefNotPermitted,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonRefNotPermitted),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonUnsupportedProtocol,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonUnsupportedProtocol),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonPortUnavailable,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonPortUnavailable),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonInvalid,
			listenerEntryReasonFromListenerReason(v1.ListenerReasonPending),
		)
	})

	t.Run("listener entry condition helper statuses", func(t *testing.T) {
		assert.Equal(
			t,
			metav1.ConditionFalse,
			listenerEntryResolvedRefsStatus(v1.ListenerEntryReasonRefNotPermitted),
		)
		assert.Equal(
			t,
			metav1.ConditionTrue,
			listenerEntryResolvedRefsStatus(v1.ListenerEntryReasonAccepted),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonInvalidCertificateRef,
			listenerEntryResolvedRefsReason(v1.ListenerEntryReasonInvalidCertificateRef),
		)
		assert.Equal(
			t,
			v1.ListenerEntryReasonResolvedRefs,
			listenerEntryResolvedRefsReason(v1.ListenerEntryReasonAccepted),
		)
		assert.Equal(t, metav1.ConditionTrue, listenerEntryConflictedStatus(effectiveListener{conflicted: true}))
		assert.Equal(t, metav1.ConditionFalse, listenerEntryConflictedStatus(effectiveListener{}))
		assert.Equal(
			t,
			v1.ListenerEntryReasonProtocolConflict,
			listenerEntryConflictedReason(effectiveListener{
				conflicted:     true,
				conflictReason: v1.ListenerReasonProtocolConflict,
			}),
		)
		assert.Equal(
			t,
			v1.ListenerEntryConditionReason(v1.ListenerReasonNoConflicts),
			listenerEntryConflictedReason(effectiveListener{}),
		)
	})

	t.Run("supportedRouteKindNamesForProtocol returns expected route kinds by listener protocol", func(t *testing.T) {
		assert.Equal(t, []v1.Kind{"HTTPRoute", "GRPCRoute"}, supportedRouteKindNamesForProtocol(v1.HTTPProtocolType))
		assert.Equal(t, []v1.Kind{"HTTPRoute", "GRPCRoute"}, supportedRouteKindNamesForProtocol(v1.HTTPSProtocolType))
		assert.Equal(t, []v1.Kind{"TLSRoute"}, supportedRouteKindNamesForProtocol(v1.TLSProtocolType))
		assert.Equal(t, []v1.Kind{"TCPRoute"}, supportedRouteKindNamesForProtocol(v1.TCPProtocolType))
		assert.Equal(t, []v1.Kind{"UDPRoute"}, supportedRouteKindNamesForProtocol(v1.UDPProtocolType))
		assert.Nil(t, supportedRouteKindNamesForProtocol(v1.ProtocolType("SCTP")))
	})

	t.Run("attachedListenerSetCount only counts sets with accepted effective listeners", func(t *testing.T) {
		listenerSets := []v1.ListenerSet{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "accepted"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "rejected"}},
		}
		count := attachedListenerSetCount(listenerSets, []effectiveListener{
			{
				sourceKind:      effectiveListenerSourceListenerSet,
				sourceNamespace: "apps",
				sourceName:      "accepted",
			},
			{
				sourceKind:      effectiveListenerSourceListenerSet,
				sourceNamespace: "apps",
				sourceName:      "rejected",
				unsupported:     true,
			},
		})

		require.NotNil(t, count)
		assert.Equal(t, int32(1), *count)
	})
}
