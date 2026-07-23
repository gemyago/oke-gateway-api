package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
)

func TestHTTPRouteModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) httpRouteModelDeps {
		return httpRouteModelDeps{
			K8sClient:      NewMockk8sClient(t),
			RootLogger:     diag.RootTestLogger(),
			GatewayModel:   NewMockgatewayModel(t),
			OciLBModel:     NewMockociLoadBalancerModel(t),
			ResourcesModel: NewMockresourcesModel(t),
		}
	}

	t.Run("programmedHTTPRoutePolicyRulesAnnotation", func(t *testing.T) {
		t.Run("formats listener scoped policy rules", func(t *testing.T) {
			listenerA := makeRandomListener()
			listenerB := makeRandomListener()
			ruleNames := []string{
				"rule-a-" + faker.New().Lorem().Word(),
				"rule-b-" + faker.New().Lorem().Word(),
			}

			assert.Equal(t, []string{
				fmt.Sprintf("%s/%s", listenerA.Name, ruleNames[0]),
				fmt.Sprintf("%s/%s", listenerA.Name, ruleNames[1]),
				fmt.Sprintf("%s/%s", listenerB.Name, ruleNames[0]),
				fmt.Sprintf("%s/%s", listenerB.Name, ruleNames[1]),
			}, programmedHTTPRoutePolicyRulesAnnotation([]gatewayv1.Listener{listenerA, listenerB}, ruleNames))
		})
	})

	t.Run("parseProgrammedHTTPRoutePolicyRules", func(t *testing.T) {
		t.Run("parses listener scoped and legacy policy rules", func(t *testing.T) {
			fake := faker.New()
			listenerName := "listener-" + fake.Lorem().Word()
			scopedRule := "scoped-" + fake.Lorem().Word()
			legacyRule := "legacy-" + fake.Lorem().Word()

			assert.Equal(t, []programmedHTTPRoutePolicyRule{
				{
					listenerName: listenerName,
					ruleName:     scopedRule,
				},
				{
					ruleName: legacyRule,
				},
			}, parseProgrammedHTTPRoutePolicyRules(
				fmt.Sprintf(" %s/%s , %s ,,", listenerName, scopedRule, legacyRule),
			))
			assert.Empty(t, parseProgrammedHTTPRoutePolicyRules(" ,, "))
		})
	})

	t.Run("removeL7RoutePolicyRules", func(t *testing.T) {
		t.Run("removes previous policy rules per listener", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			listeners := []gatewayv1.Listener{
				{Name: "b-listener"},
				{Name: "a-listener"},
			}
			ociLBModel := NewMockociLoadBalancerModel(t)
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    "a-listener",
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{"legacy-rule", "a-rule"},
			}).Return(nil).Once()
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    "b-listener",
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{"legacy-rule", "b-rule"},
			}).Return(nil).Once()

			err := removeL7RoutePolicyRules(
				t.Context(),
				ociLBModel,
				loadBalancerID,
				listeners,
				"legacy-rule,b-listener/b-rule,a-listener/a-rule",
			)

			require.NoError(t, err)
		})

		t.Run("wraps routing policy removal errors", func(t *testing.T) {
			loadBalancerID := "ocid1.loadbalancer.oc1.." + faker.New().UUID().V4()
			listeners := []gatewayv1.Listener{{Name: "https"}}
			wantErr := errors.New("commit failed")
			ociLBModel := NewMockociLoadBalancerModel(t)
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    "https",
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{"old-rule"},
			}).Return(wantErr).Once()

			err := removeL7RoutePolicyRules(t.Context(), ociLBModel, loadBalancerID, listeners, "https/old-rule")

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to remove route policy rules for listener https")
		})
	})

	t.Run("deprovisionDetachedL7Route", func(t *testing.T) {
		t.Run("removes finalizer when load balancer annotation is missing", func(t *testing.T) {
			called := false
			err := deprovisionDetachedL7Route(t.Context(), nil, deprovisionDetachedL7RouteParams{
				route:     &gatewayv1.HTTPRoute{},
				routeKind: "HTTPRoute",
				removeFinalizer: func(context.Context) error {
					called = true
					return nil
				},
			})

			require.NoError(t, err)
			assert.True(t, called)
		})

		t.Run("returns policy rule cleanup errors", func(t *testing.T) {
			wantErr := errors.New("policy cleanup failed")
			route := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps",
					Name:      "api",
					Annotations: map[string]string{
						HTTPRouteProgrammedPolicyRulesAnnotation: "http/rule",
					},
				},
			}
			ociLBModel := NewMockociLoadBalancerModel(t)
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  "lb-id",
				listenerName:    "http",
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{"rule"},
			}).Return(wantErr).Once()

			err := deprovisionDetachedL7Route(t.Context(), ociLBModel, deprovisionDetachedL7RouteParams{
				route:                 route,
				routeKind:             "HTTPRoute",
				policyRulesAnnotation: HTTPRouteProgrammedPolicyRulesAnnotation,
				loadBalancerID:        "lb-id",
				removeFinalizer:       func(context.Context) error { return nil },
			})

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("deduplicates backend refs and returns backend set cleanup errors", func(t *testing.T) {
			wantErr := errors.New("backend cleanup failed")
			route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "api"}}
			backendRef := makeRandomBackendRef().BackendRef
			ociLBModel := NewMockociLoadBalancerModel(t)
			ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
				loadBalancerID: "lb-id",
				routeNamespace: "apps",
				backendRef:     backendRef,
			}).Return(wantErr).Once()

			err := deprovisionDetachedL7Route(t.Context(), ociLBModel, deprovisionDetachedL7RouteParams{
				route:          route,
				routeKind:      "HTTPRoute",
				loadBalancerID: "lb-id",
				backendRefs:    []gatewayv1.BackendRef{backendRef, backendRef},
				removeFinalizer: func(context.Context) error {
					t.Fatal("finalizer should not be removed when backend cleanup fails")
					return nil
				},
			})

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("l7 route conflict helpers", func(t *testing.T) {
		fake := faker.New()
		listenerHostname := gatewayv1.Hostname("*.example.com")
		grpcListener := gatewayv1.Listener{Name: "grpc", Hostname: &listenerHostname, Port: 443}
		webListener := gatewayv1.Listener{Name: "web", Port: 80}
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns", Name: "shared-gateway"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{grpcListener, webListener},
			},
		}
		parentNamespace := gatewayv1.Namespace(gateway.Namespace)
		parentRef := gatewayv1.ParentReference{
			Namespace:   &parentNamespace,
			Name:        gatewayv1.ObjectName(gateway.Name),
			SectionName: &grpcListener.Name,
		}
		current := l7RouteCandidate{
			identity: l7RouteIdentity{
				kind:              l7HTTPRouteKind,
				namespace:         "routes",
				name:              "api",
				creationTimestamp: metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)),
			},
			parentRefs: []gatewayv1.ParentReference{parentRef},
			hostnames:  []gatewayv1.Hostname{"api.example.com"},
		}
		olderOpposite := l7RouteCandidate{
			identity: l7RouteIdentity{
				kind:              l7HTTPRouteKind,
				namespace:         "routes",
				name:              "grpc",
				creationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			},
			parentRefs: []gatewayv1.ParentReference{parentRef},
			hostnames:  []gatewayv1.Hostname{"*.example.com"},
		}

		winner, conflicted, err := checkL7RouteConflict(t.Context(), checkL7RouteConflictParams{
			gateway:               gateway,
			matchedListeners:      []gatewayv1.Listener{grpcListener},
			current:               current,
			oppositeRouteListName: "GRPCRoutes",
			listOppositeRoutes: func(context.Context) ([]l7RouteCandidate, error) {
				return []l7RouteCandidate{olderOpposite}, nil
			},
		})

		require.NoError(t, err)
		assert.True(t, conflicted)
		assert.Equal(t, olderOpposite.identity, winner.identity)

		olderOpposite.identity.kind = l7GRPCRouteKind
		winner, conflicted, err = checkL7RouteConflict(t.Context(), checkL7RouteConflictParams{
			gateway:               gateway,
			matchedListeners:      []gatewayv1.Listener{grpcListener},
			current:               current,
			oppositeRouteListName: "GRPCRoutes",
			listOppositeRoutes: func(context.Context) ([]l7RouteCandidate, error) {
				return []l7RouteCandidate{olderOpposite}, nil
			},
		})
		require.NoError(t, err)
		assert.False(t, conflicted)
		assert.Empty(t, winner)

		assert.False(t, l7RouteHostnamesIntersect([]gatewayv1.Hostname{}, []gatewayv1.Hostname{"api.example.com"}))
		assert.True(t, l7HostnamePatternsIntersect("API.EXAMPLE.COM", "api.example.com"))
		assert.False(t, l7HostnamePatternsIntersect("*.example.com", "example.com"))
		assert.False(t, l7HostnamePatternsIntersect("api.example.com", "web.example.com"))
		assert.True(t, l7HostnamePatternsIntersect("*.foo.example.com", "*.example.com"))
		assert.ElementsMatch(t, []gatewayv1.SectionName{grpcListener.Name}, l7RouteAttachedListenerNames(
			gateway,
			nil,
			[]gatewayv1.ParentReference{parentRef, {Name: "other"}},
			"routes",
		))
		assert.ElementsMatch(
			t,
			[]gatewayv1.SectionName{grpcListener.Name, webListener.Name},
			l7RouteAttachedListenerNames(
				gateway,
				nil,
				[]gatewayv1.ParentReference{{Namespace: &parentNamespace, Name: gatewayv1.ObjectName(gateway.Name)}},
				"routes",
			),
		)
		assert.Equal(
			t,
			[]gatewayv1.Hostname{listenerHostname},
			l7RouteHostnamesForListener(nil, grpcListener),
		)
		assert.Equal(
			t,
			[]gatewayv1.Hostname{""},
			l7RouteHostnamesForListener(nil, webListener),
		)
		assert.Empty(t, l7RouteHostnamesForListener(
			[]gatewayv1.Hostname{"api.other.test"},
			grpcListener,
		))
		assert.False(t, l7RoutesShareListenerHostname(
			gateway,
			nil,
			[]gatewayv1.Listener{webListener},
			current,
			olderOpposite,
		))
		assert.False(t, l7RoutesShareListenerHostname(
			gateway,
			nil,
			[]gatewayv1.Listener{{Name: "missing"}},
			current,
			olderOpposite,
		))
		listenerSetKind := gatewayv1.Kind("ListenerSet")
		listenerSet := gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "routes",
				Name:              "team-a",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{
					Namespace: &parentNamespace,
					Name:      gatewayv1.ObjectName(gateway.Name),
				},
				Listeners: []gatewayv1.ListenerEntry{{
					Name:     "api",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     8443,
					Hostname: lo.ToPtr(gatewayv1.Hostname("api.example.com")),
				}},
			},
		}
		listenerSetParentRef := gatewayv1.ParentReference{
			Kind:        &listenerSetKind,
			Name:        gatewayv1.ObjectName(listenerSet.Name),
			SectionName: lo.ToPtr(gatewayv1.SectionName("api")),
		}
		effectiveListeners := effectiveListenersForGateway(gateway, []gatewayv1.ListenerSet{listenerSet})
		matchedListenerSetListeners := effectiveListenersForParentRef(
			resolvedGatewayDetails{gateway: gateway, effectiveListeners: effectiveListeners},
			listenerSetParentRef,
			current.identity.namespace,
			func(ref gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
				return ref.SectionName != nil && listener.Name == *ref.SectionName
			},
		)
		listenerSetCurrent := current
		listenerSetCurrent.parentRefs = []gatewayv1.ParentReference{listenerSetParentRef}
		listenerSetCurrent.hostnames = []gatewayv1.Hostname{"api.example.com"}
		olderListenerSetRoute := listenerSetCurrent
		olderListenerSetRoute.identity.name = "aaa"
		olderListenerSetRoute.identity.creationTimestamp = metav1.NewTime(
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		)

		winner, conflicted = l7RouteConflictingWinner(l7RouteConflictParams{
			gateway:            gateway,
			effectiveListeners: effectiveListeners,
			matchedListeners:   matchedListenerSetListeners,
			current:            listenerSetCurrent,
			oppositeRoutes:     []l7RouteCandidate{olderListenerSetRoute},
		})
		assert.True(t, conflicted)
		assert.Equal(t, olderListenerSetRoute.identity, winner.identity)

		newerOpposite := olderOpposite
		newerOpposite.identity.kind = current.identity.kind
		newerOpposite.identity.creationTimestamp = metav1.NewTime(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
		winner, conflicted = l7RouteConflictingWinner(l7RouteConflictParams{
			gateway:          gateway,
			matchedListeners: []gatewayv1.Listener{grpcListener},
			current:          current,
			oppositeRoutes:   []l7RouteCandidate{newerOpposite},
		})
		assert.False(t, conflicted)
		assert.Empty(t, winner)

		assert.True(t, l7RouteWins(
			l7RouteIdentity{
				kind:              l7HTTPRouteKind,
				namespace:         "aaa",
				name:              "route",
				creationTimestamp: current.identity.creationTimestamp,
			},
			l7RouteIdentity{
				kind:              l7GRPCRouteKind,
				namespace:         "bbb",
				name:              "route",
				creationTimestamp: current.identity.creationTimestamp,
			},
		))
		assert.True(t, l7RouteWins(
			l7RouteIdentity{
				kind:              l7GRPCRouteKind,
				namespace:         "same",
				name:              "route",
				creationTimestamp: current.identity.creationTimestamp,
			},
			l7RouteIdentity{
				kind:              l7HTTPRouteKind,
				namespace:         "same",
				name:              "route",
				creationTimestamp: current.identity.creationTimestamp,
			},
		))

		wantErr := errors.New(fake.Lorem().Sentence(10))
		_, _, err = checkL7RouteConflict(t.Context(), checkL7RouteConflictParams{
			matchedListeners:      []gatewayv1.Listener{grpcListener},
			oppositeRouteListName: "GRPCRoutes",
			listOppositeRoutes: func(context.Context) ([]l7RouteCandidate, error) {
				return nil, wantErr
			},
		})
		require.ErrorIs(t, err, wantErr)

		deps := newMockDeps(t)
		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		route := makeRandomHTTPRoute()
		parentStatuses := []gatewayv1.RouteParentStatus{{
			ParentRef:      parentRef,
			ControllerName: ControllerClassName,
		}}
		k8sClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			condition := meta.FindStatusCondition(
				parentStatuses[0].Conditions,
				string(gatewayv1.RouteConditionAccepted),
			)
			return obj.GetName() == route.Name &&
				condition != nil &&
				condition.Status == metav1.ConditionFalse
		})).Return(nil)

		err = rejectL7Route(t.Context(), k8sClient, rejectL7RouteParams{
			resource:       &route,
			parentStatuses: &parentStatuses,
			gatewayClass: gatewayv1.GatewayClass{
				Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			},
			matchedRef: parentRef,
			message:    "conflicted",
			routeKind:  "HTTPRoute",
		})

		require.NoError(t, err)
	})

	t.Run("resolveRequest", func(t *testing.T) {
		t.Run("cleans deleting programmed route with no resolved parent", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			loadBalancerID := "lb-" + fake.UUID().V4()
			listenerName := "listener-" + fake.Lorem().Word()
			ruleName := "rule-" + fake.Lorem().Word()
			deleteTime := metav1.Now()
			backendRef := makeRandomBackendRef()
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)
			route.DeletionTimestamp = &deleteTime
			route.Finalizers = []string{HTTPRouteProgrammedFinalizer}
			route.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation:  listenerName + "/" + ruleName,
				L7RouteProgrammedLoadBalancerIDAnnotation: loadBalancerID,
			}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}}

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    listenerName,
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(nil).Once()
			ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				routeNamespace: route.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(nil).Once()
			k8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updated, ok := obj.(*gatewayv1.HTTPRoute)
				return ok &&
					!controllerutil.ContainsFinalizer(updated, HTTPRouteProgrammedFinalizer) &&
					updated.Annotations[HTTPRouteProgrammedPolicyRulesAnnotation] == "" &&
					updated.Annotations[L7RouteProgrammedLoadBalancerIDAnnotation] == ""
			})).Return(nil).Once()

			got, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, got)
		})

		t.Run("wraps detached cleanup errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			deleteTime := metav1.Now()
			route := makeRandomHTTPRoute()
			route.DeletionTimestamp = &deleteTime
			route.Finalizers = []string{HTTPRouteProgrammedFinalizer}
			route.Annotations = map[string]string{
				L7RouteProgrammedLoadBalancerIDAnnotation: "lb-id",
			}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}}
			wantErr := errors.New("update failed")

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)
			k8sClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.HTTPRoute")).
				Return(wantErr).
				Once()

			_, err := model.resolveRequest(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to deprovision detached HTTPRoute")
		})

		t.Run("relevant parent", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			otherRef1 := makeRandomParentRef()
			otherRef2 := makeRandomParentRef()
			workingRef := makeRandomParentRef(
				randomParentRefWithRandomPortOpt(),
			)

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefsOpt(otherRef1, otherRef2, workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef1.Namespace)),
						Name:      string(otherRef1.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef2.Namespace)),
						Name:      string(otherRef2.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			wantListeners := makeFewRandomListeners()

			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(workingRef),
					randomGatewayWithListenersOpt(wantListeners...),
				),
			)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef.Namespace)),
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			})

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "should resolve exactly one parent")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(workingRef.Namespace)),
				Name:      string(workingRef.Name),
			}
			require.Contains(t, results, parentKey)
			receiver := results[parentKey]

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      workingRef.Name,
				Namespace: workingRef.Namespace,
			}, receiver.matchedRef)
			assert.Equal(t, wantListeners, receiver.matchedListeners)
		})

		t.Run("relevant parent with section name", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			wantSectionName := gatewayv1.SectionName(fake.Lorem().Word())
			workingRef := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &wantSectionName },
			)

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			matchingListener := makeRandomListener(
				randomListenerWithNameOpt(wantSectionName),
			)
			otherListener1 := makeRandomListener()
			otherListener2 := makeRandomListener()
			wantListeners := []gatewayv1.Listener{matchingListener}

			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(workingRef),
					randomGatewayWithListenersOpt(otherListener1, matchingListener, otherListener2),
				),
			)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef.Namespace)),
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			})

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "should resolve exactly one parent")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(workingRef.Namespace)),
				Name:      string(workingRef.Name),
			}
			require.Contains(t, results, parentKey)
			receiver := results[parentKey]

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      workingRef.Name,
				Namespace: workingRef.Namespace,
				Group:     workingRef.Group,
				Kind:      workingRef.Kind,
			}, receiver.matchedRef)
			assert.Equal(t, wantListeners, receiver.matchedListeners)
		})

		t.Run("relevant ListenerSet parent with section name", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			listenerSetKind := gatewayv1.Kind("ListenerSet")
			sectionName := gatewayv1.SectionName("https")
			parentNamespace := gatewayv1.Namespace("infra")
			fromAll := gatewayv1.NamespacesFromAll
			parentRef := gatewayv1.ParentReference{
				Kind:        &listenerSetKind,
				Name:        "extra",
				SectionName: &sectionName,
			}
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(parentRef),
			)
			route.Namespace = "apps"
			route.Name = "api"
			req := reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: route.Namespace,
				Name:      route.Name,
			}}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: string(parentRef.Name)},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{
						Namespace: &parentNamespace,
						Name:      "edge",
					},
					Listeners: []gatewayv1.ListenerEntry{
						{Name: sectionName, Port: 443, Protocol: gatewayv1.HTTPSProtocolType},
					},
				},
			}
			gatewayData := makeRandomAcceptedGatewayDetails()
			gatewayData.gateway.Namespace = string(parentNamespace)
			gatewayData.gateway.Name = "edge"
			gatewayData.gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
				Namespaces: &gatewayv1.ListenerNamespaces{From: &fromAll},
			}
			gatewayData.gateway.Spec.Listeners = nil
			gatewayData.effectiveListeners = nil

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)
			setupClientGet(t, deps.K8sClient, types.NamespacedName{
				Namespace: listenerSet.Namespace,
				Name:      listenerSet.Name,
			}, listenerSet)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "infra", Name: "edge"}},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			})

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).
						Elem().
						FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.ListenerSet{listenerSet}))
					return nil
				})
			setupClientGet(t, mockClient, types.NamespacedName{Name: listenerSet.Namespace}, corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: listenerSet.Namespace},
			})

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1)
			receiver := results[types.NamespacedName{Namespace: "infra", Name: "edge"}]
			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, gatewayv1.ParentReference{
				Kind: &listenerSetKind,
				Name: parentRef.Name,
			}, receiver.matchedRef)
			require.Len(t, receiver.gatewayDetails.listenerSets, 1)
			require.Len(t, receiver.matchedListeners, 1)
			assert.NotEqual(t, sectionName, receiver.matchedListeners[0].Name)
			assert.Equal(t, gatewayv1.HTTPSProtocolType, receiver.matchedListeners[0].Protocol)
		})

		t.Run("relevant parent with multiple sections", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			sectionName1 := gatewayv1.SectionName(fake.Lorem().Word())
			sectionName2 := gatewayv1.SectionName(fake.Lorem().Word())
			require.NotEqual(t, sectionName1, sectionName2) // Ensure different sections

			// Two refs pointing to the same gateway, but different sections
			workingRef1 := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &sectionName1 },
			)
			workingRef2 := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) {
					p.Name = workingRef1.Name
					p.Namespace = workingRef1.Namespace
					p.SectionName = &sectionName2
				},
			)

			// A third ref pointing to a different gateway
			otherRef := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefsOpt(workingRef1, workingRef2, otherRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			// Define listeners for the gateway
			listener1 := makeRandomListener(randomListenerWithNameOpt(sectionName1))
			listener2 := makeRandomListener(randomListenerWithNameOpt(sectionName2))
			otherListener := makeRandomListener() // This one shouldn't be matched
			allGatewayListeners := []gatewayv1.Listener{otherListener, listener1, listener2}
			wantListeners := []gatewayv1.Listener{listener1, listener2} // Only these should be in the final result

			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(workingRef1),
					randomGatewayWithListenersOpt(allGatewayListeners...),
				),
			)

			// Mock the gateway model response for the targeted gateway
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef1.Namespace)),
						Name:      string(workingRef1.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			}).Times(2) // Expect it to be called twice for the two refs

			// Mock the gateway model response for the other gateway (should resolve false)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef.Namespace)),
						Name:      string(otherRef.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "should resolve exactly one parent gateway")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(workingRef1.Namespace)),
				Name:      string(workingRef1.Name),
			}
			require.Contains(t, results, parentKey)
			receiver := results[parentKey]

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      workingRef1.Name,
				Namespace: workingRef1.Namespace,
				Group:     workingRef1.Group,
				Kind:      workingRef1.Kind,
			}, receiver.matchedRef)
			assert.ElementsMatch(t, wantListeners, receiver.matchedListeners)
		})

		t.Run("relevant parent with non-matching section name", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			nonMatchingSectionName := gatewayv1.SectionName(fake.Lorem().Word())
			refWithNonMatchingSection := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &nonMatchingSectionName },
			)
			refWithoutSection := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefsOpt(refWithNonMatchingSection, refWithoutSection),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			listener1 := makeRandomListener()
			gatewayData1 := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(refWithNonMatchingSection),
					randomGatewayWithListenersOpt(listener1),
				),
			)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(refWithNonMatchingSection.Namespace)),
						Name:      string(refWithNonMatchingSection.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData1
				return true, nil
			})

			allListeners := makeFewRandomListeners()
			gatewayData2 := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(refWithoutSection),
					randomGatewayWithListenersOpt(allListeners...),
				),
			)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(refWithoutSection.Namespace)),
						Name:      string(refWithoutSection.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData2
				return true, nil
			})

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "at least one parent should resolve")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(refWithoutSection.Namespace)),
				Name:      string(refWithoutSection.Name),
			}
			require.Contains(t, results, parentKey)
			res := results[parentKey]

			assert.Equal(t, *gatewayData2, res.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      refWithoutSection.Name,
				Namespace: refWithoutSection.Namespace,
				Group:     refWithoutSection.Group,
				Kind:      refWithoutSection.Kind,
			}, res.matchedRef)
			assert.Equal(t, allListeners, res.matchedListeners)
		})

		t.Run("no relevant parent when section name does not match", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			nonMatchingSectionName := gatewayv1.SectionName(fake.Lorem().Word())
			workingRef := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &nonMatchingSectionName },
			)

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			listener1 := makeRandomListener()
			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithListenersOpt(listener1),
				),
			)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef.Namespace)),
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			})

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, results, "parent should not resolve when section name does not match any listener")
		})

		t.Run("no relevant parent", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			otherRef1 := makeRandomParentRef()
			otherRef2 := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(otherRef1),
				randomHTTPRouteWithRandomParentRefOpt(otherRef2),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef1.Namespace)),
						Name:      string(otherRef1.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef2.Namespace)),
						Name:      string(otherRef2.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, results, "parent should not be resolved")
		})

		t.Run("no such route", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).Return(
				apierrors.NewNotFound(
					schema.GroupResource{Group: gatewayv1.GroupName, Resource: "HTTPRoute"},
					req.NamespacedName.String(),
				),
			)

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, results, "parent should not be resolved")
		})

		t.Run("client get error", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			expectedErr := errors.New(fake.Lorem().Sentence(10))
			mockK8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).Return(expectedErr)

			results, err := model.resolveRequest(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.Nil(t, results, "should return nil results on error")
		})

		t.Run("gateway resolve error", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: fake.Lorem().Word(),
					Name:      fake.Lorem().Word(),
				},
			}
			workingRef := makeRandomParentRef()
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			expectedErr := errors.New(fake.Lorem().Sentence(10))
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef.Namespace)),
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).Return(false, expectedErr)

			results, err := model.resolveRequest(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.Nil(t, results, "should return nil results on error")
		})
	})

	t.Run("listGRPCRouteConflictCandidates", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newHTTPRouteModel(deps)
		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		deletionTimestamp := metav1.Now()
		activeRoute := makeRandomGRPCRoute()
		activeRoute.Spec.ParentRefs = []gatewayv1.ParentReference{makeRandomParentRef()}
		activeRoute.Spec.Hostnames = []gatewayv1.Hostname{"api.example.com"}
		deletedRoute := makeRandomGRPCRoute()
		deletedRoute.DeletionTimestamp = &deletionTimestamp

		k8sClient.EXPECT().List(t.Context(), &gatewayv1.GRPCRouteList{}).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				list.(*gatewayv1.GRPCRouteList).Items = []gatewayv1.GRPCRoute{activeRoute, deletedRoute}
				return nil
			})

		got, err := model.listGRPCRouteConflictCandidates(t.Context())

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, l7RouteIdentity{
			kind:              l7GRPCRouteKind,
			namespace:         activeRoute.Namespace,
			name:              activeRoute.Name,
			creationTimestamp: activeRoute.CreationTimestamp,
		}, got[0].identity)
		assert.Equal(t, activeRoute.Spec.ParentRefs, got[0].parentRefs)
		assert.Equal(t, activeRoute.Spec.Hostnames, got[0].hostnames)
	})

	t.Run("acceptRoute", func(t *testing.T) {
		t.Run("add new accepted parent", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			routeData := resolvedRouteDetails{
				matchedRef: makeRandomParentRef(
					randomParentRefWithRandomSectionNameOpt(),
					randomParentRefWithRandomPortOpt(),
				),
				gatewayDetails: resolvedGatewayDetails{
					gateway: *newRandomGateway(),
					gatewayClass: *newRandomGatewayClass(
						randomGatewayClassWithControllerNameOpt(gatewayv1.GatewayController(fake.Lorem().Word())),
					),
				},
				httpRoute: makeRandomHTTPRoute(),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					var ok bool
					updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)
					return assert.True(t, ok)
				})).
				Return(nil)

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)

			require.NoError(t, err)
			assert.Same(t, acceptedRoute, updatedRoute)

			assert.Len(t, updatedRoute.Status.Parents, 1)

			acceptedParent := updatedRoute.Status.Parents[0]
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      routeData.matchedRef.Name,
				Namespace: routeData.matchedRef.Namespace,
			}, acceptedParent.ParentRef)
			assert.Equal(t,
				routeData.gatewayDetails.gatewayClass.Spec.ControllerName,
				acceptedParent.ControllerName,
			)

			gotCondition := meta.FindStatusCondition(
				acceptedParent.Conditions,
				string(gatewayv1.RouteConditionAccepted),
			)
			require.NotNil(t, gotCondition)
			assert.False(t, gotCondition.LastTransitionTime.IsZero())
			assert.Equal(t, &metav1.Condition{
				Type:               string(gatewayv1.RouteConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.RouteReasonAccepted),
				ObservedGeneration: routeData.httpRoute.Generation,
				LastTransitionTime: gotCondition.LastTransitionTime,
				Message:            fmt.Sprintf("Route accepted by %s", routeData.gatewayDetails.gateway.Name),
			}, gotCondition)
		})

		t.Run("accepts when an older GRPCRoute has an overlapping listener hostname", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			listenerName := gatewayv1.SectionName("https")
			hostname := gatewayv1.Hostname("grpc.example.com")
			gateway := newRandomGateway(randomGatewayWithListenersOpt(gatewayv1.Listener{
				Name:     listenerName,
				Hostname: &hostname,
				Port:     443,
				Protocol: gatewayv1.HTTPSProtocolType,
			}))
			gatewayNamespace := gatewayv1.Namespace(gateway.Namespace)
			parentRef := gatewayv1.ParentReference{
				Namespace:   &gatewayNamespace,
				Name:        gatewayv1.ObjectName(gateway.Name),
				SectionName: &listenerName,
			}
			currentRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNamespaceOpt(gateway.Namespace),
				randomHTTPRouteWithRandomParentRefOpt(parentRef),
			)
			currentRoute.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
			currentRoute.Spec.Hostnames = []gatewayv1.Hostname{hostname}
			olderGRPCRoute := gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         gateway.Namespace,
					Name:              "grpc-route",
					CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
				},
				Spec: gatewayv1.GRPCRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parentRef}},
					Hostnames:       []gatewayv1.Hostname{hostname},
				},
			}

			k8sClient.EXPECT().List(t.Context(), &gatewayv1.GRPCRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					list.(*gatewayv1.GRPCRouteList).Items = []gatewayv1.GRPCRoute{olderGRPCRoute}
					return nil
				})
			config := makeRandomGatewayConfig()
			k8sClient.EXPECT().Status().Return(mockStatusWriter)
			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				route, ok := obj.(*gatewayv1.HTTPRoute)
				if !ok {
					return false
				}
				updatedRoute = route
				parentStatus := route.Status.Parents[0]
				condition := meta.FindStatusCondition(parentStatus.Conditions, string(gatewayv1.RouteConditionAccepted))
				return condition != nil &&
					condition.Status == metav1.ConditionTrue &&
					condition.Reason == string(gatewayv1.RouteReasonAccepted)
			})).Return(nil)

			got, err := model.acceptRoute(t.Context(), resolvedRouteDetails{
				gatewayDetails: resolvedGatewayDetails{
					gateway: *gateway,
					gatewayClass: gatewayv1.GatewayClass{
						Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
					},
					config: config,
				},
				httpRoute:        currentRoute,
				matchedRef:       parentRef,
				matchedListeners: []gatewayv1.Listener{gateway.Spec.Listeners[0]},
			})

			require.NoError(t, err)
			assert.Same(t, updatedRoute, got)
		})

		t.Run("rejectRoute sets conflicted condition", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			parentRef := makeRandomParentRef()
			httpRoute := makeRandomHTTPRoute()
			winner := l7RouteCandidate{
				identity: l7RouteIdentity{
					kind:      l7HTTPRouteKind,
					namespace: "routes",
					name:      "winner",
				},
			}
			wantMessage := l7RouteConflictMessage(winner)

			k8sClient.EXPECT().Status().Return(mockStatusWriter)
			mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				route, ok := obj.(*gatewayv1.HTTPRoute)
				if !ok {
					return false
				}
				require.Len(t, route.Status.Parents, 1)
				parentStatus := route.Status.Parents[0]
				condition := meta.FindStatusCondition(parentStatus.Conditions, string(gatewayv1.RouteConditionAccepted))
				return parentRefSameTarget(parentStatus.ParentRef, parentRef) &&
					condition != nil &&
					condition.Status == metav1.ConditionFalse &&
					condition.Reason == string(routeReasonConflicted) &&
					condition.Message == wantMessage
			})).Return(nil)

			err := model.rejectRoute(t.Context(), resolvedRouteDetails{
				gatewayDetails: resolvedGatewayDetails{
					gatewayClass: gatewayv1.GatewayClass{
						Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
					},
				},
				httpRoute:  httpRoute,
				matchedRef: parentRef,
			}, wantMessage)

			require.NoError(t, err)
		})

		t.Run("set condition of existing parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gatewayClass := newRandomGatewayClass()
			existingParentStatus := makeRandomRouteParentStatus(
				func(s *gatewayv1.RouteParentStatus) {
					s.ParentRef = makeRandomParentRef(
						randomParentRefWithRandomSectionNameOpt(),
						randomParentRefWithRandomPortOpt(),
					)
					s.ControllerName = gatewayClass.Spec.ControllerName
				},
			)
			routeData := resolvedRouteDetails{
				matchedRef: existingParentStatus.ParentRef,
				gatewayDetails: resolvedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(func(rps *gatewayv1.RouteParentStatus) {
								rps.ControllerName = gatewayClass.Spec.ControllerName
							}),
							makeRandomRouteParentStatus(),
							existingParentStatus,
							makeRandomRouteParentStatus(),
						}
					},
				),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					var ok bool
					updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)
					return assert.True(t, ok)
				})).
				Return(nil)

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)
			require.NoError(t, err)
			assert.Same(t, acceptedRoute, updatedRoute)

			assert.Len(t, updatedRoute.Status.Parents, 4)

			acceptedParent, found := lo.Find(updatedRoute.Status.Parents, func(s gatewayv1.RouteParentStatus) bool {
				return s.ControllerName == gatewayClass.Spec.ControllerName &&
					parentRefSameTarget(s.ParentRef, routeData.matchedRef)
			})
			require.True(t, found)

			gotCondition := meta.FindStatusCondition(
				acceptedParent.Conditions,
				string(gatewayv1.RouteConditionAccepted),
			)
			require.NotNil(t, gotCondition)
			assert.False(t, gotCondition.LastTransitionTime.IsZero())
			assert.Equal(t, &metav1.Condition{
				Type:               string(gatewayv1.RouteConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.RouteReasonAccepted),
				ObservedGeneration: routeData.httpRoute.Generation,
				LastTransitionTime: gotCondition.LastTransitionTime,
				Message:            fmt.Sprintf("Route accepted by %s", routeData.gatewayDetails.gateway.Name),
			}, gotCondition)
		})
		t.Run("should not update if already accepted", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gatewayClass := newRandomGatewayClass()
			routeGeneration := rand.Int64N(1000000)
			existingParentStatus := makeRandomRouteParentStatus(
				func(s *gatewayv1.RouteParentStatus) {
					s.ControllerName = gatewayClass.Spec.ControllerName
					meta.SetStatusCondition(&s.Conditions, metav1.Condition{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonAccepted),
						ObservedGeneration: routeGeneration,
					})
				},
			)
			routeData := resolvedRouteDetails{
				matchedRef: existingParentStatus.ParentRef,
				gatewayDetails: resolvedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Generation = routeGeneration
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(),
							makeRandomRouteParentStatus(),
							existingParentStatus,
							makeRandomRouteParentStatus(),
						}
					},
				),
			}

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)
			require.NoError(t, err)
			assert.Equal(t, acceptedRoute, &routeData.httpRoute)
		})
		t.Run("should update if generation mismatch", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gatewayClass := newRandomGatewayClass()
			routeGeneration := rand.Int64N(1000000)
			existingParentStatus := makeRandomRouteParentStatus(
				func(s *gatewayv1.RouteParentStatus) {
					s.ControllerName = gatewayClass.Spec.ControllerName
					meta.SetStatusCondition(&s.Conditions, metav1.Condition{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonAccepted),
						ObservedGeneration: routeGeneration - 1,
					})
				},
			)
			routeData := resolvedRouteDetails{
				matchedRef: existingParentStatus.ParentRef,
				gatewayDetails: resolvedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Generation = routeGeneration
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(),
							makeRandomRouteParentStatus(),
							existingParentStatus,
							makeRandomRouteParentStatus(),
						}
					},
				),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					var ok bool
					updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)
					return assert.True(t, ok)
				})).
				Return(nil)

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)
			require.NoError(t, err)
			assert.Same(t, acceptedRoute, updatedRoute)

			acceptedParent, found := lo.Find(updatedRoute.Status.Parents, func(s gatewayv1.RouteParentStatus) bool {
				return s.ControllerName == gatewayClass.Spec.ControllerName
			})
			require.True(t, found)

			gotCondition := meta.FindStatusCondition(
				acceptedParent.Conditions,
				string(gatewayv1.RouteConditionAccepted),
			)
			require.NotNil(t, gotCondition)
			assert.Equal(t, routeData.httpRoute.Generation, gotCondition.ObservedGeneration)
			assert.Equal(t, metav1.ConditionTrue, gotCondition.Status)
			assert.Equal(t, string(gatewayv1.RouteReasonAccepted), gotCondition.Reason)
		})

		t.Run("client status update error", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			routeData := resolvedRouteDetails{
				gatewayDetails: resolvedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *newRandomGatewayClass(),
				},
				httpRoute: makeRandomHTTPRoute(),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			expectedErr := errors.New(fake.Lorem().Sentence(10))

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.Anything).
				Return(expectedErr)

			_, err := model.acceptRoute(t.Context(), routeData)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("resolveBackendRefs", func(t *testing.T) {
		t.Run("valid backend refs", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			backendRefs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}
			backendRefs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(randomBackendRefWithNillNamespaceOpt()),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs1...),
					),
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs2...),
					),
				),
			)

			allBackendRefs := make([]gatewayv1.HTTPBackendRef, 0, len(backendRefs1)+len(backendRefs2))
			allBackendRefs = append(allBackendRefs, backendRefs1...)
			allBackendRefs = append(allBackendRefs, backendRefs2...)
			services := lo.Map(allBackendRefs, func(ref gatewayv1.HTTPBackendRef, _ int) corev1.Service {
				return makeRandomService(randomServiceFromBackendRef(ref, &httpRoute))
			})

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			for _, service := range services {
				setupClientGet(t, mockK8sClient, types.NamespacedName{
					Namespace: service.Namespace,
					Name:      service.Name,
				}, service)
			}

			resolvedBackendRefs, err := model.resolveBackendRefs(t.Context(), resolveBackendRefsParams{
				httpRoute: httpRoute,
			})

			require.NoError(t, err)
			require.Len(t, resolvedBackendRefs, len(services))
			for _, service := range services {
				fullName := types.NamespacedName{
					Namespace: service.Namespace,
					Name:      service.Name,
				}
				assert.Equal(t, service, resolvedBackendRefs[fullName.String()])
			}
		})

		t.Run("backend service get error", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef),
					),
				),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			expectedErr := errors.New(fake.Lorem().Sentence(10))
			mockK8sClient.EXPECT().Get(t.Context(), mock.Anything, mock.Anything).Return(expectedErr)

			_, err := model.resolveBackendRefs(t.Context(), resolveBackendRefsParams{
				httpRoute: httpRoute,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("programRoute", func(t *testing.T) {
		t.Run("successfully programs route with multiple listeners", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			// Create HTTP route with multiple rules
			backendRefs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs[0])),
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs[1])),
				),
			)

			knownServices := []corev1.Service{
				makeRandomService(randomServiceFromBackendRef(backendRefs[0], &httpRoute)),
				makeRandomService(randomServiceFromBackendRef(backendRefs[1], &httpRoute)),
			}
			knownServicesByName := lo.SliceToMap(knownServices, func(s corev1.Service) (string, corev1.Service) {
				return types.NamespacedName{
					Namespace: s.Namespace,
					Name:      s.Name,
				}.String(), s
			})

			listeners := makeFewRandomListeners()

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    knownServicesByName,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Expect reconciliation of backend sets for each backendRef.
			for _, ref := range backendRefs {
				service := knownServicesByName[backendObjectRefName(ref.BackendObjectReference, httpRoute.Namespace).String()]
				ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					service:        service,
					routeNS:        httpRoute.Namespace,
					backendRef:     ref.BackendRef,
				}).Return(nil)
			}

			// Create expected routing rules for each HTTP route rule
			expectedRules := make([]loadbalancer.RoutingRule, 0, len(httpRoute.Spec.Rules))
			for i := range httpRoute.Spec.Rules {
				rule := makeRandomOCIRoutingRule()
				expectedRules = append(expectedRules, rule)

				ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
					httpRoute:          httpRoute,
					httpRouteRuleIndex: i,
				}).Return(rule, nil)
			}

			// Expect commitRoutingPolicyV2 to be called for each listener
			for _, listener := range listeners {
				ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					listenerName:   string(listener.Name),
					policyRules:    expectedRules,
				}).Return(nil)
			}

			_, err := model.programRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("programs separate backend sets for the same service on different ports", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()
			service := makeRandomService()
			firstPort := gatewayv1.PortNumber(8000)
			firstPort += rand.Int32N(1000)
			secondPort := firstPort + 1
			firstBackendRef := makeRandomBackendRef(
				randomBackendRefWithNameOpt(service.Name),
				randomBackendRefWithNamespaceOpt(service.Namespace),
				func(ref *gatewayv1.HTTPBackendRef) {
					ref.Port = &firstPort
				},
			)
			secondBackendRef := makeRandomBackendRef(
				randomBackendRefWithNameOpt(service.Name),
				randomBackendRefWithNamespaceOpt(service.Namespace),
				func(ref *gatewayv1.HTTPBackendRef) {
					ref.Port = &secondPort
				},
			)
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNameOpt("route-"+fake.Lorem().Word()),
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(firstBackendRef)),
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(secondBackendRef)),
				),
			)
			serviceKey := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String()
			listener := makeRandomListener()
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				service:        service,
				routeNS:        httpRoute.Namespace,
				backendRef:     firstBackendRef.BackendRef,
			}).Return(nil).Once()
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				service:        service,
				routeNS:        httpRoute.Namespace,
				backendRef:     secondBackendRef.BackendRef,
			}).Return(nil).Once()

			expectedRules := make([]loadbalancer.RoutingRule, 0, len(httpRoute.Spec.Rules))
			for i := range httpRoute.Spec.Rules {
				rule := makeRandomOCIRoutingRule()
				expectedRules = append(expectedRules, rule)
				ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
					httpRoute:          httpRoute,
					httpRouteRuleIndex: i,
				}).Return(rule, nil).Once()
			}
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				listenerName:   string(listener.Name),
				policyRules:    expectedRules,
			}).Return(nil).Once()

			_, err := model.programRoute(t.Context(), programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    map[string]corev1.Service{serviceKey: service},
				matchedListeners: []gatewayv1.Listener{listener},
			})

			require.NoError(t, err)
		})

		t.Run("removes stale backend set when backend ref port changes", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()
			newPort := gatewayv1.PortNumber(9092)
			oldPort := gatewayv1.PortNumber(9091)
			backendRef := makeRandomBackendRef(func(ref *gatewayv1.HTTPBackendRef) {
				ref.Port = &newPort
			})
			oldBackendRef := backendRef
			oldBackendRef.Port = &oldPort
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)
			oldBackendSetName := ociBackendSetNameFromBackendObjectRef(
				httpRoute.Namespace,
				oldBackendRef.BackendObjectReference,
			)
			newBackendSetName := ociBackendSetNameFromBackendObjectRef(
				httpRoute.Namespace,
				backendRef.BackendObjectReference,
			)
			require.NotEqual(t, oldBackendSetName, newBackendSetName)
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedBackendSetsAnnotation: oldBackendSetName,
			}

			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			knownServicesByName := map[string]corev1.Service{
				types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String(): service,
			}
			listener := makeRandomListener()
			rule := makeRandomOCIRoutingRule()
			rule.Name = new(ociListerPolicyRuleName(httpRoute, 0))
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				service:        service,
				routeNS:        httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(nil).Once()
			ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: 0,
			}).Return(rule, nil).Once()
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				listenerName:   string(listener.Name),
				policyRules:    []loadbalancer.RoutingRule{rule},
			}).Return(nil).Once()
			ociLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), config.Spec.LoadBalancerID, oldBackendSetName).
				Return(nil).
				Once()

			result, err := model.programRoute(t.Context(), programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    knownServicesByName,
				matchedListeners: []gatewayv1.Listener{listener},
			})

			require.NoError(t, err)
			assert.Equal(t, []string{newBackendSetName}, result.programmedBackendSets)
		})

		t.Run("clears backend SSL config when BackendTLSPolicy no longer matches", func(t *testing.T) {
			fake := faker.New()
			ociLBModel := NewMockociLoadBalancerModel(t)
			config := makeRandomGatewayConfig()
			service := makeRandomService()
			backendRef := makeRandomBackendRef(
				randomBackendRefWithNameOpt(service.Name),
				randomBackendRefWithNamespaceOpt(service.Namespace),
			)
			routeNamespace := "route-" + fake.Lorem().Word()
			listener := makeRandomListener()
			rule := makeRandomOCIRoutingRule()
			serviceKey := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String()

			ociLBModel.EXPECT().
				reconcileBackendSet(t.Context(), mock.MatchedBy(func(params reconcileBackendSetParams) bool {
					return params.loadBalancerID == config.Spec.LoadBalancerID &&
						params.service.Name == service.Name &&
						params.backendRef.Name == backendRef.Name &&
						params.manageSSLConfig &&
						params.sslConfig == nil
				})).
				Return(nil).
				Once()
			ociLBModel.EXPECT().
				commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					listenerName:   string(listener.Name),
					policyRules:    []loadbalancer.RoutingRule{rule},
				}).
				Return(nil).
				Once()

			rules, _, err := programL7RoutePolicy(t.Context(), ociLBModel, programL7RoutePolicyParams{
				loadBalancerID:   config.Spec.LoadBalancerID,
				routeName:        "http-" + fake.Lorem().Word(),
				routeNamespace:   routeNamespace,
				backendRefs:      []gatewayv1.BackendRef{backendRef.BackendRef},
				knownBackends:    map[string]corev1.Service{serviceKey: service},
				matchedListeners: []gatewayv1.Listener{listener},
				backendTLSPolicy: &stubBackendTLSPolicyModel{resolveErr: errBackendTLSPolicyNotFound},
				ruleCount:        1,
				makeRoutingRule: func(int) (loadbalancer.RoutingRule, error) {
					return rule, nil
				},
			})

			require.NoError(t, err)
			require.Len(t, rules, 1)
		})

		t.Run("deduplicates backend set reconciliation for the same backend ref", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()
			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)
			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			listener := makeRandomListener()
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				service:        service,
				routeNS:        httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(nil).Once()

			expectedRules := make([]loadbalancer.RoutingRule, 0, len(httpRoute.Spec.Rules))
			for i := range httpRoute.Spec.Rules {
				rule := makeRandomOCIRoutingRule()
				expectedRules = append(expectedRules, rule)
				ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
					httpRoute:          httpRoute,
					httpRouteRuleIndex: i,
				}).Return(rule, nil).Once()
			}
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				listenerName:   string(listener.Name),
				policyRules:    expectedRules,
			}).Return(nil).Once()

			knownBackends := map[string]corev1.Service{
				backendRefName(backendRef, httpRoute.Namespace).String(): service,
			}

			_, err := model.programRoute(t.Context(), programRouteParams{
				gateway:       *gateway,
				config:        config,
				httpRoute:     httpRoute,
				knownBackends: knownBackends,
				matchedListeners: []gatewayv1.Listener{
					listener,
				},
			})

			require.NoError(t, err)
		})

		t.Run("fails when resolved backend service is missing", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()
			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)

			_, err := model.programRoute(t.Context(), programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    map[string]corev1.Service{},
				matchedListeners: []gatewayv1.Listener{makeRandomListener()},
			})

			require.ErrorContains(t, err, "resolved backend service")
		})

		t.Run("program with previously programmed annotations passes stale rules for cleanup", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			// Create HTTP route with multiple rules
			backendRefs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNamespaceOpt(fmt.Sprintf("ns_%d", rand.IntN(1000))),
				randomHTTPRouteWithNameOpt(fmt.Sprintf("rt_%d", rand.IntN(1000))),
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs[0])),
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs[1])),
				),
			)
			wantPreviousRules := []string{
				fmt.Sprintf("p0000_%s", httpRoute.Name),
				fmt.Sprintf("p0001_%s", httpRoute.Name),
			}
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: strings.Join(wantPreviousRules, ","),
			}

			knownServices := []corev1.Service{
				makeRandomService(randomServiceFromBackendRef(backendRefs[0], &httpRoute)),
				makeRandomService(randomServiceFromBackendRef(backendRefs[1], &httpRoute)),
			}
			knownServicesByName := lo.SliceToMap(knownServices, func(s corev1.Service) (string, corev1.Service) {
				return types.NamespacedName{
					Namespace: s.Namespace,
					Name:      s.Name,
				}.String(), s
			})

			listeners := makeFewRandomListeners()

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    knownServicesByName,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Expect reconciliation of backend sets for each backendRef.
			for _, ref := range backendRefs {
				service := knownServicesByName[backendObjectRefName(ref.BackendObjectReference, httpRoute.Namespace).String()]
				ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					service:        service,
					routeNS:        httpRoute.Namespace,
					backendRef:     ref.BackendRef,
				}).Return(nil)
			}

			// Create expected routing rules for each HTTP route rule
			expectedRules := make([]loadbalancer.RoutingRule, 0, len(httpRoute.Spec.Rules))
			for i := range httpRoute.Spec.Rules {
				rule := makeRandomOCIRoutingRule()
				rule.Name = new(ociListerPolicyRuleName(httpRoute, i))
				expectedRules = append(expectedRules, rule)

				ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
					httpRoute:          httpRoute,
					httpRouteRuleIndex: i,
				}).Return(rule, nil).Once()
			}

			// Expect commitRoutingPolicyV2 to be called for each listener
			for _, listener := range listeners {
				ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID:  config.Spec.LoadBalancerID,
					listenerName:    string(listener.Name),
					policyRules:     expectedRules,
					prevPolicyRules: wantPreviousRules,
				}).Return(nil)
			}

			_, err := model.programRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("removes previously programmed rules from no longer matched listeners", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()
			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)

			currentListener := makeRandomListener()
			staleListenerName := "stale-" + fake.Lorem().Word()
			staleRules := []string{
				"stale-rule-a-" + fake.Lorem().Word(),
				"stale-rule-b-" + fake.Lorem().Word(),
			}
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: strings.Join([]string{
					fmt.Sprintf("%s/%s", staleListenerName, staleRules[0]),
					fmt.Sprintf("%s/%s", staleListenerName, staleRules[1]),
				}, ","),
			}

			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			knownServicesByName := map[string]corev1.Service{
				types.NamespacedName{
					Namespace: service.Namespace,
					Name:      service.Name,
				}.String(): service,
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    knownServicesByName,
				matchedListeners: []gatewayv1.Listener{currentListener},
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				service:        service,
				routeNS:        httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(nil)

			rule := makeRandomOCIRoutingRule()
			rule.Name = new(ociListerPolicyRuleName(httpRoute, 0))
			ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: 0,
			}).Return(rule, nil)

			currentCommit := ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				listenerName:   string(currentListener.Name),
				policyRules:    []loadbalancer.RoutingRule{rule},
			}).Return(nil).Once()

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    staleListenerName,
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: staleRules,
			}).Return(nil).Once().NotBefore(currentCommit)

			result, err := model.programRoute(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, []string{
				fmt.Sprintf("%s/%s", currentListener.Name, lo.FromPtr(rule.Name)),
			}, result.programmedPolicyRules)
		})

		t.Run("fails when reconcile backend set fails", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)
			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			services := map[string]corev1.Service{
				types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String(): service,
			}
			listeners := []gatewayv1.Listener{
				makeRandomListener(),
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    services,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			wantErr := errors.New(fake.Lorem().Sentence(10))

			// First service reconciliation fails
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(wantErr)

			_, err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when makeRoutingRule fails", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)
			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			services := map[string]corev1.Service{
				types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String(): service,
			}
			listeners := []gatewayv1.Listener{
				makeRandomListener(),
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    services,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Backend set reconciliation succeeds
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(nil)

			wantErr := errors.New(fake.Lorem().Sentence(10))

			// Making routing rule fails
			ociLBModel.EXPECT().makeRoutingRule(t.Context(), mock.Anything).Return(loadbalancer.RoutingRule{}, wantErr)

			_, err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when commitRoutingPolicyV2 fails", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef)),
				),
			)
			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			services := map[string]corev1.Service{
				types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String(): service,
			}
			listeners := []gatewayv1.Listener{
				makeRandomListener(),
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    services,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Backend set reconciliation succeeds
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(nil)

			// Making routing rule succeeds
			rule := makeRandomOCIRoutingRule()
			ociLBModel.EXPECT().makeRoutingRule(t.Context(), mock.Anything).Return(rule, nil)

			wantErr := errors.New(fake.Lorem().Sentence(10))

			// Committing policy fails
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), mock.Anything).Return(wantErr)

			_, err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("removeStaleL7PolicyRules", func(t *testing.T) {
		t.Run("removes rules for listeners that are no longer matched", func(t *testing.T) {
			fake := faker.New()
			ociLBModel := NewMockociLoadBalancerModel(t)
			loadBalancerID := "lb-" + fake.UUID().V4()
			currentListenerName := "current-" + fake.Lorem().Word()
			staleListenerName := "stale-" + fake.Lorem().Word()
			staleRules := []string{
				"rule-a-" + fake.Lorem().Word(),
				"rule-b-" + fake.Lorem().Word(),
			}

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    staleListenerName,
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: staleRules,
			}).Return(nil).Once()

			err := removeStaleL7PolicyRules(
				t.Context(),
				ociLBModel,
				loadBalancerID,
				map[string][]string{
					currentListenerName: {"current-rule-" + fake.Lorem().Word()},
					staleListenerName:   staleRules,
				},
				map[string]struct{}{currentListenerName: {}},
			)

			require.NoError(t, err)
		})

		t.Run("returns stale rule cleanup errors", func(t *testing.T) {
			fake := faker.New()
			ociLBModel := NewMockociLoadBalancerModel(t)
			loadBalancerID := "lb-" + fake.UUID().V4()
			staleListenerName := "stale-" + fake.Lorem().Word()
			wantErr := errors.New("cleanup-" + fake.Lorem().Sentence(4))

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), mock.Anything).Return(wantErr).Once()

			err := removeStaleL7PolicyRules(
				t.Context(),
				ociLBModel,
				loadBalancerID,
				map[string][]string{staleListenerName: {"rule-" + fake.Lorem().Word()}},
				map[string]struct{}{},
			)

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("deprovisionStaleL7BackendSets", func(t *testing.T) {
		t.Run("deprovisions backend sets that are no longer desired", func(t *testing.T) {
			fake := faker.New()
			ociLBModel := NewMockociLoadBalancerModel(t)
			loadBalancerID := "lb-" + fake.UUID().V4()
			currentBackendSetName := "current-" + fake.Lorem().Word()
			staleBackendSetName := "stale-" + fake.Lorem().Word()

			ociLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, staleBackendSetName).
				Return(nil).
				Once()

			err := deprovisionStaleL7BackendSets(
				t.Context(),
				ociLBModel,
				loadBalancerID,
				map[string]struct{}{currentBackendSetName: {}},
				map[string]struct{}{currentBackendSetName: {}, staleBackendSetName: {}},
			)

			require.NoError(t, err)
		})

		t.Run("returns stale backend set cleanup errors", func(t *testing.T) {
			fake := faker.New()
			ociLBModel := NewMockociLoadBalancerModel(t)
			loadBalancerID := "lb-" + fake.UUID().V4()
			staleBackendSetName := "stale-" + fake.Lorem().Word()
			wantErr := errors.New("backend-set-" + fake.Lorem().Sentence(4))

			ociLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, staleBackendSetName).
				Return(wantErr).
				Once()

			err := deprovisionStaleL7BackendSets(
				t.Context(),
				ociLBModel,
				loadBalancerID,
				map[string]struct{}{},
				map[string]struct{}{staleBackendSetName: {}},
			)

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("isProgrammingRequired", func(t *testing.T) {
		// Helper to create base details for isProgrammingRequired tests
		newIsProgrammingRequiredDetails := func() (gatewayv1.GatewayController, resolvedRouteDetails) {
			fake := faker.New()
			controllerName := gatewayv1.GatewayController(fake.Internet().Domain())
			route := makeRandomHTTPRoute()
			route.Generation = rand.Int64N(10000) + 1 // Start with a random generation
			details := resolvedRouteDetails{
				httpRoute: route,
				gatewayDetails: resolvedGatewayDetails{
					gatewayClass: *newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(controllerName)),
					gateway:      *newRandomGateway(),
					config:       makeRandomGatewayConfig(),
				},
				matchedRef: gatewayv1.ParentReference{
					Namespace: new(gatewayv1.Namespace(route.Namespace)),
					Name:      gatewayv1.ObjectName(fake.Lorem().Word()),
				},
			}
			return controllerName, details
		}

		t.Run("ProgrammingRequired/NoMatchingParentStatus", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			_, details := newIsProgrammingRequiredDetails()

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{ControllerName: gatewayv1.GatewayController(fake.Lorem().Word())}, // Different controller
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required, "Programming should be required when no matching parent status exists")
		})

		t.Run("ProgrammingRequired/ParentStatusFound", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      details.matchedRef,
					Conditions: []metav1.Condition{
						{
							Type:   string(gatewayv1.RouteConditionResolvedRefs),
							Status: metav1.ConditionTrue,
						},
					},
				},
			}

			checkResult := lo.Ternary(rand.IntN(2) == 0, true, false)
			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().isConditionSet(isConditionSetParams{
				resource:      &details.httpRoute,
				conditions:    details.httpRoute.Status.Parents[0].Conditions,
				conditionType: string(gatewayv1.RouteConditionResolvedRefs),
				annotations: map[string]string{
					HTTPRouteProgrammingRevisionAnnotation:    HTTPRouteProgrammingRevisionValue,
					L7RouteProgrammedLoadBalancerIDAnnotation: details.gatewayDetails.config.Spec.LoadBalancerID,
				},
			}).Return(checkResult)

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.Equal(t, !checkResult, required)
		})

		t.Run("ProgrammingRequired/ProgrammingRevisionChanged", func(t *testing.T) {
			deps := newMockDeps(t)
			deps.ResourcesModel = newResourcesModel(resourcesModelDeps{
				K8sClient:  deps.K8sClient,
				RootLogger: diag.RootTestLogger(),
			})
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()

			if details.httpRoute.Annotations == nil {
				details.httpRoute.Annotations = map[string]string{}
			}
			details.httpRoute.Annotations[HTTPRouteProgrammingRevisionAnnotation] = "2"
			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      details.matchedRef,
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayv1.RouteConditionResolvedRefs),
							Status:             metav1.ConditionTrue,
							ObservedGeneration: details.httpRoute.Generation,
						},
					},
				},
			}

			required, err := model.isProgrammingRequired(details)

			require.NoError(t, err)
			assert.True(t, required)
		})

		t.Run("ProgrammingRequired/ParentRefMismatch", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()

			mismatchedParentRef := details.matchedRef
			mismatchedParentRef.Name = gatewayv1.ObjectName(fake.Lorem().Word()) // Different name

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      mismatchedParentRef, // Mismatched ref
				},
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required)
		})
	})

	t.Run("setProgrammed", func(t *testing.T) {
		t.Run("success", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			route.Generation = rand.Int64N(1000) + 1
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()
			parentStatusIndex := rand.IntN(5)

			route.Status.Parents = make([]gatewayv1.RouteParentStatus, parentStatusIndex+2)
			for i := range route.Status.Parents {
				route.Status.Parents[i] = gatewayv1.RouteParentStatus{
					ParentRef:      makeRandomParentRef(),
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
					Conditions:     []metav1.Condition{{Type: "SomeOther", Status: metav1.ConditionTrue}},
				}
			}
			// Set the correct parent ref for the target index
			route.Status.Parents[parentStatusIndex].ParentRef = matchedRef

			params := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				config:       gatewayData.config,
				matchedRef:   matchedRef,
				programmedPolicyRules: []string{
					"rule1-" + fake.Lorem().Word(),
					"rule2-" + fake.Lorem().Word(),
					"rule3-" + fake.Lorem().Word(),
				},
				programmedBackendSets: []string{
					"backend-set-1-" + fake.Lorem().Word(),
					"backend-set-2-" + fake.Lorem().Word(),
				},
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().setCondition(t.Context(), setConditionParams{
				resource:      &route,
				conditions:    &route.Status.Parents[parentStatusIndex].Conditions,
				conditionType: string(gatewayv1.RouteConditionResolvedRefs),
				status:        metav1.ConditionTrue,
				reason:        string(gatewayv1.RouteReasonResolvedRefs),
				message:       fmt.Sprintf("Route programmed by %s", params.gateway.Name),
				annotations: map[string]string{
					HTTPRouteProgrammingRevisionAnnotation:    HTTPRouteProgrammingRevisionValue,
					HTTPRouteProgrammedPolicyRulesAnnotation:  strings.Join(params.programmedPolicyRules, ","),
					HTTPRouteProgrammedBackendSetsAnnotation:  strings.Join(params.programmedBackendSets, ","),
					L7RouteProgrammedLoadBalancerIDAnnotation: gatewayData.config.Spec.LoadBalancerID,
				},
				finalizer: HTTPRouteProgrammedFinalizer,
			}).Return(nil)

			// The model receives details by value, so it works on a copy of httpRoute.
			err := model.setProgrammed(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("parent status not found (wrong controller)", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()

			// Add a status, but for a different controller
			route.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ParentRef:      matchedRef,
					ControllerName: gatewayv1.GatewayController(fake.Internet().Domain()),
				},
			}

			details := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef,
			}

			err := model.setProgrammed(t.Context(), details)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "parent status not found for controller")
		})

		t.Run("parent status not found (wrong parentRef)", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()
			wrongParentRef := makeRandomParentRef()

			// Add a status with the correct controller, but wrong parentRef
			route.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ParentRef:      wrongParentRef,
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
				},
			}

			details := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef, // The ref we are looking for
			}

			err := model.setProgrammed(t.Context(), details)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "parent status not found for controller")
		})

		t.Run("update fails", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()

			// Add a matching parent status entry
			route.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ParentRef:      matchedRef,
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
				},
			}

			details := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef,
			}

			updateErr := errors.New(fake.Lorem().Sentence(10))
			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().setCondition(t.Context(), mock.Anything).Return(updateErr)

			err := model.setProgrammed(t.Context(), details)
			require.ErrorIs(t, err, updateErr)
		})
	})

	t.Run("deprovisionRoute", func(t *testing.T) {
		t.Run("successfully deprovisions route with multiple listeners", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			wantBackendRefs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			backendResRules := lo.Map(
				wantBackendRefs,
				func(br gatewayv1.HTTPBackendRef, _ int) gatewayv1.HTTPRouteRule {
					return makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(br))
				},
			)

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(backendResRules...),
			)

			wantPreviousRules := []string{
				"rule1-" + fake.Lorem().Word(),
				"rule2-" + fake.Lorem().Word(),
			}
			annotationValue := strings.Join(wantPreviousRules, ",")
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: annotationValue,
			}
			httpRoute.Finalizers = []string{
				HTTPRouteProgrammedFinalizer,
				fake.Internet().Domain(),
			}

			listeners := makeFewRandomListeners()

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			var lastCommitCall *mock.Call
			for _, listener := range listeners {
				lastCommitCall = ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID:  config.Spec.LoadBalancerID,
					listenerName:    string(listener.Name),
					policyRules:     []loadbalancer.RoutingRule{}, // Important: No rules to program
					prevPolicyRules: wantPreviousRules,
				}).Return(nil).Once()
			}

			for _, backendRef := range wantBackendRefs {
				ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					routeNamespace: httpRoute.Namespace,
					backendRef:     backendRef.BackendRef,
				}).Return(nil).Once().NotBefore(lastCommitCall)
			}

			// Expect client update for finalizer removal
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			var updatedRoute *gatewayv1.HTTPRoute
			mockK8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				var ok bool
				updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)

				assert.NotContains(t, updatedRoute.Finalizers, HTTPRouteProgrammedFinalizer)

				return ok && assert.Equal(t, httpRoute.Name, updatedRoute.Name)
			})).Return(nil)

			err := model.deprovisionRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("successfully deprovisions route with no previous rules annotation", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute()
			listeners := makeFewRandomListeners()

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			// Expect commitRoutingPolicy NOT to be called
			ociLBModel.AssertNotCalled(t, "commitRoutingPolicy", mock.Anything, mock.Anything)

			err := model.deprovisionRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("deprovisions listener scoped previous rules", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute()
			httpRoute.Finalizers = []string{HTTPRouteProgrammedFinalizer}

			currentListener := makeRandomListener()
			staleListenerName := "stale-" + fake.Lorem().Word()
			currentRule := "current-rule-" + fake.Lorem().Word()
			staleRule := "stale-rule-" + fake.Lorem().Word()
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: strings.Join([]string{
					fmt.Sprintf("%s/%s", currentListener.Name, currentRule),
					fmt.Sprintf("%s/%s", staleListenerName, staleRule),
				}, ","),
			}

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: []gatewayv1.Listener{currentListener},
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			currentCommit := ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    string(currentListener.Name),
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{currentRule},
			}).Return(nil).Once()
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    staleListenerName,
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{staleRule},
			}).Return(nil).Once().NotBefore(currentCommit)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updatedRoute, ok := obj.(*gatewayv1.HTTPRoute)
				return ok && assert.NotContains(t, updatedRoute.Finalizers, HTTPRouteProgrammedFinalizer)
			})).Return(nil)

			err := model.deprovisionRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fails when commitRoutingPolicy fails", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute()
			// Ensure prevPolicyRules annotation is present so the method doesn't return early
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: "rule1,rule2",
			}
			listeners := []gatewayv1.Listener{makeRandomListener()} // Just one for simplicity

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), mock.Anything).Return(wantErr)

			err := model.deprovisionRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})
}

