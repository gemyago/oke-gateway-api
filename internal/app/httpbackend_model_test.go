package app

import (
	"context"
	"fmt"
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
		}
	}

	t.Run("syncBackendEndpoints", func(t *testing.T) {
		t.Run("update backend sets", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			refs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			refs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			rule1 := makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(refs1...))
			rule2 := makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(refs2...))

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(rule1, rule2),
			)

			config := makeRandomGatewayConfig()

			allRefs := make([]gatewayv1.HTTPBackendRef, 0, len(refs1)+len(refs2))
			allRefs = append(allRefs, refs1...)
			allRefs = append(allRefs, refs2...)

			endpointSlicesByRef := lo.SliceToMap(allRefs,
				func(ref gatewayv1.HTTPBackendRef) (string, discoveryv1.EndpointSlice) {
					return string(ref.BackendObjectReference.Name), makeRandomEndpointSlice(
						randomEndpointSliceWithEndpointsOpt(),
					)
				})

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			for _, ref := range allRefs {
				mockK8sClient.EXPECT().List(
					t.Context(),
					mock.Anything,
					client.MatchingFields{
						discoveryv1.LabelServiceName: string(ref.BackendObjectReference.Name),
					},
				).RunAndReturn(func(_ context.Context, ol client.ObjectList, _ ...client.ListOption) error {
					epSliceList, ok := ol.(*discoveryv1.EndpointSliceList)
					require.True(t, ok, "expected an EndpointSliceList")
					epSliceList.Items = append(epSliceList.Items, endpointSlicesByRef[string(ref.BackendObjectReference.Name)])
					return nil
				}).Once()
			}

			for index, rule := range httpRoute.Spec.Rules {
				var wantBackends []loadbalancer.BackendDetails
				for _, backendRef := range rule.BackendRefs {
					refSlice := endpointSlicesByRef[string(backendRef.BackendObjectReference.Name)]
					for _, endpoint := range refSlice.Endpoints {
						port := int32(*backendRef.BackendObjectReference.Port)
						wantBackends = append(wantBackends, loadbalancer.BackendDetails{
							Port:      lo.ToPtr(int(port)),
							IpAddress: &endpoint.Addresses[0],
						})
					}
				}

				backendSetName := backendSetName(httpRoute, rule, index)

				wantOperationID := faker.UUIDHyphenated()
				mockOciLoadBalancerClient, _ := deps.OciLoadBalancerClient.(*MockociLoadBalancerClient)
				mockOciLoadBalancerClient.EXPECT().UpdateBackendSet(
					t.Context(),
					mock.MatchedBy(func(req loadbalancer.UpdateBackendSetRequest) bool {
						fmt.Println("req", *req.LoadBalancerId, *req.BackendSetName, backendSetName)

						return lo.NoneBy(
							[]bool{
								assert.Equal(t, *req.LoadBalancerId, config.Spec.LoadBalancerID),
								// assert.Equal(t, *req.BackendSetName, backendSetName),
							},
							func(b bool) bool { return !b },
						)
					}),
				).Return(loadbalancer.UpdateBackendSetResponse{
					OpcWorkRequestId: &wantOperationID,
				}, nil).Once()

				mockWorkRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
				mockWorkRequestsWatcher.EXPECT().WaitFor(t.Context(), wantOperationID).Return(nil).Once()
			}

			err := model.syncRouteBackendEndpoints(t.Context(), syncRouteBackendEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
			})

			require.NoError(t, err)
		})
	})
}
