package app

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestOciLoadBalancerModelImpl(t *testing.T) {
	makeMockDeps := func(t *testing.T) ociLoadBalancerModelDeps {
		return ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
	}

	t.Run("reconcileDefaultBackendSet", func(t *testing.T) {
		t.Run("when backend set exists", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			existingBackendSet := makeRandomOCIBackendSet()
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			knownBackendSets := map[string]loadbalancer.BackendSet{
				wantBsName:             existingBackendSet,
				faker.UUIDHyphenated(): makeRandomOCIBackendSet(),
				faker.UUIDHyphenated(): makeRandomOCIBackendSet(),
			}

			params := reconcileDefaultBackendParams{
				loadBalancerID:   faker.UUIDHyphenated(),
				knownBackendSets: knownBackendSets,
				gateway:          gw,
			}
			actualBackendSet, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, existingBackendSet, actualBackendSet)
		})

		t.Run("when backend set does not exist", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"
			wantBs := makeRandomOCIBackendSet()

			params := reconcileDefaultBackendParams{
				loadBalancerID: faker.UUIDHyphenated(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			workRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     lo.ToPtr(int(80)),
						Protocol: lo.ToPtr("TCP"),
					},
					Policy: lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: wantBs,
			}, nil)

			actualBackendSet, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, wantBs, actualBackendSet)
		})

		t.Run("when create backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: faker.UUIDHyphenated(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     lo.ToPtr(int(80)),
						Protocol: lo.ToPtr("TCP"),
					},
					Policy: lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{}, wantErr)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when wait for backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: faker.UUIDHyphenated(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := faker.UUIDHyphenated()
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     lo.ToPtr(int(80)),
						Protocol: lo.ToPtr("TCP"),
					},
					Policy: lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when final get backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: faker.UUIDHyphenated(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := faker.UUIDHyphenated()
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     lo.ToPtr(int(80)),
						Protocol: lo.ToPtr("TCP"),
					},
					Policy: lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{}, wantErr)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("resolveAndTidyRoutingPolicy", func(t *testing.T) {
		t.Run("clean rules from current route", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			routeRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomNameOpt()),
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomNameOpt()),
			}

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(routeRules...),
			)

			routePolicyRules := lo.Map(routeRules,
				func(_ gatewayv1.HTTPRouteRule, i int) loadbalancer.RoutingRule {
					return loadbalancer.RoutingRule{
						Name: lo.ToPtr(ociListerPolicyRuleName(route, i)),
					}
				})

			routeOtherRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
				makeRandomOCIRoutingRule(),
				makeRandomOCIRoutingRule(),
			}

			allPolicyRules := make([]loadbalancer.RoutingRule, 0, len(routePolicyRules)+len(routeOtherRules))
			allPolicyRules = append(allPolicyRules, routePolicyRules...)
			allPolicyRules = append(allPolicyRules, routeOtherRules...)

			originalPolicy := makeRandomOCIRoutingPolicy(
				randomOCIRoutingPolicyWithRulesOpt(allPolicyRules),
			)

			params := resolveAndTidyRoutingPolicyParams{
				loadBalancerID: faker.UUIDHyphenated(),
				policyName:     *originalPolicy.Name,
				httpRoute:      route,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: originalPolicy.Name,
				LoadBalancerId:    &params.loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: originalPolicy,
			}, nil)

			actualPolicy, err := model.resolveAndTidyRoutingPolicy(t.Context(), params)
			require.NoError(t, err)

			wantPolicy := originalPolicy
			wantPolicy.Rules = routeOtherRules

			assert.Equal(t, wantPolicy, actualPolicy)
		})
	})

	t.Run("reconcileHTTPListener", func(t *testing.T) {
		t.Run("when listener exists", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()
			lbListener := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.Name = lo.ToPtr(string(gwListener.Name))
				},
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
					faker.UUIDHyphenated():  makeRandomOCIListener(),
				},
				defaultBackendSetName: faker.UUIDHyphenated(),
				listenerSpec:          &gwListener,
			}

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("when listener does not exist", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					faker.UUIDHyphenated(): makeRandomOCIListener(),
					faker.UUIDHyphenated(): makeRandomOCIListener(),
				},
				defaultBackendSetName: faker.UUIDHyphenated(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// For routing policy creation
			routingPolicyName := string(gwListener.Name) + "_policy"
			routingPolicyWorkRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), loadbalancer.CreateRoutingPolicyRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateRoutingPolicyDetails: loadbalancer.CreateRoutingPolicyDetails{
					Name:                     &routingPolicyName,
					ConditionLanguageVersion: loadbalancer.CreateRoutingPolicyDetailsConditionLanguageVersionV1,
					Rules: []loadbalancer.RoutingRule{
						{
							Name:      lo.ToPtr("default_catch_all"),
							Condition: lo.ToPtr("any(http.request.url.path sw '/')"),
							Actions: []loadbalancer.Action{
								loadbalancer.ForwardToBackendSet{
									BackendSetName: lo.ToPtr(params.defaultBackendSetName),
								},
							},
						},
					},
				},
			}).Return(loadbalancer.CreateRoutingPolicyResponse{
				OpcWorkRequestId: &routingPolicyWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			// For listener creation
			listenerWorkRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  lo.ToPtr(string(gwListener.Name)),
					Port:                  lo.ToPtr(int(gwListener.Port)),
					Protocol:              lo.ToPtr(string(gwListener.Protocol)),
					DefaultBackendSetName: lo.ToPtr(params.defaultBackendSetName),
					RoutingPolicyName:     lo.ToPtr(routingPolicyName),
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &listenerWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(nil)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("when create routing policy fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					faker.UUIDHyphenated(): makeRandomOCIListener(),
					faker.UUIDHyphenated(): makeRandomOCIListener(),
				},
				defaultBackendSetName: faker.UUIDHyphenated(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{}, wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when wait for routing policy fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					faker.UUIDHyphenated(): makeRandomOCIListener(),
					faker.UUIDHyphenated(): makeRandomOCIListener(),
				},
				defaultBackendSetName: faker.UUIDHyphenated(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			routingPolicyWorkRequestID := faker.UUIDHyphenated()
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{
					OpcWorkRequestId: &routingPolicyWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when create listener fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					faker.UUIDHyphenated(): makeRandomOCIListener(),
					faker.UUIDHyphenated(): makeRandomOCIListener(),
				},
				defaultBackendSetName: faker.UUIDHyphenated(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			routingPolicyWorkRequestID := faker.UUIDHyphenated()

			// Expect routing policy creation to succeed
			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{
					OpcWorkRequestId: &routingPolicyWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), mock.Anything).
				Return(loadbalancer.CreateListenerResponse{}, wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when wait for listener fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					faker.UUIDHyphenated(): makeRandomOCIListener(),
					faker.UUIDHyphenated(): makeRandomOCIListener(),
				},
				defaultBackendSetName: faker.UUIDHyphenated(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			routingPolicyWorkRequestID := faker.UUIDHyphenated()

			// Expect routing policy creation to succeed
			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{
					OpcWorkRequestId: &routingPolicyWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			listenerWorkRequestID := faker.UUIDHyphenated()
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), mock.Anything).
				Return(loadbalancer.CreateListenerResponse{
					OpcWorkRequestId: &listenerWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("reconcileBackendSet", func(t *testing.T) {
		t.Run("create new backend set", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			params := reconcileBackendSetParams{
				loadBalancerID: faker.UUIDHyphenated(),
				name:           faker.UUIDHyphenated(),
				healthChecker: &loadbalancer.HealthCheckerDetails{
					Protocol: lo.ToPtr("HTTP"),
					Port:     lo.ToPtr(rand.IntN(65535)),
				},
			}

			wantBs := makeRandomOCIBackendSet()

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &params.name,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name:          &params.name,
					HealthChecker: params.healthChecker,
					Policy:        lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &params.name,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: wantBs,
			}, nil)

			err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fail when create backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			params := reconcileBackendSetParams{
				loadBalancerID: faker.UUIDHyphenated(),
				name:           faker.UUIDHyphenated(),
				healthChecker: &loadbalancer.HealthCheckerDetails{
					Protocol: lo.ToPtr("HTTP"),
					Port:     lo.ToPtr(rand.IntN(65535)),
				},
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &params.name,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name:          &params.name,
					HealthChecker: params.healthChecker,
					Policy:        lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{}, wantErr)

			err := model.reconcileBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when wait for backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			params := reconcileBackendSetParams{
				loadBalancerID: faker.UUIDHyphenated(),
				name:           faker.UUIDHyphenated(),
				healthChecker: &loadbalancer.HealthCheckerDetails{
					Protocol: lo.ToPtr("HTTP"),
					Port:     lo.ToPtr(rand.IntN(65535)),
				},
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := faker.UUIDHyphenated()
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &params.name,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name:          &params.name,
					HealthChecker: params.healthChecker,
					Policy:        lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

			err := model.reconcileBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("return existing backend set", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			params := reconcileBackendSetParams{
				loadBalancerID: faker.UUIDHyphenated(),
				name:           faker.UUIDHyphenated(),
				healthChecker: &loadbalancer.HealthCheckerDetails{
					Protocol: lo.ToPtr("HTTP"),
					Port:     lo.ToPtr(rand.IntN(65535)),
				},
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			wantBs := makeRandomOCIBackendSet()
			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &params.name,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: wantBs,
			}, nil)

			err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
		})
	})

	t.Run("removeMissingListeners", func(t *testing.T) {
		t.Run("no listeners to remove", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			gwListener1 := makeRandomHTTPListener()
			gwListener2 := makeRandomHTTPListener()

			lbListener1 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = lo.ToPtr(string(gwListener1.Name))
			})
			lbListener2 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = lo.ToPtr(string(gwListener2.Name))
			})

			params := removeMissingListenersParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListener1.Name: lbListener1,
					*lbListener2.Name: lbListener2,
				},
				gatewayListeners: []gatewayv1.Listener{
					gwListener1,
					gwListener2,
				},
			}

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("some listeners to remove", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			gwListener1 := makeRandomHTTPListener()
			gwListener2 := makeRandomHTTPListener()
			lbListener1 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = lo.ToPtr(string(gwListener1.Name))
			})
			lbListener2 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = lo.ToPtr(string(gwListener2.Name))
			})
			lbListenerToRemove1 := makeRandomOCIListener()
			lbListenerToRemove2 := makeRandomOCIListener()

			params := removeMissingListenersParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListener1.Name:         lbListener1,
					*lbListener2.Name:         lbListener2,
					*lbListenerToRemove1.Name: lbListenerToRemove1,
					*lbListenerToRemove2.Name: lbListenerToRemove2,
				},
				gatewayListeners: []gatewayv1.Listener{
					gwListener1,
					gwListener2,
				},
			}

			// Expect deletion for both missing listeners
			workRequestID1 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove1.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID1}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID1).Return(nil).Once()

			workRequestID2 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove2.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID2}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("some listeners to remove with routing policy", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			gwListener1 := makeRandomHTTPListener()
			gwListener2 := makeRandomHTTPListener()
			lbListener1 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = lo.ToPtr(string(gwListener1.Name))
			})
			lbListener2 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = lo.ToPtr(string(gwListener2.Name))
			})
			lbListenerToRemove1 := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.RoutingPolicyName = lo.ToPtr("policy1" + faker.DomainName())
				},
			)
			lbListenerToRemove2 := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.RoutingPolicyName = lo.ToPtr("policy2" + faker.DomainName())
				},
			)

			params := removeMissingListenersParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListener1.Name:         lbListener1,
					*lbListener2.Name:         lbListener2,
					*lbListenerToRemove1.Name: lbListenerToRemove1,
					*lbListenerToRemove2.Name: lbListenerToRemove2,
				},
				gatewayListeners: []gatewayv1.Listener{
					gwListener1,
					gwListener2,
				},
			}

			deletePolicyRequestID1 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove1.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID1}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID1).Return(nil).Once()

			deletePolicyRequestID2 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove2.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID2}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID2).Return(nil).Once()

			// Expect deletion for both missing listeners
			deleteListenerRequestID := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove1.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteListenerRequestID).Return(nil).Once()

			workRequestID2 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove2.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID2}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fail when delete listener fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			lbListenerToRemove := makeRandomOCIListener()
			wantErr := errors.New(faker.Sentence())

			params := removeMissingListenersParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{}, wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when wait for listener fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			lbListenerToRemove := makeRandomOCIListener()
			wantErr := errors.New(faker.Sentence())

			params := removeMissingListenersParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			workRequestID := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("continues deleting even if one fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			lbListenerToRemove1 := makeRandomOCIListener()
			lbListenerToRemove2 := makeRandomOCIListener() // This one succeeds
			lbListenerToRemove3 := makeRandomOCIListener() // This one fails during wait

			wantErr1 := errors.New(faker.Sentence())
			wantErr3 := errors.New(faker.Sentence())

			params := removeMissingListenersParams{
				loadBalancerID: faker.UUIDHyphenated(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove1.Name: lbListenerToRemove1,
					*lbListenerToRemove2.Name: lbListenerToRemove2,
					*lbListenerToRemove3.Name: lbListenerToRemove3,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			// Expect deletion attempt for all three
			// 1. Fails on delete call
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove1.Name,
			}).Return(loadbalancer.DeleteListenerResponse{}, wantErr1).Once()

			// 2. Succeeds fully
			workRequestID2 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove2.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID2}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			// 3. Fails on wait
			workRequestID3 := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove3.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID3}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID3).Return(wantErr3).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err) // Should return combined error
			require.ErrorIs(t, err, wantErr1)
			require.ErrorIs(t, err, wantErr3)
		})
	})

	t.Run("appendRoutingRule", func(t *testing.T) {
		t.Run("append a new rule", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(makeRandomHTTPRouteRule(), makeRandomHTTPRouteRule()),
			)
			ruleIndex := 1
			existingRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
				makeRandomOCIRoutingRule(),
			}

			params := appendRoutingRuleParams{
				actualPolicyRules:  existingRules,
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
			}

			expectedCondition := faker.Sentence()
			routingRulesMapper.EXPECT().mapHTTPRouteMatchesToCondition(
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return(expectedCondition, nil).Once()

			expectedRuleName := ociListerPolicyRuleName(httpRoute, ruleIndex)
			expectedBackendSetName := ociBackendSetName(httpRoute, ruleIndex)

			wantRules := make([]loadbalancer.RoutingRule, 0, len(existingRules)+1)
			wantRules = append(wantRules, existingRules...)
			wantRules = append(wantRules, loadbalancer.RoutingRule{
				Name:      lo.ToPtr(expectedRuleName),
				Condition: lo.ToPtr(expectedCondition),
				Actions: []loadbalancer.Action{
					loadbalancer.ForwardToBackendSet{
						BackendSetName: lo.ToPtr(expectedBackendSetName),
					},
				},
			})

			actualRules, err := model.appendRoutingRule(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, wantRules, actualRules)
		})

		t.Run("fail when mapping matches to condition fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(makeRandomHTTPRouteRule()),
			)
			ruleIndex := 0
			existingRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := appendRoutingRuleParams{
				actualPolicyRules:  existingRules,
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
			}

			expectedErr := errors.New(faker.Sentence())
			routingRulesMapper.EXPECT().mapHTTPRouteMatchesToCondition(
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return("", expectedErr).Once()

			actualRules, err := model.appendRoutingRule(t.Context(), params)
			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.Nil(t, actualRules)
		})
	})

	t.Run("commitRoutingPolicy", func(t *testing.T) {
		t.Run("successfully update routing policy", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			params := commitRoutingPolicyParams{
				loadBalancerID: faker.UUIDHyphenated(),
				policy:         makeRandomOCIRoutingPolicy(),
			}
			workRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), loadbalancer.UpdateRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: params.policy.Name,
				UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
					ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
						params.policy.ConditionLanguageVersion,
					),
					Rules: params.policy.Rules,
				},
			}).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fail when update routing policy fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			params := commitRoutingPolicyParams{
				loadBalancerID: faker.UUIDHyphenated(),
				policy:         makeRandomOCIRoutingPolicy(),
			}
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), loadbalancer.UpdateRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: params.policy.Name,
				UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
					ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
						params.policy.ConditionLanguageVersion,
					),
					Rules: params.policy.Rules,
				},
			}).Return(loadbalancer.UpdateRoutingPolicyResponse{}, wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when wait for routing policy update fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			params := commitRoutingPolicyParams{
				loadBalancerID: faker.UUIDHyphenated(),
				policy:         makeRandomOCIRoutingPolicy(),
			}
			workRequestID := faker.UUIDHyphenated()
			wantErr := errors.New(faker.Sentence())

			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), loadbalancer.UpdateRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: params.policy.Name,
				UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
					ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
						params.policy.ConditionLanguageVersion,
					),
					Rules: params.policy.Rules,
				},
			}).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})
}

