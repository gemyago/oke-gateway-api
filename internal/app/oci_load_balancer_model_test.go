package app

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOciLoadBalancerModelImpl(t *testing.T) {
	makeMockDeps := func(t *testing.T) ociLoadBalancerModelDeps {
		return ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
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

			actualListener, err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, lbListener, actualListener)
		})

		t.Run("when listener does not exist", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomHTTPListener()
			wantListener := makeRandomOCIListener()

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

			workRequestID := faker.UUIDHyphenated()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  lo.ToPtr(string(gwListener.Name)),
					Port:                  lo.ToPtr(int(gwListener.Port)),
					Protocol:              lo.ToPtr(string(gwListener.Protocol)),
					DefaultBackendSetName: lo.ToPtr(params.defaultBackendSetName),
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			updatedLb := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomListenersOpt(),
				func(lb *loadbalancer.LoadBalancer) {
					lb.Listeners[string(gwListener.Name)] = wantListener
				},
			)

			ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: updatedLb,
			}, nil)

			actualListener, err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, wantListener, actualListener)
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

			actualBackendSet, err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, wantBs, actualBackendSet)
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

			actualBackendSet, err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, wantBs, actualBackendSet)
		})

		t.Run("fail if error getting backend set", func(t *testing.T) {
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
			}).Return(loadbalancer.GetBackendSetResponse{}, wantErr)

			_, err := model.reconcileBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})
}