func TestResolveL7BackendSSLConfig(t *testing.T) {
	service := corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "backend"}}
	backendRef := gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
		Name: gatewayv1.ObjectName("backend"),
	}}

	t.Run("does not manage SSL config without BackendTLSPolicy model", func(t *testing.T) {
		sslConfig, managed, err := resolveL7BackendSSLConfig(
			t.Context(),
			programL7RoutePolicyParams{},
			service,
			backendRef,
		)

		require.NoError(t, err)
		require.Nil(t, sslConfig)
		require.False(t, managed)
	})

	t.Run("does not resolve SSL config when BackendTLSPolicy support is disabled", func(t *testing.T) {
		sslConfig, managed, err := resolveL7BackendSSLConfig(
			t.Context(),
			programL7RoutePolicyParams{
				backendTLSDisabled: true,
				backendTLSPolicy: &stubBackendTLSPolicyModel{
					resolveFunc: func(resolveBackendTLSPolicyParams) (*loadbalancer.SslConfigurationDetails, error) {
						require.Fail(t, "disabled BackendTLSPolicy support should not resolve policies")
						return nil, errors.New("unexpected policy resolution")
					},
				},
			},
			service,
			backendRef,
		)

		require.NoError(t, err)
		require.Nil(t, sslConfig)
		require.False(t, managed)
	})

	t.Run("manages nil SSL config when no policy matches", func(t *testing.T) {
		sslConfig, managed, err := resolveL7BackendSSLConfig(
			t.Context(),
			programL7RoutePolicyParams{
				backendTLSPolicy: &stubBackendTLSPolicyModel{resolveErr: errBackendTLSPolicyNotFound},
			},
			service,
			backendRef,
		)

		require.NoError(t, err)
		require.Nil(t, sslConfig)
		require.True(t, managed)
	})

	t.Run("manages nil SSL config when matching policy is invalid", func(t *testing.T) {
		fake := faker.New()
		invalidPolicy := gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "policy-" + fake.Lorem().Word(),
				Name:      "policy-" + fake.Lorem().Word(),
			},
		}
		sslConfig, managed, err := resolveL7BackendSSLConfig(
			t.Context(),
			programL7RoutePolicyParams{
				backendTLSPolicy: &stubBackendTLSPolicyModel{
					resolveErr: backendTLSPolicyStatusError{
						policy:  invalidPolicy,
						reason:  gatewayv1.BackendTLSPolicyReasonInvalidCACertificateRef,
						message: "CA certificate reference was not found",
					},
				},
			},
			service,
			backendRef,
		)

		require.NoError(t, err)
		require.Nil(t, sslConfig)
		require.True(t, managed)
	})

	t.Run("returns resolved SSL config", func(t *testing.T) {
		verifyDepth := 2
		wantConfig := &loadbalancer.SslConfigurationDetails{VerifyDepth: &verifyDepth}
		backendTLS := &stubBackendTLSPolicyModel{
			resolveFunc: func(params resolveBackendTLSPolicyParams) (*loadbalancer.SslConfigurationDetails, error) {
				require.Equal(t, service.Name, params.service.Name)
				return wantConfig, nil
			},
		}
		sslConfig, managed, err := resolveL7BackendSSLConfig(
			t.Context(),
			programL7RoutePolicyParams{
				backendTLSPolicy: backendTLS,
			},
			service,
			backendRef,
		)

		require.NoError(t, err)
		require.True(t, managed)
		require.Same(t, wantConfig, sslConfig)
	})

	t.Run("returns resolution errors", func(t *testing.T) {
		wantErr := errors.New("policy invalid")
		_, managed, err := resolveL7BackendSSLConfig(
			t.Context(),
			programL7RoutePolicyParams{
				backendTLSPolicy: &stubBackendTLSPolicyModel{resolveErr: wantErr},
			},
			service,
			backendRef,
		)

		require.ErrorIs(t, err, wantErr)
		require.True(t, managed)
	})
}
