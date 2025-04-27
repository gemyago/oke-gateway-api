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

			firstRefPort := int32(*rule2.BackendRefs[0].BackendObjectReference.Port)
			var wantBackends []loadbalancer.BackendDetails
			for _, backendRef := range rule2.BackendRefs {
				refSlice := endpointSlicesByRef[string(backendRef.BackendObjectReference.Name)]
				for _, endpoint := range refSlice.Endpoints {
					wantBackends = append(wantBackends, loadbalancer.BackendDetails{
						Port:      lo.ToPtr(int(firstRefPort)),
						IpAddress: &endpoint.Addresses[0],
						Drain:     lo.ToPtr(false),
					})
				}
			}

			backendSetName := backendSetName(httpRoute, rule2, 1)

			// Create a sample existing BackendSet using the fixture
			sampleBackendSet := makeRandomOCIBackendSet(
				randomOCIBackendSetWithNameOpt(backendSetName),
			)

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
							assert.ElementsMatch(t, wantBackends, req.Backends),
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

			err := model.syncRouteBackendRuleEndpoints(t.Context(), syncRouteBackendRuleEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
				ruleIndex: 1,
			})

			require.NoError(t, err)
		})

		t.Run("handle endpoint conditions", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPBackendModel(deps)

			ref := makeRandomBackendRef()
			rule := makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(ref))
			httpRoute := makeRandomHTTPRoute(randomHTTPRouteWithRandomRulesOpt(rule))
			config := makeRandomGatewayConfig()
			refPort := int32(*ref.BackendObjectReference.Port)

			// Define endpoints using makeRandomEndpoint with randomEndpointWithConditionsOpt
			// Include if Ready != false.
			// Drain if Terminating == true.

			// Case 1: Ready=true, Terminating=false -> Include, Drain=false
			readyNotTerminating := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(false)))
			// Case 2: Ready=true, Terminating=nil -> Include, Drain=false
			readyNilTerminating := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), nil))
			// Case 3: Ready=false -> Exclude
			notReadyEndpoint := makeRandomEndpoint(
				randomEndpointWithConditionsOpt(lo.ToPtr(false), nil),
			) // Terminating doesn't matter here

			// Case 4: Ready=true, Terminating=true -> Include, Drain=true
			terminatingReadyEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(true), lo.ToPtr(true)))
			// Case 5: Ready=false, Terminating=true -> Exclude (Ready=false takes priority)
			terminatingNotReadyEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(lo.ToPtr(false), lo.ToPtr(true)))
			// Case 6: Ready=nil, Terminating=true -> Include, Drain=true
			terminatingNilReadyEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(nil, lo.ToPtr(true)))
			// Case 7: Ready=nil, Terminating=false -> Include, Drain=false
			nilReadyNotTerminatingEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(nil, lo.ToPtr(false)))
			// Case 8: Ready=nil, Terminating=nil -> Include, Drain=false
			nilReadyNilTerminatingEndpoint := makeRandomEndpoint(randomEndpointWithConditionsOpt(nil, nil))

			endpointSlice := makeRandomEndpointSlice(
				randomEndpointSliceWithServiceNameOpt(string(ref.BackendObjectReference.Name)),
			)
			endpointSlice.Endpoints = []discoveryv1.Endpoint{
				readyNotTerminating,            // Case 1
				readyNilTerminating,            // Case 2
				notReadyEndpoint,               // Case 3 (Exclude)
				terminatingReadyEndpoint,       // Case 4
				terminatingNotReadyEndpoint,    // Case 5 (Exclude)
				terminatingNilReadyEndpoint,    // Case 6
				nilReadyNotTerminatingEndpoint, // Case 7
				nilReadyNilTerminatingEndpoint, // Case 8
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().List(
				t.Context(),
				mock.Anything,
				client.MatchingLabels{
					discoveryv1.LabelServiceName: string(ref.BackendObjectReference.Name),
				},
			).RunAndReturn(func(_ context.Context, ol client.ObjectList, _ ...client.ListOption) error {
				epSliceList, ok := ol.(*discoveryv1.EndpointSliceList)
				require.True(t, ok, "expected an EndpointSliceList")
				epSliceList.Items = append(epSliceList.Items, endpointSlice)
				return nil
			}).Once()

			// Expected backends (Include if Ready != false; Drain if Terminating == true)
			wantBackends := []loadbalancer.BackendDetails{
				{ // Case 1: readyNotTerminating
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &readyNotTerminating.Addresses[0],
					Drain:     lo.ToPtr(false),
				},
				{ // Case 2: readyNilTerminating
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &readyNilTerminating.Addresses[0],
					Drain:     lo.ToPtr(false),
				},
				// Case 3: notReadyEndpoint (Excluded)
				{ // Case 4: terminatingReadyEndpoint
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &terminatingReadyEndpoint.Addresses[0],
					Drain:     lo.ToPtr(true),
				},
				// Case 5: terminatingNotReadyEndpoint (Excluded)
				{ // Case 6: terminatingNilReadyEndpoint
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &terminatingNilReadyEndpoint.Addresses[0],
					Drain:     lo.ToPtr(true),
				},
				{ // Case 7: nilReadyNotTerminatingEndpoint
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &nilReadyNotTerminatingEndpoint.Addresses[0],
					Drain:     lo.ToPtr(false),
				},
				{ // Case 8: nilReadyNilTerminatingEndpoint
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &nilReadyNilTerminatingEndpoint.Addresses[0],
					Drain:     lo.ToPtr(false),
				},
			}

			backendSetName := backendSetName(httpRoute, rule, 0) // Assuming ruleIndex is 0

			// Create a sample existing BackendSet using the fixture
			sampleBackendSet := makeRandomOCIBackendSet(
				randomOCIBackendSetWithNameOpt(backendSetName),
			)

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
							assert.ElementsMatch(t, wantBackends, req.Backends),
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

			err := model.syncRouteBackendRuleEndpoints(t.Context(), syncRouteBackendRuleEndpointsParams{
				httpRoute: httpRoute,
				config:    config,
				ruleIndex: 0, // We only have one rule in this test
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
			}

			// Act
			result, err := model.identifyBackendsToUpdate(t.Context(), params)

			// Assert
			require.NoError(t, err)
			// This assertion is expected to fail initially
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
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
			}

			result, err := model.identifyBackendsToUpdate(t.Context(), params)
			require.NoError(t, err)
			assert.ElementsMatch(t, expectedResult.updatedBackends, result.updatedBackends)
			assert.Equal(t, expectedResult.updateRequired, result.updateRequired)
		})
	})
}