func Test_ociListerPolicyRuleName(t *testing.T) {
	type testCase struct {
		name      string
		route     gatewayv1.HTTPRoute
		ruleIndex int
		want      string
	}

	tests := []func() testCase{
		func() testCase {
			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "unnamed rule",
				route:     route,
				ruleIndex: index,
				want:      fmt.Sprintf("%s_r%04d", route.Name, index),
			}
		},
		func() testCase {
			ruleName := faker.UUIDHyphenated()
			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))
			fewRules[index].Name = lo.ToPtr(gatewayv1.SectionName(ruleName))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "named rule",
				route:     route,
				ruleIndex: index,
				want:      fmt.Sprintf("%s_r%04d_%s", route.Name, index, ruleName),
			}
		},
	}

	for _, tc := range tests {
		tc := tc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociListerPolicyRuleName(tc.route, tc.ruleIndex)
			assert.Equal(t, tc.want, got)
		})
	}
}

func Test_ociBackendSetName(t *testing.T) {
	type testCase struct {
		name      string
		route     gatewayv1.HTTPRoute
		ruleIndex int
		want      string
	}

	tests := []func() testCase{
		func() testCase {
			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "unnamed rule",
				route:     route,
				ruleIndex: index,
				want:      fmt.Sprintf("%s-r%04d", route.Name, index),
			}
		},
		func() testCase {
			ruleName := faker.UUIDHyphenated()

			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))
			fewRules[index].Name = lo.ToPtr(gatewayv1.SectionName(ruleName))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "named rule",
				route:     route,
				ruleIndex: index,
				want:      fmt.Sprintf("%s-r%04d-%s", route.Name, index, ruleName),
			}
		},
	}

	for _, tc := range tests {
		tc := tc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociBackendSetName(tc.route, tc.ruleIndex)
			assert.Equal(t, tc.want, got)
		})
	}
}
