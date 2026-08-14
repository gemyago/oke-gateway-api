package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const networkLoadBalancerBusyRequeueAfter = 15 * time.Second

type networkLoadBalancerBusyError struct {
	id    string
	cause error
}

func (e *networkLoadBalancerBusyError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("OCI Network Load Balancer %s is busy: %v", e.id, e.cause)
	}
	return fmt.Sprintf("OCI Network Load Balancer %s is busy", e.id)
}

func (e *networkLoadBalancerBusyError) Unwrap() error {
	return e.cause
}

func networkLoadBalancerBusyRequeue() reconcile.Result {
	return reconcile.Result{RequeueAfter: networkLoadBalancerBusyRequeueAfter}
}

func updateNetworkLoadBalancerBackendSet(
	ctx context.Context,
	ociClient ociNetworkLoadBalancerClient,
	workRequestsWatcher workRequestsWatcher,
	nlb *networkloadbalancer.NetworkLoadBalancer,
	backendSetName string,
	operation string,
	details networkloadbalancer.UpdateBackendSetDetails,
) error {
	if busyErr := networkLoadBalancerBusyErrorFromState(nlb); busyErr != nil {
		return busyErr
	}
	desiredBackends := normalizeNetworkLoadBalancerBackendDetails(details.Backends)
	details.Backends = nil
	response, err := ociClient.UpdateBackendSet(ctx, networkloadbalancer.UpdateBackendSetRequest{
		NetworkLoadBalancerId:   nlb.Id,
		BackendSetName:          new(backendSetName),
		UpdateBackendSetDetails: details,
	})
	if err != nil {
		if busyErr := networkLoadBalancerBusyErrorFromOCI(nlb.Id, err); busyErr != nil {
			return busyErr
		}
		if busyErr := networkLoadBalancerMissingBackendSetErrorFromOCI(nlb.Id, err); busyErr != nil {
			return busyErr
		}
		return fmt.Errorf("failed to %s Network Load Balancer backend set %s: %w", operation, backendSetName, err)
	}
	if response.OpcWorkRequestId == nil {
		return fmt.Errorf(
			"failed to %s Network Load Balancer backend set %s: missing work request id",
			operation,
			backendSetName,
		)
	}
	if err = workRequestsWatcher.WaitFor(ctx, *response.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed waiting for backend set %s %s: %w", backendSetName, operation, err)
	}
	return syncNetworkLoadBalancerBackends(ctx, ociClient, workRequestsWatcher, nlb, backendSetName, desiredBackends)
}

func normalizeNetworkLoadBalancerBackendDetails(
	backends []networkloadbalancer.BackendDetails,
) []networkloadbalancer.BackendDetails {
	normalized := make(map[string]networkloadbalancer.BackendDetails, len(backends))
	for _, backend := range backends {
		mergeNetworkLoadBalancerBackend(normalized, backend)
	}
	return lo.Values(normalized)
}

func syncNetworkLoadBalancerBackends(
	ctx context.Context,
	ociClient ociNetworkLoadBalancerClient,
	workRequestsWatcher workRequestsWatcher,
	nlb *networkloadbalancer.NetworkLoadBalancer,
	backendSetName string,
	desired []networkloadbalancer.BackendDetails,
) error {
	current := mapNetworkLoadBalancerBackends(nil)
	if nlb.BackendSets != nil {
		if backendSet, ok := nlb.BackendSets[backendSetName]; ok {
			current = mapNetworkLoadBalancerBackends(backendSet.Backends)
		}
	}
	desiredMap := lo.SliceToMap(desired, func(backend networkloadbalancer.BackendDetails) (
		string,
		networkloadbalancer.BackendDetails,
	) {
		return networkLoadBalancerBackendKey(backend), backend
	})

	for name, backend := range desiredMap {
		currentBackend, found := current[name]
		switch {
		case !found:
			if err := createNetworkLoadBalancerBackend(
				ctx,
				ociClient,
				workRequestsWatcher,
				nlb.Id,
				backendSetName,
				backend,
			); err != nil {
				return err
			}
		case !networkLoadBalancerBackendDetailsEqual(currentBackend, backend):
			if err := updateNetworkLoadBalancerBackend(
				ctx,
				ociClient,
				workRequestsWatcher,
				nlb.Id,
				backendSetName,
				name,
				backend,
			); err != nil {
				return err
			}
		}
	}

	for name := range current {
		if _, found := desiredMap[name]; found {
			continue
		}
		if err := deleteNetworkLoadBalancerBackend(
			ctx,
			ociClient,
			workRequestsWatcher,
			nlb.Id,
			backendSetName,
			name,
		); err != nil {
			return err
		}
	}
	return nil
}

func mapNetworkLoadBalancerBackends(backends []networkloadbalancer.Backend) map[string]networkloadbalancer.Backend {
	return lo.SliceToMap(backends, func(backend networkloadbalancer.Backend) (string, networkloadbalancer.Backend) {
		return fmt.Sprintf("%s:%d", lo.FromPtr(backend.IpAddress), lo.FromPtr(backend.Port)), backend
	})
}

func networkLoadBalancerBackendDetailsEqual(
	current networkloadbalancer.Backend,
	desired networkloadbalancer.BackendDetails,
) bool {
	return lo.FromPtr(current.Weight) == lo.FromPtr(desired.Weight) &&
		lo.FromPtr(current.IsDrain) == lo.FromPtr(desired.IsDrain) &&
		lo.FromPtr(current.IsBackup) == lo.FromPtr(desired.IsBackup) &&
		lo.FromPtr(current.IsOffline) == lo.FromPtr(desired.IsOffline)
}

