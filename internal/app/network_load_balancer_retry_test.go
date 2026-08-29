package app

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

func TestNetworkLoadBalancerRetry(t *testing.T) {
	t.Run("detects updating lifecycle state", func(t *testing.T) {
		nlbID := "nlb-id"
		err := networkLoadBalancerBusyErrorFromState(&networkloadbalancer.NetworkLoadBalancer{
			Id:             &nlbID,
			LifecycleState: networkloadbalancer.LifecycleStateUpdating,
		})

		var busyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &busyErr)
		assert.Contains(t, err.Error(), nlbID)
	})

	t.Run("ignores non-updating lifecycle state", func(t *testing.T) {
		err := networkLoadBalancerBusyErrorFromState(&networkloadbalancer.NetworkLoadBalancer{
			LifecycleState: networkloadbalancer.LifecycleStateActive,
		})

		require.NoError(t, err)
	})

	t.Run("detects OCI invalid updating state transition conflict", func(t *testing.T) {
		nlbID := "nlb-id"
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("InvalidStateTransition"),
			ociapi.RandomServiceErrorWithMessage(
				"Invalid State Transition of NLB lifeCycle state from Updating to Updating",
			),
		)

		err := networkLoadBalancerBusyErrorFromOCI(&nlbID, cause)

		require.ErrorIs(t, err, cause)
		assert.Contains(t, err.Error(), nlbID)
	})

	t.Run("detects OCI invalid state transition conflict by code", func(t *testing.T) {
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("InvalidStateTransition"),
			ociapi.RandomServiceErrorWithMessage("conflict"),
		)

		err := networkLoadBalancerBusyErrorFromOCI(nil, cause)

		require.ErrorIs(t, err, cause)
		assert.NotContains(t, err.Error(), "nlb-id")
	})

	t.Run("detects missing backend set as retryable OCI dependency", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound),
			ociapi.RandomServiceErrorWithCode("NotAuthorizedOrNotFound"),
			ociapi.RandomServiceErrorWithMessage("Unknown resource BackendSet bs_"+fake.Lorem().Word()),
		)

		err := networkLoadBalancerMissingBackendSetErrorFromOCI(&nlbID, cause)

		require.ErrorIs(t, err, cause)
		assert.Contains(t, err.Error(), nlbID)
	})

	t.Run("ignores nil OCI errors", func(t *testing.T) {
		require.Nil(t, networkLoadBalancerBusyErrorFromOCI(new(string), nil))
	})

	t.Run("ignores unrelated OCI errors", func(t *testing.T) {
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusInternalServerError),
			ociapi.RandomServiceErrorWithMessage("temporary failure"),
		)

		err := networkLoadBalancerBusyErrorFromOCI(new(string), cause)

		require.Nil(t, err)
	})

	t.Run("ignores non-service errors", func(t *testing.T) {
		err := networkLoadBalancerBusyErrorFromOCI(new(string), errors.New("boom"))

		require.Nil(t, err)
	})

	t.Run("normalizes duplicate backends before syncing members", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		updateWorkRequestID := fake.UUID().V4()
		createBackendWorkRequestID := fake.UUID().V4()
		ipAddress := fake.Internet().Ipv4()
		port := fake.IntBetween(1024, 65535)
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.MatchedBy(func(request networkloadbalancer.UpdateBackendSetRequest) bool {
				return lo.FromPtr(request.NetworkLoadBalancerId) == nlbID &&
					lo.FromPtr(request.BackendSetName) == backendSetName &&
					request.UpdateBackendSetDetails.Backends == nil
			})).
			Return(networkloadbalancer.UpdateBackendSetResponse{OpcWorkRequestId: &updateWorkRequestID}, nil)
		ociClient.EXPECT().
			CreateBackend(t.Context(), mock.MatchedBy(func(request networkloadbalancer.CreateBackendRequest) bool {
				return lo.FromPtr(request.NetworkLoadBalancerId) == nlbID &&
					lo.FromPtr(request.BackendSetName) == backendSetName &&
					lo.FromPtr(request.Name) == fmt.Sprintf("%s:%d", ipAddress, port) &&
					lo.FromPtr(request.IpAddress) == ipAddress &&
					lo.FromPtr(request.Port) == port &&
					lo.FromPtr(request.Weight) == 3
			})).
			Return(networkloadbalancer.CreateBackendResponse{OpcWorkRequestId: &createBackendWorkRequestID}, nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), updateWorkRequestID).Return(nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), createBackendWorkRequestID).Return(nil)

		err := updateNetworkLoadBalancerBackendSet(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id:             &nlbID,
				LifecycleState: networkloadbalancer.LifecycleStateActive,
			},
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{
				Backends: []networkloadbalancer.BackendDetails{
					{
						IpAddress: &ipAddress,
						Port:      &port,
						Weight:    new(1),
						IsDrain:   new(true),
					},
					{
						IpAddress: &ipAddress,
						Port:      &port,
						Weight:    new(2),
						IsDrain:   new(true),
					},
				},
			},
		)

		require.NoError(t, err)
	})

	t.Run("does not update backend set while network load balancer is updating", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)

		err := updateNetworkLoadBalancerBackendSet(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id:             &nlbID,
				LifecycleState: networkloadbalancer.LifecycleStateUpdating,
			},
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{},
		)

		var busyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &busyErr)
		assert.Contains(t, err.Error(), nlbID)
	})

	t.Run("returns retryable error when backend set update races OCI creation visibility", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		cause := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound),
			ociapi.RandomServiceErrorWithCode("NotAuthorizedOrNotFound"),
			ociapi.RandomServiceErrorWithMessage("Unknown resource BackendSet "+backendSetName),
		)
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.MatchedBy(func(request networkloadbalancer.UpdateBackendSetRequest) bool {
				return lo.FromPtr(request.NetworkLoadBalancerId) == nlbID &&
					lo.FromPtr(request.BackendSetName) == backendSetName
			})).
			Return(networkloadbalancer.UpdateBackendSetResponse{}, cause)

		err := updateNetworkLoadBalancerBackendSet(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id:             &nlbID,
				LifecycleState: networkloadbalancer.LifecycleStateActive,
			},
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{},
		)

		var busyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &busyErr)
		require.ErrorIs(t, err, cause)
	})

	t.Run("returns missing backend set update work request errors", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.Anything).
			Return(networkloadbalancer.UpdateBackendSetResponse{}, nil)

		err := updateNetworkLoadBalancerBackendSet(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id:             &nlbID,
				LifecycleState: networkloadbalancer.LifecycleStateActive,
			},
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{},
		)

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("maps network load balancer backends by ip and port", func(t *testing.T) {
		fake := faker.New()
		ipAddress := fake.Internet().Ipv4()
		port1 := fake.IntBetween(1024, 32767)
		port2 := fake.IntBetween(32768, 65535)
		backend1 := networkloadbalancer.Backend{
			Name:      new(fmt.Sprintf("%s:%d", ipAddress, port1)),
			IpAddress: &ipAddress,
			Port:      &port1,
		}
		backend2 := networkloadbalancer.Backend{
			Name:      new(fmt.Sprintf("%s:%d", ipAddress, port2)),
			IpAddress: &ipAddress,
			Port:      &port2,
		}

		result := mapNetworkLoadBalancerBackends([]networkloadbalancer.Backend{backend1, backend2})

		assert.Equal(t, map[string]networkloadbalancer.Backend{
			fmt.Sprintf("%s:%d", ipAddress, port1): backend1,
			fmt.Sprintf("%s:%d", ipAddress, port2): backend2,
		}, result)
	})

	t.Run("detects network load balancer backend option drift", func(t *testing.T) {
		fake := faker.New()
		backendName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		current := backendFromName(backendName, fake.IntBetween(1, 99))
		desired := backendDetailsFromName(backendName, lo.FromPtr(current.Weight), lo.FromPtr(current.IsDrain))
		desired.IsBackup = current.IsBackup
		desired.IsOffline = current.IsOffline

		assert.True(t, networkLoadBalancerBackendDetailsEqual(current, desired))

		desired.Weight = new(lo.FromPtr(current.Weight) + 1)
		assert.False(t, networkLoadBalancerBackendDetailsEqual(current, desired))

		desired.Weight = current.Weight
		desired.IsDrain = new(!lo.FromPtr(current.IsDrain))
		assert.False(t, networkLoadBalancerBackendDetailsEqual(current, desired))

		desired.IsDrain = current.IsDrain
		desired.IsBackup = new(!lo.FromPtr(current.IsBackup))
		assert.False(t, networkLoadBalancerBackendDetailsEqual(current, desired))

		desired.IsBackup = current.IsBackup
		desired.IsOffline = new(!lo.FromPtr(current.IsOffline))
		assert.False(t, networkLoadBalancerBackendDetailsEqual(current, desired))
	})

	t.Run("wraps backend set update wait errors", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		workRequestID := fake.UUID().V4()
		wantErr := errors.New("wait failed")
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.Anything).
			Return(networkloadbalancer.UpdateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

		err := updateNetworkLoadBalancerBackendSet(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id:             &nlbID,
				LifecycleState: networkloadbalancer.LifecycleStateActive,
			},
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{},
		)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed waiting for backend set")
	})

	t.Run("wraps non-retryable backend set update errors", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		wantErr := errors.New("update failed")
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			UpdateBackendSet(t.Context(), mock.Anything).
			Return(networkloadbalancer.UpdateBackendSetResponse{}, wantErr)

		err := updateNetworkLoadBalancerBackendSet(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id:             &nlbID,
				LifecycleState: networkloadbalancer.LifecycleStateActive,
			},
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{},
		)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update Network Load Balancer backend set")
	})

	t.Run("syncs backend member create update and delete", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		createName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		updateName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		deleteName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		keepName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		createWorkRequestID := fake.UUID().V4()
		updateWorkRequestID := fake.UUID().V4()
		deleteWorkRequestID := fake.UUID().V4()
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			CreateBackend(t.Context(), mock.MatchedBy(func(request networkloadbalancer.CreateBackendRequest) bool {
				return lo.FromPtr(request.NetworkLoadBalancerId) == nlbID &&
					lo.FromPtr(request.BackendSetName) == backendSetName &&
					lo.FromPtr(request.Name) == createName &&
					lo.FromPtr(request.Weight) == 2
			})).
			Return(networkloadbalancer.CreateBackendResponse{OpcWorkRequestId: &createWorkRequestID}, nil)
		ociClient.EXPECT().
			UpdateBackend(t.Context(), mock.MatchedBy(func(request networkloadbalancer.UpdateBackendRequest) bool {
				return lo.FromPtr(request.NetworkLoadBalancerId) == nlbID &&
					lo.FromPtr(request.BackendSetName) == backendSetName &&
					lo.FromPtr(request.BackendName) == updateName &&
					lo.FromPtr(request.Weight) == 3 &&
					lo.FromPtr(request.IsDrain)
			})).
			Return(networkloadbalancer.UpdateBackendResponse{OpcWorkRequestId: &updateWorkRequestID}, nil)
		ociClient.EXPECT().
			DeleteBackend(t.Context(), mock.MatchedBy(func(request networkloadbalancer.DeleteBackendRequest) bool {
				return lo.FromPtr(request.NetworkLoadBalancerId) == nlbID &&
					lo.FromPtr(request.BackendSetName) == backendSetName &&
					lo.FromPtr(request.BackendName) == deleteName
			})).
			Return(networkloadbalancer.DeleteBackendResponse{OpcWorkRequestId: &deleteWorkRequestID}, nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), createWorkRequestID).Return(nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), updateWorkRequestID).Return(nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteWorkRequestID).Return(nil)

		err := syncNetworkLoadBalancerBackends(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id: &nlbID,
				BackendSets: map[string]networkloadbalancer.BackendSet{
					backendSetName: {
						Backends: []networkloadbalancer.Backend{
							backendFromName(updateName, 1),
							backendFromName(deleteName, 1),
							backendFromName(keepName, 4),
						},
					},
				},
			},
			backendSetName,
			[]networkloadbalancer.BackendDetails{
				backendDetailsFromName(createName, 2, false),
				backendDetailsFromName(updateName, 3, true),
				backendDetailsFromName(keepName, 4, false),
			},
		)

		require.NoError(t, err)
	})

	t.Run("returns duplicate create backend errors", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		createName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		duplicateErr := ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("NotAuthorizedOrResourceAlreadyExists"),
			ociapi.RandomServiceErrorWithMessage("Duplicate backend IP/id + port combinations not allowed"),
		)
		ociClient.EXPECT().
			CreateBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.CreateBackendResponse{}, duplicateErr)

		err := syncNetworkLoadBalancerBackends(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id: &nlbID,
				BackendSets: map[string]networkloadbalancer.BackendSet{
					backendSetName: {},
				},
			},
			backendSetName,
			[]networkloadbalancer.BackendDetails{backendDetailsFromName(createName, 1, false)},
		)

		require.ErrorIs(t, err, duplicateErr)
	})

	t.Run("treats missing delete backend as already reconciled", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		deleteName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			DeleteBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.DeleteBackendResponse{}, ociapi.NewRandomServiceError(
				ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound),
			))

		err := syncNetworkLoadBalancerBackends(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id: &nlbID,
				BackendSets: map[string]networkloadbalancer.BackendSet{
					backendSetName: {Backends: []networkloadbalancer.Backend{backendFromName(deleteName, 1)}},
				},
			},
			backendSetName,
			nil,
		)

		require.NoError(t, err)
	})

	t.Run("creates backend when update target disappears", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		backendName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		createWorkRequestID := fake.UUID().V4()
		ociClient := NewMockociNetworkLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		ociClient.EXPECT().
			UpdateBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.UpdateBackendResponse{}, ociapi.NewRandomServiceError(
				ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound),
			))
		ociClient.EXPECT().
			CreateBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.CreateBackendResponse{OpcWorkRequestId: &createWorkRequestID}, nil)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), createWorkRequestID).Return(nil)

		err := syncNetworkLoadBalancerBackends(
			t.Context(),
			ociClient,
			workRequestsWatcher,
			&networkloadbalancer.NetworkLoadBalancer{
				Id: &nlbID,
				BackendSets: map[string]networkloadbalancer.BackendSet{
					backendSetName: {Backends: []networkloadbalancer.Backend{backendFromName(backendName, 1)}},
				},
			},
			backendSetName,
			[]networkloadbalancer.BackendDetails{backendDetailsFromName(backendName, 2, false)},
		)

		require.NoError(t, err)
	})

	t.Run("returns missing backend work request errors", func(t *testing.T) {
		fake := faker.New()
		backendSetName := "bs_" + fake.Lorem().Word()
		backendName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		assert.ErrorContains(t,
			waitForNetworkLoadBalancerBackendOperation(t.Context(), nil, nil, "create", backendSetName, backendName),
			"missing work request id",
		)
	})

	t.Run("wraps backend member operation errors", func(t *testing.T) {
		fake := faker.New()
		nlbID := "ocid1.networkloadbalancer.oc1.." + fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		backendName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		backend := backendDetailsFromName(backendName, 1, false)
		createErr := errors.New("create failed")
		updateErr := errors.New("update failed")
		deleteErr := errors.New("delete failed")

		createClient := NewMockociNetworkLoadBalancerClient(t)
		createClient.EXPECT().CreateBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.CreateBackendResponse{}, createErr)
		require.ErrorIs(t,
			createNetworkLoadBalancerBackend(t.Context(), createClient, nil, &nlbID, backendSetName, backend),
			createErr,
		)

		updateClient := NewMockociNetworkLoadBalancerClient(t)
		updateClient.EXPECT().UpdateBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.UpdateBackendResponse{}, updateErr)
		require.ErrorIs(t,
			updateNetworkLoadBalancerBackend(
				t.Context(),
				updateClient,
				nil,
				&nlbID,
				backendSetName,
				backendName,
				backend,
			),
			updateErr,
		)

		deleteClient := NewMockociNetworkLoadBalancerClient(t)
		deleteClient.EXPECT().DeleteBackend(t.Context(), mock.Anything).
			Return(networkloadbalancer.DeleteBackendResponse{}, deleteErr)
		require.ErrorIs(t,
			deleteNetworkLoadBalancerBackend(t.Context(), deleteClient, nil, &nlbID, backendSetName, backendName),
			deleteErr,
		)
	})

	t.Run("wraps backend work request wait errors", func(t *testing.T) {
		fake := faker.New()
		workRequestID := fake.UUID().V4()
		backendSetName := "bs_" + fake.Lorem().Word()
		backendName := fmt.Sprintf("%s:%d", fake.Internet().Ipv4(), fake.IntBetween(1024, 65535))
		wantErr := errors.New("wait failed")
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

		err := waitForNetworkLoadBalancerBackendOperation(
			t.Context(),
			workRequestsWatcher,
			&workRequestID,
			"create",
			backendSetName,
			backendName,
		)

		require.ErrorIs(t, err, wantErr)
	})
}

func backendFromName(name string, weight int) networkloadbalancer.Backend {
	ipAddress, port := splitNetworkLoadBalancerBackendName(name)
	return networkloadbalancer.Backend{
		Name:      new(name),
		IpAddress: new(ipAddress),
		Port:      new(port),
		Weight:    new(weight),
		IsDrain:   new(false),
	}
}

func backendDetailsFromName(name string, weight int, drain bool) networkloadbalancer.BackendDetails {
	ipAddress, port := splitNetworkLoadBalancerBackendName(name)
	return networkloadbalancer.BackendDetails{
		Name:      new(name),
		IpAddress: new(ipAddress),
		Port:      new(port),
		Weight:    new(weight),
		IsDrain:   new(drain),
	}
}

func splitNetworkLoadBalancerBackendName(name string) (string, int) {
	ipAddress, rawPort, _ := strings.Cut(name, ":")
	port, _ := strconv.Atoi(rawPort)
	return ipAddress, port
}
