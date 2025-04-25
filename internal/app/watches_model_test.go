package app

import (
	"fmt"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestWatchesModel(t *testing.T) {
	makeMockDeps := func(t *testing.T) WatchesModelDeps {
		return WatchesModelDeps{
			K8sClient: NewMockk8sClient(t),
			Logger:    diag.RootTestLogger(),
		}
	}

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
				randomHTTPRouteWithRandomRulesOpt(
					randomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs1...),
					),
					randomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs2...),
					),
				),
				randomHTTPRouteWithRandomRulesOpt(
					randomHTTPRouteRule(
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
					ref.BackendObjectReference.Namespace,
					ref.BackendObjectReference.Name,
				)
			})

			result := model.indexHTTPRouteByBackendService(&httpRoute)

			require.ElementsMatch(t, wantIndices, result)
		})
	})
}
