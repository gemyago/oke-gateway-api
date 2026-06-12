package ociapi

import (
	context "context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type stubNetworkLoadBalancerWorkRequestsClient struct {
	responses []networkloadbalancer.GetWorkRequestResponse
	err       error
	requests  []networkloadbalancer.GetWorkRequestRequest
}

func (s *stubNetworkLoadBalancerWorkRequestsClient) GetWorkRequest(
	_ context.Context,
	request networkloadbalancer.GetWorkRequestRequest,
) (networkloadbalancer.GetWorkRequestResponse, error) {
	s.requests = append(s.requests, request)
	if s.err != nil {
		return networkloadbalancer.GetWorkRequestResponse{}, s.err
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func TestWorkRequestsWatcher(t *testing.T) {
	newMockDeps := func(t *testing.T) WorkRequestsWatcherDeps {
		return WorkRequestsWatcherDeps{
			Client:       NewMockworkRequestsClient(t),
			RootLogger:   diag.RootTestLogger(),
			pollInterval: 1 * time.Millisecond,
		}
	}

	makeMockWorkRequestResponse := func(
		status loadbalancer.WorkRequestLifecycleStateEnum,
	) loadbalancer.GetWorkRequestResponse {
		return loadbalancer.GetWorkRequestResponse{
			WorkRequest: loadbalancer.WorkRequest{
				LifecycleState: status,
			},
		}
	}

	t.Run("WaitFor", func(t *testing.T) {
		t.Run("success_completes_eventually", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			w := NewWorkRequestsWatcher(deps)

			workRequestID := fake.UUID().V4()

			responses := []loadbalancer.GetWorkRequestResponse{
				makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateAccepted),
				makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateInProgress),
				makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateInProgress),
				makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateSucceeded),
			}

			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			for _, response := range responses {
				mockClient.EXPECT().GetWorkRequest(
					t.Context(),
					loadbalancer.GetWorkRequestRequest{
						WorkRequestId: &workRequestID,
					},
				).Return(response, nil).Once()
			}

			err := w.WaitFor(t.Context(), workRequestID)
			require.NoError(t, err)
		})

		errorStates := []loadbalancer.WorkRequestLifecycleStateEnum{
			loadbalancer.WorkRequestLifecycleStateFailed,
		}

		for _, state := range errorStates {
			t.Run(fmt.Sprintf("fail if %s state", state), func(t *testing.T) {
				fake := faker.New()
				deps := newMockDeps(t)
				w := NewWorkRequestsWatcher(deps)

				workRequestID := fake.UUID().V4()

				responses := []loadbalancer.GetWorkRequestResponse{
					makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateAccepted),
					makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateInProgress),
					makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateInProgress),
					makeMockWorkRequestResponse(state),
				}

				mockClient, _ := deps.Client.(*MockworkRequestsClient)

				for _, response := range responses {
					mockClient.EXPECT().GetWorkRequest(
						t.Context(),
						loadbalancer.GetWorkRequestRequest{
							WorkRequestId: &workRequestID,
						},
					).Return(response, nil).Once()
				}

				err := w.WaitFor(t.Context(), workRequestID)
				require.ErrorContains(t, err, fmt.Sprintf(
					"work request %s is in %s state", workRequestID, state),
				)
			})
		}

		t.Run("fail if get work request fails", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			w := NewWorkRequestsWatcher(deps)
			workRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))
			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			mockClient.EXPECT().GetWorkRequest(
				t.Context(),
				loadbalancer.GetWorkRequestRequest{
					WorkRequestId: &workRequestID,
				},
			).Return(loadbalancer.GetWorkRequestResponse{}, wantErr).Once()

			err := w.WaitFor(t.Context(), workRequestID)
			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("fail if context is cancelled", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			deps.pollInterval = 1 * time.Minute
			w := NewWorkRequestsWatcher(deps)

			workRequestID := fake.UUID().V4()

			responses := []loadbalancer.GetWorkRequestResponse{
				makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateAccepted),
			}

			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			cancelledCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			for _, response := range responses {
				mockClient.EXPECT().GetWorkRequest(
					cancelledCtx,
					loadbalancer.GetWorkRequestRequest{
						WorkRequestId: &workRequestID,
					},
				).Run(func(_ context.Context, _ loadbalancer.GetWorkRequestRequest) {
					cancel()
				}).Return(response, nil).Once()
			}
			err := w.WaitFor(cancelledCtx, workRequestID)
			require.ErrorIs(t, err, context.Canceled)
		})

		t.Run("fail if max poll duration is exceeded", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			deps.pollInterval = 2 * time.Second
			deps.maxPollDuration = 1 * time.Millisecond
			w := NewWorkRequestsWatcher(deps)

			workRequestID := fake.UUID().V4()

			responses := []loadbalancer.GetWorkRequestResponse{
				makeMockWorkRequestResponse(loadbalancer.WorkRequestLifecycleStateAccepted),
			}

			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			for _, response := range responses {
				mockClient.EXPECT().GetWorkRequest(
					t.Context(),
					loadbalancer.GetWorkRequestRequest{
						WorkRequestId: &workRequestID,
					},
				).Return(response, nil).Once()
			}
			err := w.WaitFor(t.Context(), workRequestID)
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	})
}

