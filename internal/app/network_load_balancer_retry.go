package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
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
	response, err := ociClient.UpdateBackendSet(ctx, networkloadbalancer.UpdateBackendSetRequest{
		NetworkLoadBalancerId:   nlb.Id,
		BackendSetName:          new(backendSetName),
		UpdateBackendSetDetails: details,
	})
	if err != nil {
		if busyErr := networkLoadBalancerBusyErrorFromOCI(nlb.Id, err); busyErr != nil {
			return busyErr
		}
		return fmt.Errorf("failed to %s Network Load Balancer backend set %s: %w", operation, backendSetName, err)
	}
	if response.OpcWorkRequestId == nil {
		return nil
	}
	if err = workRequestsWatcher.WaitFor(ctx, *response.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed waiting for backend set %s %s: %w", backendSetName, operation, err)
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

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
