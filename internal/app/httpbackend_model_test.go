package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	t.Run("syncRouteBackendRefsEndpoints", func(t *testing.T) {
		t.Run("sync all rules", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			rules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt()),
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt()),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(rules...),
			)

			config := makeRandomGatewayConfig()

			mockSelf, _ := deps.self.(*MockhttpBackendModel)

			// Expect syncRouteBackendRefEndpoints to be called for each rule
			for _, rule := range rules {
				for _, ref := range rule.BackendRefs {
					mockSelf.EXPECT().syncRouteBackendRefEndpoints(
						t.Context(),
						syncRouteBackendRefEndpointsParams{
							httpRoute:  httpRoute,
							config:     config,
							backendRef: ref,
						},
					).Return(nil).Once()
				}
			}

			err := model.syncRouteEndpoints(t.Context(), syncRouteEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
			})

			assert.NoError(t, err)
		})

		t.Run("propagate rule sync error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			rules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(
					makeRandomBackendRef(),
				)),
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(
					makeRandomBackendRef(),
				)),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(rules...),
			)

			config := makeRandomGatewayConfig()

			mockSelf, _ := deps.self.(*MockhttpBackendModel)

			expectedErr := errors.New(faker.Sentence())

			// First rule sync succeeds
			mockSelf.EXPECT().syncRouteBackendRefEndpoints(
				t.Context(),
				syncRouteBackendRefEndpointsParams{
					httpRoute:  httpRoute,
					config:     config,
					backendRef: rules[0].BackendRefs[0],
				},
			).Return(nil).Once()

			// Second rule sync fails
			mockSelf.EXPECT().syncRouteBackendRefEndpoints(
				t.Context(),
				syncRouteBackendRefEndpointsParams{
					httpRoute:  httpRoute,
					config:     config,
					backendRef: rules[1].BackendRefs[0],
				},
			).Return(expectedErr).Once()

			err := model.syncRouteEndpoints(t.Context(), syncRouteEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("syncRouteBackendRefEndpoints", func(t *testing.T) {
		t.Run("update backend set", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute()

			config := makeRandomGatewayConfig()

			endpointSlice := makeRandomEndpointSlice()

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockK8sClient.EXPECT().List(
				t.Context(),
				mock.Anything,
				client.MatchingLabels{
					discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name),
				},
			).RunAndReturn(func(_ context.Context, ol client.ObjectList, _ ...client.ListOption) error {
				epSliceList, ok := ol.(*discoveryv1.EndpointSliceList)
				require.True(t, ok, "expected an EndpointSliceList")
				epSliceList.Items = append(epSliceList.Items, endpointSlice)
				return nil
			}).Once()

			wantUpdatedBackends := makeFewRandomOCIBackendDetails()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)

			// Create a sample existing BackendSet using the fixture
			currentBackends := makeFewRandomOCIBackends()
			sampleBackendSet := makeRandomOCIBackendSet(
				randomOCIBackendSetWithNameOpt(backendSetName),
				randomOCIBackendSetWithBackendsOpt(currentBackends),
			)

			backendRefPort := int32(*backendRef.BackendObjectReference.Port)

			mockSelf, _ := deps.self.(*MockhttpBackendModel)
			mockSelf.EXPECT().identifyBackendsToUpdate(
				t.Context(),
				mock.MatchedBy(func(params identifyBackendsToUpdateParams) bool {
					return assert.Equal(t, backendRefPort, params.endpointPort) &&
						assert.ElementsMatch(t, currentBackends, params.currentBackends) &&
						assert.ElementsMatch(t, []discoveryv1.EndpointSlice{endpointSlice}, params.endpointSlices)
				}),
			).Return(identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: wantUpdatedBackends,
			}, nil).Once()

			mockOciLoadBalancerClient, _ := deps.OciLoadBalancerClient.(*MockociLoadBalancerClient)

			// Expect GetBackendSet call
			mockOciLoadBalancerClient.EXPECT().GetBackendSet(
				t.Context(),
				loadbalancer.GetBackendSetRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
					BackendSetName: &backendSetName,
				},
			).Return(loadbalancer.GetBackendSetResponse{BackendSet: sampleBackendSet}, nil).Once()

			wantOperationID := faker.UUIDHyphenated()
			mockOciLoadBalancerClient.EXPECT().UpdateBackendSet(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateBackendSetRequest) bool {
					return lo.NoneBy(
						[]bool{
							assert.Equal(t, *req.LoadBalancerId, config.Spec.LoadBalancerID),
							assert.Equal(t, *req.BackendSetName, backendSetName),
							assert.ElementsMatch(t, wantUpdatedBackends, req.Backends),
							assert.Equal(t, sampleBackendSet.Policy, req.Policy),
							assert.Equal(t, sampleBackendSet.HealthChecker.Protocol, req.HealthChecker.Protocol),
							assert.Equal(t, sampleBackendSet.HealthChecker.Port, req.HealthChecker.Port),
							assert.Equal(t, sampleBackendSet.HealthChecker.UrlPath, req.HealthChecker.UrlPath),
							assert.Equal(t, sampleBackendSet.HealthChecker.ReturnCode, req.HealthChecker.ReturnCode),
							assert.Equal(t, sampleBackendSet.SessionPersistenceConfiguration, req.SessionPersistenceConfiguration),
							assert.Equal(t,
								sampleBackendSet.LbCookieSessionPersistenceConfiguration,
								req.LbCookieSessionPersistenceConfiguration,
							),
							assert.Equal(t, sampleBackendSet.SslConfiguration.CertificateName, req.SslConfiguration.CertificateName),
							assert.Equal(t,
								sampleBackendSet.SslConfiguration.TrustedCertificateAuthorityIds,
								req.SslConfiguration.TrustedCertificateAuthorityIds,
							),
						},
						func(b bool) bool { return !b },
					)
				}),
			).Return(loadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: &wantOperationID,
			}, nil).Once()

			mockWorkRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			mockWorkRequestsWatcher.EXPECT().WaitFor(t.Context(), wantOperationID).Return(nil).Once()

			err := model.syncRouteBackendRefEndpoints(t.Context(), syncRouteBackendRefEndpointsParams{
				httpRoute:  httpRoute,
				config:     config,
				backendRef: backendRef,
			})

			require.NoError(t, err)
		})

		t.Run("no update required", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute()

			config := makeRandomGatewayConfig()

			endpointSlice := makeRandomEndpointSlice()

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				mock.Anything,
				client.MatchingLabels{
					discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name),
				},
			).RunAndReturn(func(_ context.Context, ol client.ObjectList, _ ...client.ListOption) error {
				epSliceList, ok := ol.(*discoveryv1.EndpointSliceList)
				require.True(t, ok, "expected an EndpointSliceList")
				epSliceList.Items = append(epSliceList.Items, endpointSlice)
				return nil
			}).Once()

			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)

			currentBackends := makeFewRandomOCIBackends()
			sampleBackendSet := makeRandomOCIBackendSet(
				randomOCIBackendSetWithNameOpt(backendSetName),
				randomOCIBackendSetWithBackendsOpt(currentBackends),
			)

			mockOciLoadBalancerClient, _ := deps.OciLoadBalancerClient.(*MockociLoadBalancerClient)
			mockOciLoadBalancerClient.EXPECT().GetBackendSet(
				t.Context(),
				loadbalancer.GetBackendSetRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
					BackendSetName: &backendSetName,
				},
			).Return(loadbalancer.GetBackendSetResponse{BackendSet: sampleBackendSet}, nil).Once()

			backendRefPort := int32(*backendRef.BackendObjectReference.Port)

			mockSelf, _ := deps.self.(*MockhttpBackendModel)
			mockSelf.EXPECT().identifyBackendsToUpdate(
				t.Context(),
				identifyBackendsToUpdateParams{
					endpointPort:    backendRefPort,
					currentBackends: currentBackends,
					endpointSlices:  []discoveryv1.EndpointSlice{endpointSlice},
				},
			).Return(identifyBackendsToUpdateResult{
				updateRequired:  false,
				updatedBackends: []loadbalancer.BackendDetails{},
			}, nil).Once()

			err := model.syncRouteBackendRefEndpoints(t.Context(), syncRouteBackendRefEndpointsParams{
				httpRoute:  httpRoute,
				config:     config,
				backendRef: backendRef,
			})

			require.NoError(t, err)
		})
	})

	t.Run("identifyBackendsToUpdate", func(t *testing.T) {
		t.Run("happy path - add new backends", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)
			refPort := int32(rand.IntN(65534) + 1)

			currentBackends := []loadbalancer.Backend{}

			// Create multiple ready, non-terminating endpoints using makeFewRandomEndpoints
			numEndpoints := 3 + rand.IntN(3) // 3 to 5 endpoints
			endpoints := makeFewRandomEndpoints(
				numEndpoints,
				randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)), // All ready, not terminating
			)

			// Distribute endpoints into multiple slices and lists
			slice1 := discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Name: faker.UUIDHyphenated()},
				Endpoints:  endpoints[:numEndpoints/2], // First half
			}
			slice2 := discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Name: faker.UUIDHyphenated()},
				Endpoints:  endpoints[numEndpoints/2:], // Second half
			}

			endpointSlices := []discoveryv1.EndpointSlice{slice1, slice2}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			// Calculate expected backends from ALL endpoints
			expectedUpdatedBackends := make([]loadbalancer.BackendDetails, 0, numEndpoints)
			for _, endpoint := range endpoints {
				expectedUpdatedBackends = append(expectedUpdatedBackends, loadbalancer.BackendDetails{
					IpAddress: &endpoint.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				})
			}

			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: expectedUpdatedBackends,
				drainingCount:   0, // All are non-draining
			}

			// Act
			result, err := model.identifyBackendsToUpdate(t.Context(), params)

			// Assert
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("backend removal", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			initialEndpoints := makeFewRandomEndpoints(3, randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			currentBackends := lo.Map(initialEndpoints, func(ep discoveryv1.Endpoint, i int) loadbalancer.Backend {
				return loadbalancer.Backend{
					Name:      lo.ToPtr(fmt.Sprintf("backend-%d", i)),
					IpAddress: &ep.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				}
			})

			remainingEndpoints := initialEndpoints[:2]
			endpointSlices := []discoveryv1.EndpointSlice{
				{
					Endpoints: remainingEndpoints,
				},
			}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedUpdatedBackends := lo.Map(
				remainingEndpoints,
				func(ep discoveryv1.Endpoint, _ int) loadbalancer.BackendDetails {
					return loadbalancer.BackendDetails{
						IpAddress: &ep.Addresses[0],
						Port:      lo.ToPtr(int(refPort)),
						Drain:     lo.ToPtr(false),
					}
				})
			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: expectedUpdatedBackends,
				drainingCount:   0, // Remaining are non-draining
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("drain status update - start draining", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			initialEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			currentBackends := []loadbalancer.Backend{
				{
					Name:      lo.ToPtr(faker.Word()),
					IpAddress: &initialEndpoint.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				},
			}

			drainingEndpoint := initialEndpoint
			drainingEndpoint.Conditions.Terminating = lo.ToPtr(true)
			endpointSlices := []discoveryv1.EndpointSlice{
				{Endpoints: []discoveryv1.Endpoint{drainingEndpoint}},
			}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedUpdatedBackends := []loadbalancer.BackendDetails{
				{
					IpAddress: &initialEndpoint.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(true),
				},
			}
			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: expectedUpdatedBackends,
				drainingCount:   1, // The single backend is now draining
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("drain status update - stop draining", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			initialEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(true)))
			currentBackends := []loadbalancer.Backend{
				{
					Name:      lo.ToPtr(faker.Word()),
					IpAddress: &initialEndpoint.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(true),
				},
			}

			notDrainingEndpoint := initialEndpoint
			notDrainingEndpoint.Conditions.Terminating = lo.ToPtr(false)
			endpointSlices := []discoveryv1.EndpointSlice{
				{Endpoints: []discoveryv1.Endpoint{notDrainingEndpoint}},
			}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedUpdatedBackends := []loadbalancer.BackendDetails{
				{
					IpAddress: &initialEndpoint.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				},
			}
			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: expectedUpdatedBackends,
				drainingCount:   0, // The single backend is no longer draining
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("no changes needed", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			ep1 := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			ep2 := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(true)))
			initialEndpoints := []discoveryv1.Endpoint{ep1, ep2}
			currentBackends := lo.Map(initialEndpoints, func(ep discoveryv1.Endpoint, i int) loadbalancer.Backend {
				return loadbalancer.Backend{
					Name:      lo.ToPtr(fmt.Sprintf("backend-%d", i)),
					IpAddress: &ep.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     ep.Conditions.Terminating,
				}
			})

			endpointSlices := []discoveryv1.EndpointSlice{
				{Endpoints: initialEndpoints},
			}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedResult := identifyBackendsToUpdateResult{
				updateRequired: false,
				updatedBackends: lo.Map(currentBackends, func(b loadbalancer.Backend, _ int) loadbalancer.BackendDetails {
					return loadbalancer.BackendDetails{
						IpAddress: b.IpAddress,
						Port:      b.Port,
						Drain:     b.Drain,
					}
				}),
				drainingCount: 1, // ep2 was draining
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("all backends removed (empty slices)", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			initialEndpoints := makeFewRandomEndpoints(2, randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			currentBackends := lo.Map(initialEndpoints, func(ep discoveryv1.Endpoint, i int) loadbalancer.Backend {
				return loadbalancer.Backend{
					Name:      lo.ToPtr(fmt.Sprintf("backend-%d", i)),
					IpAddress: &ep.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				}
			})

			endpointSlices := []discoveryv1.EndpointSlice{}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: []loadbalancer.BackendDetails{},
				drainingCount:   0,
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("all backends removed (non-ready slices)", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			initialEndpoints := makeFewRandomEndpoints(2, randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			currentBackends := lo.Map(initialEndpoints, func(ep discoveryv1.Endpoint, i int) loadbalancer.Backend {
				return loadbalancer.Backend{
					Name:      lo.ToPtr(fmt.Sprintf("backend-%d", i)),
					IpAddress: &ep.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				}
			})

			nonReadyEndpoints := makeFewRandomEndpoints(2, randomEndpointWithConditionsOpt(lo.ToPtr(false), nil))
			endpointSlices := []discoveryv1.EndpointSlice{
				{Endpoints: nonReadyEndpoints},
			}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true,
				updatedBackends: []loadbalancer.BackendDetails{},
				drainingCount:   0,
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("empty input (no change)", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			currentBackends := []loadbalancer.Backend{}
			endpointSlices := []discoveryv1.EndpointSlice{}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  false,
				updatedBackends: []loadbalancer.BackendDetails{},
				drainingCount:   0,
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})

		t.Run("endpoint with no addresses", func(t *testing.T) {
			model := newHTTPBackendModel(newMockDeps(t))
			refPort := int32(rand.IntN(65534) + 1)

			// One endpoint with address, one without
			endpointWithAddr := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			endpointWithoutAddr := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			endpointWithoutAddr.Addresses = []string{}

			currentBackends := []loadbalancer.Backend{}
			endpointSlices := []discoveryv1.EndpointSlice{
				{Endpoints: []discoveryv1.Endpoint{endpointWithAddr, endpointWithoutAddr}},
			}

			params := identifyBackendsToUpdateParams{
				endpointPort:    refPort,
				currentBackends: currentBackends,
				endpointSlices:  endpointSlices,
			}

			// Only the endpoint with an address should be included
			expectedUpdatedBackends := []loadbalancer.BackendDetails{
				{
					IpAddress: &endpointWithAddr.Addresses[0],
					Port:      lo.ToPtr(int(refPort)),
					Drain:     lo.ToPtr(false),
				},
			}
			expectedResult := identifyBackendsToUpdateResult{
				updateRequired:  true, // Adding one backend
				updatedBackends: expectedUpdatedBackends,
				drainingCount:   0,
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
			assert.Equal(t, expectedResult.drainingCount, result.drainingCount)
		})
	})
}
