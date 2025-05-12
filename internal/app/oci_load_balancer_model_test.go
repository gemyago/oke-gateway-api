package app

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
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
			service := makeRandomService()

			params := reconcileBackendSetParams{
				loadBalancerID: faker.UUIDHyphenated(),
				service:        service,
			}

			wantBsName := ociBackendSetNameFromService(service)

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: lo.ToPtr("TCP"),
						Port:     lo.ToPtr(int(service.Spec.Ports[0].Port)),
					},
					Policy: lo.ToPtr("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("do nothing if backend set exists", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			exitingBs := makeRandomOCIBackendSet(func(bs *loadbalancer.BackendSet) {
				bs.Name = lo.ToPtr(wantBsName)
			})

			params := reconcileBackendSetParams{
				loadBalancerID: faker.UUIDHyphenated(),
				service:        service,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: exitingBs,
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

	t.Run("makeRoutingRule", func(t *testing.T) {
		t.Run("successfully create a routing rule", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
			)
			ruleIndex := 0

			params := makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
			}

			expectedCondition := faker.Sentence()
			routingRulesMapper.EXPECT().mapHTTPRouteMatchesToCondition(
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return(expectedCondition, nil).Once()

			expectedRuleName := ociListerPolicyRuleName(httpRoute, ruleIndex)
			expectedBackendSets := lo.Map(refs, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return ociBackendSetNameFromBackendRef(httpRoute, ref)
			})

			expectedRule := loadbalancer.RoutingRule{
				Name:      lo.ToPtr(expectedRuleName),
				Condition: lo.ToPtr(expectedCondition),
				Actions: lo.Map(expectedBackendSets, func(backendSet string, _ int) loadbalancer.Action {
					return loadbalancer.ForwardToBackendSet{
						BackendSetName: lo.ToPtr(backendSet),
					}
				}),
			}

			actualRule, err := model.makeRoutingRule(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, expectedRule, actualRule)
		})

		t.Run("fail when mapping matches to condition fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(makeRandomHTTPRouteRule()),
			)
			ruleIndex := 0

			params := makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
			}

			expectedErr := errors.New(faker.Sentence())
			routingRulesMapper.EXPECT().mapHTTPRouteMatchesToCondition(
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return("", expectedErr).Once()

			_, err := model.makeRoutingRule(t.Context(), params)
			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("commitRoutingPolicy", func(t *testing.T) {
		t.Run("successfully merge and update routing policy", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := faker.UUIDHyphenated()
			listenerName := faker.UUIDHyphenated()
			policyName := listenerPolicyName(listenerName)

			existingRulePrefixes := []string{
				"routes-1",
				"routes-2",
				"routes-3",
				"routes-4",
			}

			existingRules := lo.Map(existingRulePrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      lo.ToPtr(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: lo.ToPtr(faker.Sentence()),
				}
			})

			existingRules = append(existingRules, loadbalancer.RoutingRule{
				Name:      lo.ToPtr(string(defaultCatchAllRuleName)),
				Condition: lo.ToPtr(faker.Sentence()),
			})

			newRulesPrefixes := []string{
				"new-routes-1",
				"new-routes-2",
				"new-routes-3",
			}
			newRules := lo.Map(newRulesPrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      lo.ToPtr(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: lo.ToPtr(faker.Sentence()),
				}
			})

			replacedRuleIndex := rand.IntN(len(existingRulePrefixes))
			replacedRule := loadbalancer.RoutingRule{
				Name:      lo.ToPtr(fmt.Sprintf("%s%04d", existingRulePrefixes[replacedRuleIndex], replacedRuleIndex)),
				Condition: lo.ToPtr(faker.Sentence()),
			}

			rulesToCommit := make([]loadbalancer.RoutingRule, 0, len(existingRules)+len(newRules))
			rulesToCommit = append(rulesToCommit, existingRules...)
			rulesToCommit[replacedRuleIndex] = replacedRule
			rulesToCommit = append(rulesToCommit, newRules...)

			wantMergedRules := make([]loadbalancer.RoutingRule, 0, len(rulesToCommit))
			wantMergedRules = append(wantMergedRules, rulesToCommit...)

			// Sort the expected rules
			sort.Slice(wantMergedRules, func(i, j int) bool {
				ruleI := lo.FromPtr(wantMergedRules[i].Name)
				ruleJ := lo.FromPtr(wantMergedRules[j].Name)
				if ruleI == defaultCatchAllRuleName {
					return false
				}
				if ruleJ == defaultCatchAllRuleName {
					return true
				}
				return ruleI < ruleJ
			})

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    rulesToCommit,
			}

			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     lo.ToPtr(policyName),
				Rules:                    existingRules,
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
			}

			// Expect to get the current routing policy
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: lo.ToPtr(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			// Expect to update the policy with merged rules
			workRequestID := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), loadbalancer.UpdateRoutingPolicyRequest{
				LoadBalancerId:    &loadBalancerID,
				RoutingPolicyName: lo.ToPtr(policyName),
				UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
					ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
						existingPolicy.ConditionLanguageVersion,
					),
					Rules: wantMergedRules,
				},
			}).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("delete previously programmed rules that are not in the new policy", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := faker.UUIDHyphenated()
			listenerName := faker.UUIDHyphenated()
			policyName := listenerPolicyName(listenerName)

			existingRulePrefixes := []string{
				"routes-1",
				"routes-2",
				"routes-3",
				"routes-4",
			}

			deletedRulePrefixes := []string{
				"deleted-routes-1",
				"deleted-routes-2",
				"deleted-routes-3",
			}

			deletedRules := lo.Map(deletedRulePrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      lo.ToPtr(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: lo.ToPtr(faker.Sentence()),
				}
			})

			existingRules := lo.Map(existingRulePrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      lo.ToPtr(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: lo.ToPtr(faker.Sentence()),
				}
			})

			existingRules = append(existingRules, loadbalancer.RoutingRule{
				Name:      lo.ToPtr(string(defaultCatchAllRuleName)),
				Condition: lo.ToPtr(faker.Sentence()),
			})

			newRulesPrefixes := []string{
				"new-routes-1",
				"new-routes-2",
				"new-routes-3",
			}
			newRules := lo.Map(newRulesPrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      lo.ToPtr(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: lo.ToPtr(faker.Sentence()),
				}
			})

			replacedRuleIndex := rand.IntN(len(existingRulePrefixes))
			replacedRule := loadbalancer.RoutingRule{
				Name:      lo.ToPtr(fmt.Sprintf("%s%04d", existingRulePrefixes[replacedRuleIndex], replacedRuleIndex)),
				Condition: lo.ToPtr(faker.Sentence()),
			}

			rulesToCommit := make([]loadbalancer.RoutingRule, 0, len(existingRules)+len(newRules))
			rulesToCommit = append(rulesToCommit, existingRules...)
			rulesToCommit[replacedRuleIndex] = replacedRule
			rulesToCommit = append(rulesToCommit, newRules...)

			wantMergedRules := make([]loadbalancer.RoutingRule, 0, len(rulesToCommit))
			wantMergedRules = append(wantMergedRules, rulesToCommit...)

			// Sort the expected rules
			sort.Slice(wantMergedRules, func(i, j int) bool {
				ruleI := lo.FromPtr(wantMergedRules[i].Name)
				ruleJ := lo.FromPtr(wantMergedRules[j].Name)
				if ruleI == defaultCatchAllRuleName {
					return false
				}
				if ruleJ == defaultCatchAllRuleName {
					return true
				}
				return ruleI < ruleJ
			})

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    rulesToCommit,
				prevPolicyRules: lo.Map(deletedRules, func(rule loadbalancer.RoutingRule, _ int) string {
					return lo.FromPtr(rule.Name)
				}),
			}

			allExistingRules := make([]loadbalancer.RoutingRule, 0, len(existingRules)+len(deletedRules))
			allExistingRules = append(allExistingRules, existingRules...)
			allExistingRules = append(allExistingRules, deletedRules...)

			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     lo.ToPtr(policyName),
				Rules:                    allExistingRules,
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
			}

			// Expect to get the current routing policy
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: lo.ToPtr(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			// Expect to update the policy with merged rules
			workRequestID := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.MatchedBy(
				func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					assert.Equal(t, wantMergedRules, req.UpdateRoutingPolicyDetails.Rules)
					return true
				},
			)).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fail when get routing policy fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := faker.UUIDHyphenated()
			listenerName := faker.UUIDHyphenated()
			policyName := listenerPolicyName(listenerName)

			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			}

			wantErr := errors.New(faker.Sentence())
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: lo.ToPtr(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{}, wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when update routing policy fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := faker.UUIDHyphenated()
			listenerName := faker.UUIDHyphenated()
			policyName := listenerPolicyName(listenerName)

			existingPolicy := makeRandomOCIRoutingPolicy(
				func(policy *loadbalancer.RoutingPolicy) {
					policy.Name = lo.ToPtr(policyName)
				},
			)

			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: lo.ToPtr(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			wantErr := errors.New(faker.Sentence())
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{}, wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when wait for update fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := faker.UUIDHyphenated()
			listenerName := faker.UUIDHyphenated()
			policyName := listenerPolicyName(listenerName)

			existingPolicy := makeRandomOCIRoutingPolicy(
				func(policy *loadbalancer.RoutingPolicy) {
					policy.Name = lo.ToPtr(policyName)
				},
			)

			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: lo.ToPtr(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			workRequestID := faker.UUIDHyphenated()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{
					OpcWorkRequestId: &workRequestID,
				}, nil)

			wantErr := errors.New(faker.Sentence())
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
				randomHTTPRouteWithNameOpt("route_"+faker.Word()),
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "unnamed rule",
				route:     route,
				ruleIndex: index,
				want:      fmt.Sprintf("p%04d_%s", index, route.Name),
			}
		},
		func() testCase {
			rule := makeRandomHTTPRouteRule()
			index := 0

			unsanitizedParentName := fmt.Sprintf("route-%d-!#:-rule", rand.IntN(1000))
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(rule),
				randomHTTPRouteWithNameOpt(unsanitizedParentName),
			)
			unsanitizedInput := fmt.Sprintf("p%04d_%s", index, unsanitizedParentName)
			want := ociapi.ConstructOCIResourceName(unsanitizedInput, ociapi.OCIResourceNameConfig{
				MaxLength:           32,
				InvalidCharsPattern: invalidCharsForPolicyNamePattern,
			})

			return testCase{
				name:      "sanitized unnamed rule",
				route:     route,
				ruleIndex: index,
				want:      want,
			}
		},
		func() testCase {
			ruleName := "rl_" + faker.Word()
			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))
			fewRules[index].Name = lo.ToPtr(gatewayv1.SectionName(ruleName))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithNameOpt("rt_"+faker.Word()),
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "named rule",
				route:     route,
				ruleIndex: index,
				want:      fmt.Sprintf("p%04d_%s_%s", index, route.Name, ruleName),
			}
		},
		func() testCase {
			unsanitizedRuleName := fmt.Sprintf("rule-%d-!#:-rule", rand.IntN(1000))

			rule := makeRandomHTTPRouteRule()
			rule.Name = lo.ToPtr(gatewayv1.SectionName(unsanitizedRuleName))
			index := 0

			unsanitizedParentName := fmt.Sprintf("route-%d-!#:-rule", rand.IntN(1000))
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(rule),
				randomHTTPRouteWithNameOpt(unsanitizedParentName),
			)
			unsanitizedInput := fmt.Sprintf("p%04d_%s_%s", index, unsanitizedParentName, unsanitizedRuleName)
			want := ociapi.ConstructOCIResourceName(unsanitizedInput, ociapi.OCIResourceNameConfig{
				MaxLength:           32,
				InvalidCharsPattern: invalidCharsForPolicyNamePattern,
			})

			return testCase{
				name:      "sanitized named rule",
				route:     route,
				ruleIndex: index,
				want:      want,
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

func Test_ociBackendSetNameFromBackendRef(t *testing.T) {
	type testCase struct {
		name       string
		httpRoute  gatewayv1.HTTPRoute
		backendRef gatewayv1.HTTPBackendRef
		want       string
	}

	tests := []func() testCase{
		func() testCase {
			refName := faker.Username()
			refNamespace := faker.Word() + "-ns"
			httpRouteNs := faker.Word() + "-route-ns"

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNameOpt(faker.Username()+"-route"),
				func(hr *gatewayv1.HTTPRoute) {
					hr.Namespace = httpRouteNs
				},
			)
			backendRef := makeRandomBackendRef(
				func(br *gatewayv1.HTTPBackendRef) {
					br.Name = gatewayv1.ObjectName(refName)
					br.Namespace = lo.ToPtr(gatewayv1.Namespace(refNamespace))
				},
			)
			wantName := fmt.Sprintf("%s-%s", refNamespace, refName)
			return testCase{
				name:       "with namespace in backendRef",
				httpRoute:  httpRoute,
				backendRef: backendRef,
				want: ociapi.ConstructOCIResourceName(wantName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
		func() testCase {
			routeNs := faker.Word() + "-route-namespace"
			refName := faker.Username() + "-svc"

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNameOpt(faker.Username()+"-route"),
				func(hr *gatewayv1.HTTPRoute) {
					hr.Namespace = routeNs
				},
			)
			backendRef := makeRandomBackendRef(
				func(br *gatewayv1.HTTPBackendRef) {
					br.Name = gatewayv1.ObjectName(refName)
					br.Namespace = nil // No namespace in ref
				},
			)
			wantName := fmt.Sprintf("%s-%s", routeNs, refName)
			return testCase{
				name:       "without namespace in backendRef, uses route namespace",
				httpRoute:  httpRoute,
				backendRef: backendRef,
				want: ociapi.ConstructOCIResourceName(wantName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
		func() testCase {
			longRefNs := faker.UUIDDigit()[0:16]
			longRefName := faker.UUIDDigit()[0:16]

			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef(
				func(br *gatewayv1.HTTPBackendRef) {
					br.Name = gatewayv1.ObjectName(longRefName)
					br.Namespace = lo.ToPtr(gatewayv1.Namespace(longRefNs))
				},
			)
			originalName := fmt.Sprintf("%s-%s", longRefNs, longRefName)
			assert.Greater(t, len(originalName), maxBackendSetNameLength)
			return testCase{
				name:       "long name truncated",
				httpRoute:  httpRoute,
				backendRef: backendRef,
				want: ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
	}

	for _, tcFunc := range tests {
		tc := tcFunc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociBackendSetNameFromBackendRef(tc.httpRoute, tc.backendRef)
			assert.Equal(t, tc.want, got)
		})
	}
}

func Test_ociBackendSetNameFromService(t *testing.T) {
	type testCase struct {
		name    string
		service v1.Service
		want    string
	}

	tests := []func() testCase{
		func() testCase {
			svcNs := faker.Word() + "-ns"
			svcName := faker.Username() + "-svc"
			service := makeRandomService(
				func(s *v1.Service) {
					s.Name = svcName
					s.Namespace = svcNs
				},
			)
			wantName := fmt.Sprintf("%s-%s", svcNs, svcName)
			return testCase{
				name:    "standard name",
				service: service,
				want: ociapi.ConstructOCIResourceName(wantName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
		func() testCase {
			longSvcNs := faker.UUIDDigit()[0:20]   // Ensure length that will cause truncation
			longSvcName := faker.UUIDDigit()[0:20] // Ensure length that will cause truncation
			service := makeRandomService(
				func(s *v1.Service) {
					s.Name = longSvcName
					s.Namespace = longSvcNs
				},
			)
			originalName := fmt.Sprintf("%s-%s", longSvcNs, longSvcName)
			assert.Greater(t, len(originalName), maxBackendSetNameLength)
			return testCase{
				name:    "long name truncated",
				service: service,
				want: ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
	}

	for _, tcFunc := range tests {
		tc := tcFunc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociBackendSetNameFromService(tc.service)
			assert.Equal(t, tc.want, got)
		})
	}
}
