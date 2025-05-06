package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/go-faker/faker/v4"
	"github.com/samber/lo"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

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

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.NoError(t, err)
		})

		t.Run("returns error if indexer registration fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			mockIndexer := k8sapi.NewMockFieldIndexer(t)
			wantErr := errors.New(faker.Sentence())
			mockIndexer.EXPECT().IndexField(
				t.Context(),
				&gatewayv1.HTTPRoute{},
				httpRouteBackendServiceIndexKey,
				mock.AnythingOfType("client.IndexerFunc"),
			).Return(wantErr)

			err := model.RegisterFieldIndexers(t.Context(), mockIndexer)
			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("indexHTTPRouteByBackendService", func(t *testing.T) {
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
	})

	t.Run("MapEndpointSliceToHTTPRoute", func(t *testing.T) {
		t.Run("finds matching HTTPRoutes based on service index", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.DomainName()
			ns := faker.Username()
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

		t.Run("returns nil if k8s client returns error", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			svcName := faker.DomainName()
			ns := faker.Username()
			indexKey := fmt.Sprintf("%v/%v", ns, svcName)

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithNamespaceOpt(ns),
				randomEndpointSliceWithServiceNameOpt(svcName),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.Sentence())
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

			svcName := faker.DomainName()
			ns := faker.Username()
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
}
