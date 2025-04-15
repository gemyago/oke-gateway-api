package ociapi

import (
	context "context"
	"fmt"
	"testing"
	"time"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/workrequests"
	"github.com/stretchr/testify/require"
)

func TestWorkRequestsWatcher(t *testing.T) {
	newMockDeps := func(t *testing.T) WorkRequestsWatcherDeps {
		return WorkRequestsWatcherDeps{
			Client:       NewMockworkRequestsClient(t),
			RootLogger:   diag.RootTestLogger(),
			pollInterval: 1 * time.Millisecond,
		}
	}

	makeMockWorkRequestResponse := func(status workrequests.WorkRequestStatusEnum) workrequests.GetWorkRequestResponse {
		return workrequests.GetWorkRequestResponse{
			WorkRequest: workrequests.WorkRequest{
				Status: status,
			},
		}
	}

	t.Run("WaitFor", func(t *testing.T) {
		t.Run("success_completes_eventually", func(t *testing.T) {
			deps := newMockDeps(t)
			w := NewWorkRequestsWatcher(deps)

			workRequestID := faker.UUIDHyphenated()

			responses := []workrequests.GetWorkRequestResponse{
				makeMockWorkRequestResponse(workrequests.WorkRequestStatusAccepted),
				makeMockWorkRequestResponse(workrequests.WorkRequestStatusInProgress),
				makeMockWorkRequestResponse(workrequests.WorkRequestStatusInProgress),
				makeMockWorkRequestResponse(workrequests.WorkRequestStatusSucceeded),
			}

			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			for _, response := range responses {
				mockClient.EXPECT().GetWorkRequest(
					t.Context(),
					workrequests.GetWorkRequestRequest{
						WorkRequestId: &workRequestID,
					},
				).Return(response, nil).Once()
			}

			err := w.WaitFor(t.Context(), workRequestID)
			require.NoError(t, err)
		})

		errorStates := []workrequests.WorkRequestStatusEnum{
			workrequests.WorkRequestStatusCanceled,
			workrequests.WorkRequestStatusFailed,
		}

		for _, state := range errorStates {
			t.Run(fmt.Sprintf("fail if %s state", state), func(t *testing.T) {
				deps := newMockDeps(t)
				w := NewWorkRequestsWatcher(deps)

				workRequestID := faker.UUIDHyphenated()

				responses := []workrequests.GetWorkRequestResponse{
					makeMockWorkRequestResponse(workrequests.WorkRequestStatusAccepted),
					makeMockWorkRequestResponse(workrequests.WorkRequestStatusInProgress),
					makeMockWorkRequestResponse(workrequests.WorkRequestStatusInProgress),
					makeMockWorkRequestResponse(state),
				}

				mockClient, _ := deps.Client.(*MockworkRequestsClient)

				for _, response := range responses {
					mockClient.EXPECT().GetWorkRequest(
						t.Context(),
						workrequests.GetWorkRequestRequest{
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

		t.Run("fail if context is cancelled", func(t *testing.T) {
			deps := newMockDeps(t)
			deps.pollInterval = 1 * time.Minute
			w := NewWorkRequestsWatcher(deps)

			workRequestID := faker.UUIDHyphenated()

			responses := []workrequests.GetWorkRequestResponse{
				makeMockWorkRequestResponse(workrequests.WorkRequestStatusAccepted),
			}

			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			cancelledCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			for _, response := range responses {
				mockClient.EXPECT().GetWorkRequest(
					cancelledCtx,
					workrequests.GetWorkRequestRequest{
						WorkRequestId: &workRequestID,
					},
				).Run(func(_ context.Context, _ workrequests.GetWorkRequestRequest) {
					cancel()
				}).Return(response, nil).Once()
			}
			err := w.WaitFor(cancelledCtx, workRequestID)
			require.ErrorIs(t, err, context.Canceled)
		})

		t.Run("fail if max poll duration is exceeded", func(t *testing.T) {
			deps := newMockDeps(t)
			deps.pollInterval = 2 * time.Second
			deps.maxPollDuration = 1 * time.Millisecond
			w := NewWorkRequestsWatcher(deps)

			workRequestID := faker.UUIDHyphenated()

			responses := []workrequests.GetWorkRequestResponse{
				makeMockWorkRequestResponse(workrequests.WorkRequestStatusAccepted),
			}

			mockClient, _ := deps.Client.(*MockworkRequestsClient)

			for _, response := range responses {
				mockClient.EXPECT().GetWorkRequest(
					t.Context(),
					workrequests.GetWorkRequestRequest{
						WorkRequestId: &workRequestID,
					},
				).Return(response, nil).Once()
			}
			err := w.WaitFor(t.Context(), workRequestID)
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	})
}
