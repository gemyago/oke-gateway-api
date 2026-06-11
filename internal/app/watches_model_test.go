package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	configtypes "github.com/gemyago/oke-gateway-api/internal/types"
)

func withRelevantGatewayClass(gw *gatewayv1.Gateway) {
	if gw.Annotations == nil {
		gw.Annotations = make(map[string]string)
	}
	gw.Annotations[ControllerClassName] = "true"
}

func TestWatchesModel(t *testing.T) {
	makeMockDeps := func(t *testing.T) WatchesModelDeps {
		return WatchesModelDeps{
			K8sClient: NewMockk8sClient(t),
			Logger:    diag.RootTestLogger(),
		}
	}

	t.Run("RegisterFieldIndexers", func(t *testing.T) {
		t.Run("registers indexer for HTTPRoute backend service references", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1alpha2.TCPRoute{},
				tcpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1alpha2.UDPRoute{},
				udpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.NoError(t, err)
		})

		t.Run("skips L4 route indexers when disabled", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer, RegisterFieldIndexersOptions{})
			require.NoError(t, err)
		})

		t.Run("returns error if HTTPRoute indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if GRPCRoute indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorContains(t, err, "failed to index GRPCRoute by backend service")
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if Gateway certificate indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.GRPCRoute{},
				grpcRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1alpha2.TCPRoute{},
				tcpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1alpha2.UDPRoute{},
				udpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(nil)

			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.Gateway{},
				gatewayCertificateIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if L4 route indexer registration fails", func(t *testing.T) {
			for name, tc := range map[string]struct {
				failTCP bool
				err     string
			}{
				"tcp": {failTCP: true, err: "failed to index TCPRoute by backend service"},
				"udp": {err: "failed to index UDPRoute by backend service"},
			} {
				t.Run(name, func(t *testing.T) {
					deps := makeMockDeps(t)
					model := NewWatchesModel(deps)
					mockIndexer := k8sapi.NewMockFieldIndexer(t)
					wantErr := errors.New(faker.New().Lorem().Sentence(10))

					mockIndexer.EXPECT().IndexField(
						t.Context(),
						&gatewayv1.HTTPRoute{},
						httpRouteBackendServiceIndexKey,
						mock.AnythingOfType("client.IndexerFunc"),
					).Return(nil)
					mockIndexer.EXPECT().IndexField(
						t.Context(),
						&gatewayv1.GRPCRoute{},
						grpcRouteBackendServiceIndexKey,
						mock.AnythingOfType("client.IndexerFunc"),
					).Return(nil)
					if tc.failTCP {
						mockIndexer.EXPECT().IndexField(
							t.Context(),
							&gatewayv1alpha2.TCPRoute{},
							tcpRouteBackendServiceIndexKey,
							mock.AnythingOfType("client.IndexerFunc"),
						).Return(wantErr)
					} else {
						mockIndexer.EXPECT().IndexField(
							t.Context(),
							&gatewayv1alpha2.TCPRoute{},
							tcpRouteBackendServiceIndexKey,
							mock.AnythingOfType("client.IndexerFunc"),
						).Return(nil)
						mockIndexer.EXPECT().IndexField(
							t.Context(),
							&gatewayv1alpha2.UDPRoute{},
							udpRouteBackendServiceIndexKey,
							mock.AnythingOfType("client.IndexerFunc"),
						).Return(wantErr)
					}

					err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
					require.ErrorContains(t, err, tc.err)
					require.ErrorIs(t, err, wantErr)
				})
			}
		})
	})

	t.Run("indexHTTPRouteByBackendService", func(t *testing.T) {
		withRelevantRouteParentStatus := func(h *gatewayv1.HTTPRoute) {
			h.Status.Parents = append(h.Status.Parents,
				makeRandomRouteParentStatus(),
				makeRandomRouteParentStatus(
					randomRouteParentStatusWithConditionOpt(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionTrue,
					),
					randomRouteParentStatusWithControllerNameOpt(ControllerClassName),
				),
			)
		}

		t.Run("build index of all backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			refs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			refs3 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs1...),
					),
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs2...),
					),
				),
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs3...),
					),
				),
			)

			allRefs := make([]gatewayv1.HTTPBackendRef, 0, len(refs1)+len(refs2)+len(refs3))
			allRefs = append(allRefs, refs1...)
			allRefs = append(allRefs, refs2...)
			allRefs = append(allRefs, refs3...)
			wantIndices := lo.Map(allRefs, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return fmt.Sprintf("%v/%v",
					*ref.BackendObjectReference.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)

			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("uses namespace from route as fallback", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(
					randomBackendRefWithNillNamespaceOpt(),
				),
				makeRandomBackendRef(
					randomBackendRefWithNillNamespaceOpt(),
				),
			}

			route := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs1...),
					),
				),
			)

			wantIndices := lo.Map(refs1, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return fmt.Sprintf("%v/%v",
					route.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(t.Context(), &route)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("deduplicate backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
			)

			wantIndices := lo.Map(refs, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return fmt.Sprintf("%v/%v",
					*ref.BackendObjectReference.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("ignore non route objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.indexHTTPRouteByBackendService(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})

		t.Run("ignores deleted routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				withRelevantRouteParentStatus,
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
			)

			// Mark the route for deletion
			deletionTimestamp := metav1.Now()
			httpRoute.DeletionTimestamp = &deletionTimestamp

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.Nil(t, result)
		})

		t.Run("ignores routes without relevant parent status", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...)),
				),
			)

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.Nil(t, result)
		})

		t.Run("ignores routes with relevant but not accepted parent status", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				func(h *gatewayv1.HTTPRoute) {
					h.Status.Parents = append(h.Status.Parents,
						makeRandomRouteParentStatus(),
						makeRandomRouteParentStatus(
							randomRouteParentStatusWithConditionOpt(
								string(gatewayv1.RouteConditionResolvedRefs),
								metav1.ConditionFalse,
							),
							randomRouteParentStatusWithControllerNameOpt(ControllerClassName),
						),
					)
				},
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...)),
				),
			)

			result := model.indexHTTPRouteByBackendService(t.Context(), &httpRoute)
			require.Nil(t, result)
		})
	})

	t.Run("indexGRPCRouteByBackendService", func(t *testing.T) {
		withRelevantGRPCRouteParentStatus := func(route *gatewayv1.GRPCRoute) {
			route.Status.Parents = append(route.Status.Parents,
				makeRandomRouteParentStatus(),
				makeRandomRouteParentStatus(
					randomRouteParentStatusWithConditionOpt(
						string(gatewayv1.RouteConditionResolvedRefs),
						metav1.ConditionTrue,
					),
					randomRouteParentStatusWithControllerNameOpt(ControllerClassName),
				),
			)
		}

		t.Run("build index of all backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			refs := []gatewayv1.GRPCBackendRef{
				makeRandomGRPCBackendRef(),
				makeRandomGRPCBackendRef(randomGRPCBackendRefWithNilNamespaceOpt()),
			}

			grpcRoute := makeRandomGRPCRoute(
				withRelevantGRPCRouteParentStatus,
				randomGRPCRouteWithRulesOpt(
					makeRandomGRPCRouteRule(randomGRPCRouteRuleWithRandomBackendRefsOpt(refs...)),
				),
			)

			wantIndices := lo.Map(refs, func(ref gatewayv1.GRPCBackendRef, _ int) string {
				namespace := grpcRoute.Namespace
				if ref.BackendObjectReference.Namespace != nil {
					namespace = string(*ref.BackendObjectReference.Namespace)
				}
				return fmt.Sprintf("%v/%v", namespace, ref.BackendObjectReference.Name)
			})

			result := model.indexGRPCRouteByBackendService(t.Context(), &grpcRoute)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("ignores routes without relevant parent status", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			grpcRoute := makeRandomGRPCRoute(
				randomGRPCRouteWithRulesOpt(
					makeRandomGRPCRouteRule(randomGRPCRouteRuleWithRandomBackendRefsOpt(
						makeRandomGRPCBackendRef(),
					)),
				),
			)

			result := model.indexGRPCRouteByBackendService(t.Context(), &grpcRoute)
			require.Nil(t, result)
		})

		t.Run("ignores non route objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.indexGRPCRouteByBackendService(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})
	})

	t.Run("MapEndpointSliceToHTTPRoute", func(t *testing.T) {
		t.Run("finds matching HTTPRoutes based on service index", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			wantRoutes := []gatewayv1.HTTPRoute{
				makeRandomHTTPRoute(),
				makeRandomHTTPRoute(),
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(wantRoutes))
				return nil
			})

			wantRequests := lo.Map(wantRoutes, func(route gatewayv1.HTTPRoute, _ int) reconcile.Request {
				return reconcile.Request{
					NamespacedName: apitypes.NamespacedName{
						Name:      route.Name,
						Namespace: route.Namespace,
					},
				}
			})

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("ignores HTTPRoutes marked for deletion", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			// One route not marked for deletion, one route marked for deletion
			routeToDelete := makeRandomHTTPRoute()
			deletionTimestamp := metav1.Now()
			routeToDelete.DeletionTimestamp = &deletionTimestamp

			validRoute := makeRandomHTTPRoute()

			allRoutes := []gatewayv1.HTTPRoute{
				validRoute,
				routeToDelete,
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(allRoutes))
				return nil
			})

			// Only the validRoute should be reconciled
			wantRequests := []reconcile.Request{
				{
					NamespacedName: apitypes.NamespacedName{
						Name:      validRoute.Name,
						Namespace: validRoute.Namespace,
					},
				},
			}

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.Nil(t, result)
		})

		t.Run("returns nil when no routes found", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.HTTPRouteList{},
				client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				// Ensure Items field is explicitly set to an empty slice
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.HTTPRoute{}))
				return nil
			})

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &endpointSlice)
			require.Nil(t, result)
		})

		t.Run("returns nil if object is not an EndpointSlice", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})

		t.Run("ignore EndpointSlices without service name label", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapEndpointSliceToHTTPRoute(t.Context(), &discoveryv1.EndpointSlice{})
			require.Nil(t, result)
		})
	})

	t.Run("MapEndpointSliceToGRPCRoute", func(t *testing.T) {
		t.Run("finds matching GRPCRoutes and ignores deleted routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			validRoute := makeRandomGRPCRoute()
			routeToDelete := makeRandomGRPCRoute()
			deletionTimestamp := metav1.Now()
			routeToDelete.DeletionTimestamp = &deletionTimestamp

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteBackendServiceIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.GRPCRoute{
					validRoute,
					routeToDelete,
				}))
				return nil
			})

			result := model.MapEndpointSliceToGRPCRoute(t.Context(), &endpointSlice)
			require.ElementsMatch(t, []reconcile.Request{{
				NamespacedName: apitypes.NamespacedName{
					Name:      validRoute.Name,
					Namespace: validRoute.Namespace,
				},
			}}, result)
		})

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.New().Internet().Domain()
			ns := faker.New().Internet().User()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GRPCRouteList{},
				client.MatchingFields{grpcRouteBackendServiceIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapEndpointSliceToGRPCRoute(t.Context(), &endpointSlice)
			require.Nil(t, result)
		})
	})

	t.Run("MapHTTPRouteToGRPCRoute", func(t *testing.T) {
		t.Run("queues existing GRPCRoutes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			deletedRoute := makeRandomGRPCRoute()
			deletionTimestamp := metav1.Now()
			deletedRoute.DeletionTimestamp = &deletionTimestamp
			wantRoutes := []gatewayv1.GRPCRoute{makeRandomGRPCRoute(), makeRandomGRPCRoute()}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.GRPCRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(
						reflect.ValueOf(append(wantRoutes, deletedRoute)),
					)
					return nil
				})

			result := model.MapHTTPRouteToGRPCRoute(t.Context(), &gatewayv1.HTTPRoute{})

			require.ElementsMatch(t, lo.Map(wantRoutes, func(route gatewayv1.GRPCRoute, _ int) reconcile.Request {
				return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}
			}), result)
		})

		t.Run("returns nil for non HTTPRoute objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapHTTPRouteToGRPCRoute(t.Context(), &corev1.Service{})

			require.Nil(t, result)
		})

		t.Run("returns nil when GRPCRoute list fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.GRPCRouteList{}).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			result := model.MapHTTPRouteToGRPCRoute(t.Context(), &gatewayv1.HTTPRoute{})

			require.Nil(t, result)
		})
	})

	t.Run("MapGRPCRouteToHTTPRoute", func(t *testing.T) {
		t.Run("queues existing HTTPRoutes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			deletedRoute := makeRandomHTTPRoute()
			deletionTimestamp := metav1.Now()
			deletedRoute.DeletionTimestamp = &deletionTimestamp
			wantRoutes := []gatewayv1.HTTPRoute{makeRandomHTTPRoute(), makeRandomHTTPRoute()}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.HTTPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(
						reflect.ValueOf(append(wantRoutes, deletedRoute)),
					)
					return nil
				})

			result := model.MapGRPCRouteToHTTPRoute(t.Context(), &gatewayv1.GRPCRoute{})

			require.ElementsMatch(t, lo.Map(wantRoutes, func(route gatewayv1.HTTPRoute, _ int) reconcile.Request {
				return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}
			}), result)
		})

		t.Run("returns nil for non GRPCRoute objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapGRPCRouteToHTTPRoute(t.Context(), &corev1.Service{})

			require.Nil(t, result)
		})

		t.Run("returns nil when HTTPRoute list fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1.HTTPRouteList{}).
				Return(errors.New(faker.New().Lorem().Sentence(10)))

			result := model.MapGRPCRouteToHTTPRoute(t.Context(), &gatewayv1.GRPCRoute{})

			require.Nil(t, result)
		})
	})

	t.Run("indexGatewayByCertificate", func(t *testing.T) {
		t.Run("indexes all referenced Secret namespaced names from HTTPS listeners", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			// Create HTTPS listeners with random secrets
			listener1 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			listener2 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway(
				withRelevantGatewayClass,
				randomGatewayWithListenersOpt(listener1, listener2),
			)

			// Collect all referenced secrets
			var wantIndices []string
			for _, l := range gateway.Spec.Listeners {
				if l.TLS != nil {
					for _, ref := range l.TLS.CertificateRefs {
						ns := gateway.Namespace
						if ref.Namespace != nil {
							ns = string(*ref.Namespace)
						}
						wantIndices = append(wantIndices, ns+"/"+string(ref.Name))
					}
				}
			}

			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("deduplicates secrets", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			listener1 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			listener2 := makeRandomListener(func(l *gatewayv1.Listener) {
				l.TLS = &gatewayv1.ListenerTLSConfig{
					CertificateRefs: listener1.TLS.CertificateRefs,
				}
			})
			gateway := newRandomGateway(
				withRelevantGatewayClass,
				randomGatewayWithListenersOpt(listener1, listener2),
			)

			wantIndices := lo.Map(
				listener1.TLS.CertificateRefs,
				func(ref gatewayv1.SecretObjectReference, _ int) string {
					ns := gateway.Namespace
					if ref.Namespace != nil {
						ns = string(*ref.Namespace)
					}
					return ns + "/" + string(ref.Name)
				},
			)

			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.ElementsMatch(t, wantIndices, result)
		})

		t.Run("ignores non-Gateway objects", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			result := model.indexGatewayByCertificateSecrets(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})

		t.Run("ignores Gateways marked for deletion", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := newRandomGateway(withRelevantGatewayClass)
			deletionTimestamp := metav1.Now()
			gateway.DeletionTimestamp = &deletionTimestamp
			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.Nil(t, result)
		})

		t.Run("returns empty slice if no HTTPS listeners or no certificate refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := newRandomGateway(withRelevantGatewayClass) // Only HTTP listeners by default
			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.Empty(t, result)
		})

		t.Run("ignores Gateways without correct controller class", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := newRandomGateway() // No controller class set
			result := model.indexGatewayByCertificateSecrets(t.Context(), gateway)
			require.Nil(t, result)
		})
	})

	t.Run("MapSecretToGateway", func(t *testing.T) {
		t.Run("finds matching Gateways based on certificate index", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			wantGateways := []gatewayv1.Gateway{
				*newRandomGateway(withRelevantGatewayClass),
				*newRandomGateway(withRelevantGatewayClass),
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(wantGateways))
				return nil
			})

			wantRequests := lo.Map(wantGateways, func(gateway gatewayv1.Gateway, _ int) reconcile.Request {
				return reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&gateway),
				}
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("ignores non-TLS secrets", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(func(s *corev1.Secret) {
				s.Type = corev1.SecretTypeOpaque
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("ignores TLS secrets without certificate data", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(func(s *corev1.Secret) {
				s.Type = corev1.SecretTypeTLS
				s.Data = map[string][]byte{
					corev1.TLSPrivateKeyKey: []byte("private-key"),
				}
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("ignores TLS secrets without private key data", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(func(s *corev1.Secret) {
				s.Type = corev1.SecretTypeTLS
				s.Data = map[string][]byte{
					corev1.TLSCertKey: []byte("certificate"),
				}
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("ignores Gateways marked for deletion", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			// One gateway not marked for deletion, one gateway marked for deletion
			gatewayToDelete := *newRandomGateway(withRelevantGatewayClass)
			deletionTimestamp := metav1.Now()
			gatewayToDelete.DeletionTimestamp = &deletionTimestamp

			validGateway := *newRandomGateway(withRelevantGatewayClass)

			allGateways := []gatewayv1.Gateway{
				validGateway,
				gatewayToDelete,
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(allGateways))
				return nil
			})

			// Only the validGateway should be reconciled
			wantRequests := []reconcile.Request{
				{
					NamespacedName: client.ObjectKeyFromObject(&validGateway),
				},
			}

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.ElementsMatch(t, wantRequests, result)
		})

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).Return(wantErr)

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("returns nil when no gateways found", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			indexKey := fmt.Sprintf("%v/%v", secret.Namespace, secret.Name)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				&gatewayv1.GatewayList{},
				client.MatchingFields{gatewayCertificateIndexKey: indexKey},
			).RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				// Ensure Items field is explicitly set to an empty slice
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.Gateway{}))
				return nil
			})

			result := model.MapSecretToGateway(t.Context(), &secret)
			require.Nil(t, result)
		})

		t.Run("returns nil if object is not a Secret", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			result := model.MapSecretToGateway(t.Context(), &corev1.Service{})
			require.Nil(t, result)
		})
	})

	t.Run("L4 route watches", func(t *testing.T) {
		backendPort := gatewayv1.PortNumber(1935)
		crossNamespace := gatewayv1.Namespace("media")

		t.Run("indexes TCPRoute and UDPRoute backend services", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			tcpRoute := &gatewayv1alpha2.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
				Spec: gatewayv1alpha2.TCPRouteSpec{
					Rules: []gatewayv1alpha2.TCPRouteRule{
						{
							BackendRefs: []gatewayv1.BackendRef{
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: "rtmp-primary",
										Port: &backendPort,
									},
								},
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Namespace: &crossNamespace,
										Name:      "rtmp-secondary",
										Port:      &backendPort,
									},
								},
							},
						},
					},
				},
			}
			udpRoute := &gatewayv1alpha2.UDPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
				Spec: gatewayv1alpha2.UDPRouteSpec{
					Rules: []gatewayv1alpha2.UDPRouteRule{
						{
							BackendRefs: []gatewayv1.BackendRef{
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Name: "coap-primary",
										Port: &backendPort,
									},
								},
								{
									BackendObjectReference: gatewayv1.BackendObjectReference{
										Namespace: &crossNamespace,
										Name:      "coap-secondary",
										Port:      &backendPort,
									},
								},
							},
						},
					},
				},
			}

			require.ElementsMatch(t,
				[]string{"iot/rtmp-primary", "media/rtmp-secondary"},
				model.indexTCPRouteByBackendService(t.Context(), tcpRoute),
			)
			require.ElementsMatch(t,
				[]string{"iot/coap-primary", "media/coap-secondary"},
				model.indexUDPRouteByBackendService(t.Context(), udpRoute),
			)
			require.Nil(t, model.indexTCPRouteByBackendService(t.Context(), &corev1.Service{}))
			require.Nil(t, model.indexUDPRouteByBackendService(t.Context(), &corev1.Service{}))

			deletionTimestamp := metav1.Now()
			tcpRoute.DeletionTimestamp = &deletionTimestamp
			udpRoute.DeletionTimestamp = &deletionTimestamp
			require.Nil(t, model.indexTCPRouteByBackendService(t.Context(), tcpRoute))
			require.Nil(t, model.indexUDPRouteByBackendService(t.Context(), udpRoute))
		})

		t.Run("maps EndpointSlices to TCPRoute and UDPRoute requests", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt("iot"),
				randomEndpointSliceWithServiceNameOpt("backend"),
			)
			tcpRoutes := []gatewayv1alpha2.TCPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "srt"}},
			}
			udpRoutes := []gatewayv1alpha2.UDPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"}},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1alpha2.TCPRouteList{},
					client.MatchingFields{tcpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1alpha2.UDPRouteList{},
					client.MatchingFields{udpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}},
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "srt"}},
			}, model.MapEndpointSliceToTCPRoute(t.Context(), &endpointSlice))
			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}},
			}, model.MapEndpointSliceToUDPRoute(t.Context(), &endpointSlice))
			require.Nil(t, model.MapEndpointSliceToTCPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapEndpointSliceToUDPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapEndpointSliceToTCPRoute(t.Context(), &discoveryv1.EndpointSlice{}))
			require.Nil(t, model.MapEndpointSliceToUDPRoute(t.Context(), &discoveryv1.EndpointSlice{}))
		})

		t.Run("handles L4 EndpointSlice mapping errors and deleted routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt("iot"),
				randomEndpointSliceWithServiceNameOpt("backend"),
			)
			now := metav1.Now()
			tcpRoutes := []gatewayv1alpha2.TCPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "active"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "deleting", DeletionTimestamp: &now}},
			}
			udpRoutes := []gatewayv1alpha2.UDPRoute{
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "active"}},
				{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "deleting", DeletionTimestamp: &now}},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1alpha2.TCPRouteList{},
					client.MatchingFields{tcpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1alpha2.UDPRouteList{},
					client.MatchingFields{udpRouteBackendServiceIndexKey: "iot/backend"}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})
			require.Equal(
				t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "active"}}},
				model.MapEndpointSliceToTCPRoute(t.Context(), &endpointSlice),
			)
			require.Equal(
				t,
				[]reconcile.Request{{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "active"}}},
				model.MapEndpointSliceToUDPRoute(t.Context(), &endpointSlice),
			)

			deps = makeMockDeps(t)
			model = NewWatchesModel(deps)
			mockK8sClient, _ = deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1alpha2.TCPRouteList{},
					client.MatchingFields{tcpRouteBackendServiceIndexKey: "iot/backend"}).
				Return(errors.New("tcp list failed"))
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1alpha2.UDPRouteList{},
					client.MatchingFields{udpRouteBackendServiceIndexKey: "iot/backend"}).
				Return(errors.New("udp list failed"))
			require.Nil(t, model.MapEndpointSliceToTCPRoute(t.Context(), &endpointSlice))
			require.Nil(t, model.MapEndpointSliceToUDPRoute(t.Context(), &endpointSlice))
		})

		t.Run("maps ReferenceGrants to cross-namespace L4 routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "allow"}}
			tcpRoutes := []gatewayv1alpha2.TCPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
					Spec: gatewayv1alpha2.TCPRouteSpec{Rules: []gatewayv1alpha2.TCPRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Namespace: &crossNamespace,
							Name:      "rtmp",
							Port:      &backendPort,
						}}},
					}}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "local"},
					Spec: gatewayv1alpha2.TCPRouteSpec{Rules: []gatewayv1alpha2.TCPRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "local",
							Port: &backendPort,
						}}},
					}}},
				},
			}
			udpRoutes := []gatewayv1alpha2.UDPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
					Spec: gatewayv1alpha2.UDPRouteSpec{Rules: []gatewayv1alpha2.UDPRouteRule{{
						BackendRefs: []gatewayv1.BackendRef{{BackendObjectReference: gatewayv1.BackendObjectReference{
							Namespace: &crossNamespace,
							Name:      "coap",
							Port:      &backendPort,
						}}},
					}}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.TCPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.UDPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}},
			}, model.MapReferenceGrantToTCPRoute(t.Context(), grant))
			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}},
			}, model.MapReferenceGrantToUDPRoute(t.Context(), grant))
			require.Nil(t, model.MapReferenceGrantToTCPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapReferenceGrantToUDPRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("handles ReferenceGrant L4 route list errors", func(t *testing.T) {
			grant := &gatewayv1beta1.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "allow"}}
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.TCPRouteList{}).
				Return(errors.New("tcp list failed"))
			require.Nil(t, model.MapReferenceGrantToTCPRoute(t.Context(), grant))

			deps = makeMockDeps(t)
			model = NewWatchesModel(deps)
			mockK8sClient, _ = deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.UDPRouteList{}).
				Return(errors.New("udp list failed"))
			require.Nil(t, model.MapReferenceGrantToUDPRoute(t.Context(), grant))
		})

		t.Run("maps Gateway changes to attached L4 routes", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}}
			tcpRoutes := []gatewayv1alpha2.TCPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
					Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
					}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other"},
					Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "other"}},
					}},
				},
			}
			udpRoutes := []gatewayv1alpha2.UDPRoute{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
					Spec: gatewayv1alpha2.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
					}},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.TCPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(tcpRoutes))
					return nil
				})
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.UDPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(udpRoutes))
					return nil
				})

			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}},
			}, model.MapGatewayToTCPRoute(t.Context(), gateway))
			require.ElementsMatch(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"}},
			}, model.MapGatewayToUDPRoute(t.Context(), gateway))
			require.Nil(t, model.MapGatewayToTCPRoute(t.Context(), &corev1.Service{}))
			require.Nil(t, model.MapGatewayToUDPRoute(t.Context(), &corev1.Service{}))
		})

		t.Run("handles Gateway L4 route list errors", func(t *testing.T) {
			gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}}
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.TCPRouteList{}).
				Return(errors.New("tcp list failed"))
			require.Nil(t, model.MapGatewayToTCPRoute(t.Context(), gateway))

			deps = makeMockDeps(t)
			model = NewWatchesModel(deps)
			mockK8sClient, _ = deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(t.Context(), &gatewayv1alpha2.UDPRouteList{}).
				Return(errors.New("udp list failed"))
			require.Nil(t, model.MapGatewayToUDPRoute(t.Context(), gateway))
		})

		t.Run("maps GatewayConfig changes to referencing Gateways", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			config := &configtypes.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge-config"},
			}
			now := metav1.Now()
			gateways := []gatewayv1.Gateway{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:   "iot",
						Name:        "edge",
						Annotations: map[string]string{ControllerClassName: "true"},
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "iot",
						Name:      "edge-l4",
						Annotations: map[string]string{
							NetworkLoadBalancerControllerClassName: "true",
						},
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:   "iot",
						Name:        "unsupported",
						Annotations: map[string]string{"example.com/controller": "true"},
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other"},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "other-config",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "missing-ref"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:         "iot",
						Name:              "deleting",
						DeletionTimestamp: &now,
					},
					Spec: gatewayv1.GatewaySpec{
						Infrastructure: &gatewayv1.GatewayInfrastructure{
							ParametersRef: &gatewayv1.LocalParametersReference{
								Name: "edge-config",
							},
						},
					},
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{}, client.InNamespace("iot")).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf(gateways))
					return nil
				})

			require.Equal(t, []reconcile.Request{
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "edge"}},
				{NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "edge-l4"}},
			}, model.MapGatewayConfigToGateway(t.Context(), config))
			require.Nil(t, model.MapGatewayConfigToGateway(t.Context(), &corev1.Service{}))
		})

		t.Run("handles GatewayConfig Gateway list errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)
			config := &configtypes.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge-config"},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().
				List(t.Context(), &gatewayv1.GatewayList{}, client.InNamespace("iot")).
				Return(errors.New("gateway list failed"))

			require.Nil(t, model.MapGatewayConfigToGateway(t.Context(), config))
		})
	})
}
