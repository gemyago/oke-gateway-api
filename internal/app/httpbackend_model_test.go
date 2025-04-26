package app

import (
	"context"
	"errors"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestHTTPBackendModel(t *testing.T) {
	newMockDeps := func(t *testing.T) httpBackendModelDeps {
		return httpBackendModelDeps{
			K8sClient:             NewMockk8sClient(t),
			RootLogger:            diag.RootTestLogger(),
			OciLoadBalancerClient: NewMockociLoadBalancerClient(t),
			WorkRequestsWatcher:   NewMockworkRequestsWatcher(t),
			self:                  NewMockhttpBackendModel(t),
		}
	}

	t.Run("syncRouteBackendEndpoints", func(t *testing.T) {
		t.Run("sync all rules", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			rules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt()),
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt()),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(rules...),
			)

			config := makeRandomGatewayConfig()

			mockSelf, _ := deps.self.(*MockhttpBackendModel)

			// Expect syncRouteBackendRuleEndpoints to be called for each rule
			for i := range rules {
				mockSelf.EXPECT().syncRouteBackendRuleEndpoints(
					t.Context(),
					syncRouteBackendRuleEndpointsParams{
						httpRoute: httpRoute,
						config:    config,
						ruleIndex: i,
					},
				).Return(nil).Once()
			}

			err := model.syncRouteBackendEndpoints(t.Context(), syncRouteBackendEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
			})

			assert.NoError(t, err)
		})

		t.Run("propagate rule sync error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			rules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt()),
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt()),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(rules...),
			)

			config := makeRandomGatewayConfig()

			mockSelf, _ := deps.self.(*MockhttpBackendModel)

			expectedErr := errors.New(faker.Sentence())

			// First rule sync succeeds
			mockSelf.EXPECT().syncRouteBackendRuleEndpoints(
				t.Context(),
				syncRouteBackendRuleEndpointsParams{
					httpRoute: httpRoute,
					config:    config,
					ruleIndex: 0,
				},
			).Return(nil).Once()

			// Second rule sync fails
			mockSelf.EXPECT().syncRouteBackendRuleEndpoints(
				t.Context(),
				syncRouteBackendRuleEndpointsParams{
					httpRoute: httpRoute,
					config:    config,
					ruleIndex: 1,
				},
			).Return(expectedErr).Once()

			err := model.syncRouteBackendEndpoints(t.Context(), syncRouteBackendEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("syncRouteBackendRuleEndpoints", func(t *testing.T) {
		t.Run("update backend set", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			refs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			rule1 := makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			))
			rule2 := makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(
				refs2...,
			))

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(rule1, rule2),
			)

			config := makeRandomGatewayConfig()

			endpointSlicesByRef := lo.SliceToMap(refs2,
				func(ref gatewayv1.HTTPBackendRef) (string, discoveryv1.EndpointSlice) {
					return string(ref.BackendObjectReference.Name), makeRandomEndpointSlice(
						randomEndpointSliceWithEndpointsOpt(),
					)
				})

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			for _, ref := range refs2 {
				mockK8sClient.EXPECT().List(
					t.Context(),
					mock.Anything,
					client.MatchingLabels{
						discoveryv1.LabelServiceName: string(ref.BackendObjectReference.Name),
					},
				).RunAndReturn(func(_ context.Context, ol client.ObjectList, _ ...client.ListOption) error {
					epSliceList, ok := ol.(*discoveryv1.EndpointSliceList)
					require.True(t, ok, "expected an EndpointSliceList")
					epSliceList.Items = append(epSliceList.Items, endpointSlicesByRef[string(ref.BackendObjectReference.Name)])
					return nil
				}).Once()
			}

			var wantBackends []loadbalancer.BackendDetails
			for _, backendRef := range rule2.BackendRefs {
				refSlice := endpointSlicesByRef[string(backendRef.BackendObjectReference.Name)]
				for _, endpoint := range refSlice.Endpoints {
					port := int32(*backendRef.BackendObjectReference.Port)
					wantBackends = append(wantBackends, loadbalancer.BackendDetails{
						Port:      lo.ToPtr(int(port)),
						IpAddress: &endpoint.Addresses[0],
					})
				}
			}

			backendSetName := backendSetName(httpRoute, rule2, 1)

			wantOperationID := faker.UUIDHyphenated()
			mockOciLoadBalancerClient, _ := deps.OciLoadBalancerClient.(*MockociLoadBalancerClient)
			mockOciLoadBalancerClient.EXPECT().UpdateBackendSet(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateBackendSetRequest) bool {
					return lo.NoneBy(
						[]bool{
							assert.Equal(t, *req.LoadBalancerId, config.Spec.LoadBalancerID),
							assert.Equal(t, *req.BackendSetName, backendSetName),
							assert.Equal(t, wantBackends, req.Backends),
						},
						func(b bool) bool { return !b },
					)
				}),
			).Return(loadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: &wantOperationID,
			}, nil).Once()

			mockWorkRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			mockWorkRequestsWatcher.EXPECT().WaitFor(t.Context(), wantOperationID).Return(nil).Once()

			err := model.syncRouteBackendRuleEndpoints(t.Context(), syncRouteBackendRuleEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
				ruleIndex: 1,
			})

			require.NoError(t, err)
		})
	})
}
