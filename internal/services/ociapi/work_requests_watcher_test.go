package ociapi

import (
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
	})
}