func TestNetworkLoadBalancerWorkRequestsWatcher(t *testing.T) {
	makeResponse := func(status networkloadbalancer.OperationStatusEnum) networkloadbalancer.GetWorkRequestResponse {
		return networkloadbalancer.GetWorkRequestResponse{
			WorkRequest: networkloadbalancer.WorkRequest{
				Status: status,
			},
		}
	}

	t.Run("WaitFor succeeds using network load balancer work request client", func(t *testing.T) {
		client := &stubNetworkLoadBalancerWorkRequestsClient{
			responses: []networkloadbalancer.GetWorkRequestResponse{
				makeResponse(networkloadbalancer.OperationStatusAccepted),
				makeResponse(networkloadbalancer.OperationStatusInProgress),
				makeResponse(networkloadbalancer.OperationStatusSucceeded),
			},
		}
		watcher := NewNetworkLoadBalancerWorkRequestsWatcher(NetworkLoadBalancerWorkRequestsWatcherDeps{
			Client:       client,
			RootLogger:   diag.RootTestLogger(),
			pollInterval: 1 * time.Millisecond,
		})
		workRequestID := faker.New().UUID().V4()

		err := watcher.WaitFor(t.Context(), workRequestID)

		require.NoError(t, err)
		require.Len(t, client.requests, 3)
		require.Equal(t, workRequestID, *client.requests[0].WorkRequestId)
	})

	t.Run("WaitFor returns network load balancer failure states", func(t *testing.T) {
		client := &stubNetworkLoadBalancerWorkRequestsClient{
			responses: []networkloadbalancer.GetWorkRequestResponse{
				makeResponse(networkloadbalancer.OperationStatusFailed),
			},
		}
		watcher := NewNetworkLoadBalancerWorkRequestsWatcher(NetworkLoadBalancerWorkRequestsWatcherDeps{
			Client:       client,
			RootLogger:   diag.RootTestLogger(),
			pollInterval: 1 * time.Millisecond,
		})

		err := watcher.WaitFor(t.Context(), faker.New().UUID().V4())

		require.ErrorContains(t, err, "network load balancer work request")
		require.ErrorContains(t, err, string(networkloadbalancer.OperationStatusFailed))
	})

	t.Run("WaitFor wraps network load balancer get errors", func(t *testing.T) {
		wantErr := errors.New("get failed")
		client := &stubNetworkLoadBalancerWorkRequestsClient{err: wantErr}
		watcher := NewNetworkLoadBalancerWorkRequestsWatcher(NetworkLoadBalancerWorkRequestsWatcherDeps{
			Client:     client,
			RootLogger: diag.RootTestLogger(),
		})

		err := watcher.WaitFor(t.Context(), faker.New().UUID().V4())

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get network load balancer work request")
	})

	t.Run("WaitFor respects network load balancer timeout and context cancellation", func(t *testing.T) {
		for name, tc := range map[string]struct {
			ctx             func() (context.Context, context.CancelFunc)
			maxPollDuration time.Duration
			wantErr         error
		}{
			"deadline": {
				ctx: func() (context.Context, context.CancelFunc) {
					return context.WithCancel(t.Context())
				},
				maxPollDuration: 1 * time.Millisecond,
				wantErr:         context.DeadlineExceeded,
			},
			"context": {
				ctx: func() (context.Context, context.CancelFunc) {
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					return ctx, func() {}
				},
				wantErr: context.Canceled,
			},
		} {
			t.Run(name, func(t *testing.T) {
				ctx, cancel := tc.ctx()
				defer cancel()
				client := &stubNetworkLoadBalancerWorkRequestsClient{
					responses: []networkloadbalancer.GetWorkRequestResponse{
						makeResponse(networkloadbalancer.OperationStatusAccepted),
					},
				}
				watcher := NewNetworkLoadBalancerWorkRequestsWatcher(NetworkLoadBalancerWorkRequestsWatcherDeps{
					Client:          client,
					RootLogger:      diag.RootTestLogger(),
					pollInterval:    1 * time.Hour,
					maxPollDuration: tc.maxPollDuration,
				})

				err := watcher.WaitFor(ctx, faker.New().UUID().V4())

				require.ErrorIs(t, err, tc.wantErr)
			})
		}
	})
}