func createNetworkLoadBalancerBackend(
	ctx context.Context,
	ociClient ociNetworkLoadBalancerClient,
	workRequestsWatcher workRequestsWatcher,
	nlbID *string,
	backendSetName string,
	backend networkloadbalancer.BackendDetails,
) error {
	response, err := ociClient.CreateBackend(ctx, networkloadbalancer.CreateBackendRequest{
		NetworkLoadBalancerId: nlbID,
		BackendSetName:        new(backendSetName),
		CreateBackendDetails: networkloadbalancer.CreateBackendDetails{
			Name:      backend.Name,
			IpAddress: backend.IpAddress,
			TargetId:  backend.TargetId,
			Port:      backend.Port,
			Weight:    backend.Weight,
			IsDrain:   backend.IsDrain,
			IsBackup:  backend.IsBackup,
			IsOffline: backend.IsOffline,
		},
	})
	if err != nil {
		return fmt.Errorf(
			"failed to create Network Load Balancer backend %s: %w",
			networkLoadBalancerBackendKey(backend),
			err,
		)
	}
	return waitForNetworkLoadBalancerBackendOperation(
		ctx,
		workRequestsWatcher,
		response.OpcWorkRequestId,
		"create",
		backendSetName,
		networkLoadBalancerBackendKey(backend),
	)
}

func updateNetworkLoadBalancerBackend(
	ctx context.Context,
	ociClient ociNetworkLoadBalancerClient,
	workRequestsWatcher workRequestsWatcher,
	nlbID *string,
	backendSetName string,
	backendName string,
	backend networkloadbalancer.BackendDetails,
) error {
	response, err := ociClient.UpdateBackend(ctx, networkloadbalancer.UpdateBackendRequest{
		NetworkLoadBalancerId: nlbID,
		BackendSetName:        new(backendSetName),
		BackendName:           new(backendName),
		UpdateBackendDetails: networkloadbalancer.UpdateBackendDetails{
			Weight:    backend.Weight,
			IsDrain:   backend.IsDrain,
			IsBackup:  backend.IsBackup,
			IsOffline: backend.IsOffline,
		},
	})
	if err != nil {
		if networkLoadBalancerBackendNotFound(err) {
			return createNetworkLoadBalancerBackend(ctx, ociClient, workRequestsWatcher, nlbID, backendSetName, backend)
		}
		return fmt.Errorf("failed to update Network Load Balancer backend %s: %w", backendName, err)
	}
	return waitForNetworkLoadBalancerBackendOperation(
		ctx,
		workRequestsWatcher,
		response.OpcWorkRequestId,
		"update",
		backendSetName,
		backendName,
	)
}

func deleteNetworkLoadBalancerBackend(
	ctx context.Context,
	ociClient ociNetworkLoadBalancerClient,
	workRequestsWatcher workRequestsWatcher,
	nlbID *string,
	backendSetName string,
	backendName string,
) error {
	response, err := ociClient.DeleteBackend(ctx, networkloadbalancer.DeleteBackendRequest{
		NetworkLoadBalancerId: nlbID,
		BackendSetName:        new(backendSetName),
		BackendName:           new(backendName),
	})
	if err != nil {
		if networkLoadBalancerBackendNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete Network Load Balancer backend %s: %w", backendName, err)
	}
	return waitForNetworkLoadBalancerBackendOperation(
		ctx,
		workRequestsWatcher,
		response.OpcWorkRequestId,
		"delete",
		backendSetName,
		backendName,
	)
}

func waitForNetworkLoadBalancerBackendOperation(
	ctx context.Context,
	workRequestsWatcher workRequestsWatcher,
	workRequestID *string,
	operation string,
	backendSetName string,
	backendName string,
) error {
	if workRequestID == nil {
		return fmt.Errorf(
			"failed to %s Network Load Balancer backend %s in backend set %s: missing work request id",
			operation,
			backendName,
			backendSetName,
		)
	}
	if err := workRequestsWatcher.WaitFor(ctx, *workRequestID); err != nil {
		return fmt.Errorf(
			"failed waiting for backend %s in backend set %s %s: %w",
			backendName,
			backendSetName,
			operation,
			err,
		)
	}
	return nil
}

func networkLoadBalancerBusyErrorFromState(nlb *networkloadbalancer.NetworkLoadBalancer) error {
	if nlb == nil || nlb.LifecycleState != networkloadbalancer.LifecycleStateUpdating {
		return nil
	}
	return &networkLoadBalancerBusyError{id: ptrString(nlb.Id)}
}

func networkLoadBalancerBusyErrorFromOCI(id *string, err error) *networkLoadBalancerBusyError {
	if err == nil || !isNetworkLoadBalancerBusyServiceError(err) {
		return nil
	}
	return &networkLoadBalancerBusyError{id: ptrString(id), cause: err}
}

func networkLoadBalancerMissingBackendSetErrorFromOCI(id *string, err error) *networkLoadBalancerBusyError {
	if err == nil || !networkLoadBalancerBackendNotFound(err) {
		return nil
	}
	return &networkLoadBalancerBusyError{id: ptrString(id), cause: err}
}

func isNetworkLoadBalancerBusyServiceError(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	if !ok || serviceErr.GetHTTPStatusCode() != http.StatusConflict {
		return false
	}
	code := strings.ToLower(serviceErr.GetCode())
	message := strings.ToLower(serviceErr.GetMessage())
	return (strings.Contains(message, "invalid state transition") &&
		strings.Contains(message, "updating")) ||
		strings.Contains(code, "invalidstatetransition")
}

func networkLoadBalancerBackendNotFound(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok && serviceErr.GetHTTPStatusCode() == http.StatusNotFound
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
