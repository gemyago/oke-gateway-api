package e2ek8s

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const defaultPollInterval = 2 * time.Second

type WaitOptions struct {
	PollInterval time.Duration
}

func NewWaitOptions() *WaitOptions {
	return &WaitOptions{
		PollInterval: defaultPollInterval,
	}
}

func WaitForGatewayClassAccepted(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	name string,
	opts *WaitOptions,
) (*gatewayv1.GatewayClass, error) {
	resource := &gatewayv1.GatewayClass{}
	key := ctrlclient.ObjectKey{Name: name}

	err := waitFor(
		ctx,
		fmt.Sprintf("wait for GatewayClass %q Accepted=True", name),
		opts,
		func(ctx context.Context) (bool, string, error) {
			if err := kubeClient.Get(ctx, key, resource); err != nil {
				return false, "", err
			}

			return hasCondition(
				resource.Status.Conditions,
				string(gatewayv1.GatewayClassConditionStatusAccepted),
				resource.Generation,
			)
		},
	)
	if err != nil {
		return nil, err
	}

	return resource, nil
}

func WaitForGatewayAccepted(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	opts *WaitOptions,
) (*gatewayv1.Gateway, error) {
	return waitForGatewayCondition(
		ctx,
		kubeClient,
		namespace,
		name,
		string(gatewayv1.GatewayConditionAccepted),
		opts,
	)
}

func WaitForGatewayProgrammed(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	opts *WaitOptions,
) (*gatewayv1.Gateway, error) {
	return waitForGatewayCondition(
		ctx,
		kubeClient,
		namespace,
		name,
		string(gatewayv1.GatewayConditionProgrammed),
		opts,
	)
}

func WaitForHTTPRouteAccepted(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	gatewayName string,
	opts *WaitOptions,
) (*gatewayv1.HTTPRoute, error) {
	return waitForHTTPRouteCondition(
		ctx,
		kubeClient,
		namespace,
		name,
		gatewayName,
		string(gatewayv1.RouteConditionAccepted),
		opts,
	)
}

func WaitForHTTPRouteResolvedRefs(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	gatewayName string,
	opts *WaitOptions,
) (*gatewayv1.HTTPRoute, error) {
	return waitForHTTPRouteCondition(
		ctx,
		kubeClient,
		namespace,
		name,
		gatewayName,
		string(gatewayv1.RouteConditionResolvedRefs),
		opts,
	)
}

func WaitForHTTPRouteDeleted(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	opts *WaitOptions,
) error {
	resource := &gatewayv1.HTTPRoute{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}

	return waitFor(
		ctx,
		fmt.Sprintf("wait for HTTPRoute %s/%s deletion", namespace, name),
		opts,
		func(ctx context.Context) (bool, string, error) {
			if err := kubeClient.Get(ctx, key, resource); err != nil {
				if apierrors.IsNotFound(err) {
					return true, "", nil
				}

				return false, "", err
			}

			return false, "resource still exists", nil
		},
	)
}

func WaitForNamespacesDeleted(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	names []string,
	opts *WaitOptions,
) error {
	for _, name := range names {
		resource := &corev1.Namespace{}
		key := ctrlclient.ObjectKey{Name: name}

		if err := waitFor(
			ctx,
			fmt.Sprintf("wait for Namespace %q deletion", name),
			opts,
			func(ctx context.Context) (bool, string, error) {
				if err := kubeClient.Get(ctx, key, resource); err != nil {
					if apierrors.IsNotFound(err) {
						return true, "", nil
					}

					return false, "", err
				}

				if resource.DeletionTimestamp != nil {
					return false, "namespace is terminating", nil
				}

				return false, "namespace still exists", nil
			},
		); err != nil {
			return err
		}
	}

	return nil
}

func WaitForDeploymentReady(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	opts *WaitOptions,
) (*appsv1.Deployment, error) {
	resource := &appsv1.Deployment{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}

	err := waitFor(
		ctx,
		fmt.Sprintf("wait for Deployment %s/%s availability", namespace, name),
		opts,
		func(ctx context.Context) (bool, string, error) {
			if err := kubeClient.Get(ctx, key, resource); err != nil {
				return false, "", err
			}

			desiredReplicas := int32(1)
			if resource.Spec.Replicas != nil {
				desiredReplicas = *resource.Spec.Replicas
			}

			if resource.Status.ObservedGeneration < resource.Generation {
				return false, "controller has not observed the latest generation", nil
			}

			if resource.Status.AvailableReplicas < desiredReplicas ||
				resource.Status.ReadyReplicas < desiredReplicas {
				return false, fmt.Sprintf(
					"ready replicas %d/%d, available replicas %d/%d",
					resource.Status.ReadyReplicas,
					desiredReplicas,
					resource.Status.AvailableReplicas,
					desiredReplicas,
				), nil
			}

			ready, message := deploymentAvailable(resource.Status.Conditions)
			if !ready {
				return false, message, nil
			}

			return true, "", nil
		},
	)
	if err != nil {
		return nil, err
	}

	return resource, nil
}

func WaitForServiceEndpointsReady(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	serviceName string,
	opts *WaitOptions,
) ([]discoveryv1.EndpointSlice, error) {
	var endpointSlices discoveryv1.EndpointSliceList

	err := waitFor(
		ctx,
		fmt.Sprintf("wait for Service %s/%s ready endpoint slices", namespace, serviceName),
		opts,
		func(ctx context.Context) (bool, string, error) {
			endpointSlices = discoveryv1.EndpointSliceList{}
			if err := kubeClient.List(
				ctx,
				&endpointSlices,
				ctrlclient.InNamespace(namespace),
				ctrlclient.MatchingLabels{discoveryv1.LabelServiceName: serviceName},
			); err != nil {
				return false, "", err
			}

			if len(endpointSlices.Items) == 0 {
				return false, "no EndpointSlices found for Service", nil
			}

			for _, endpointSlice := range endpointSlices.Items {
				for _, endpoint := range endpointSlice.Endpoints {
					if len(endpoint.Addresses) == 0 {
						continue
					}

					if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
						return true, "", nil
					}
				}
			}

			return false, "no ready endpoint addresses published yet", nil
		},
	)
	if err != nil {
		return nil, err
	}

	return endpointSlices.Items, nil
}

func waitForGatewayCondition(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	conditionType string,
	opts *WaitOptions,
) (*gatewayv1.Gateway, error) {
	resource := &gatewayv1.Gateway{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}

	err := waitFor(
		ctx,
		fmt.Sprintf("wait for Gateway %s/%s %s=True", namespace, name, conditionType),
		opts,
		func(ctx context.Context) (bool, string, error) {
			if err := kubeClient.Get(ctx, key, resource); err != nil {
				return false, "", err
			}

			return hasCondition(resource.Status.Conditions, conditionType, resource.Generation)
		},
	)
	if err != nil {
		return nil, err
	}

	return resource, nil
}

func waitForHTTPRouteCondition(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	namespace string,
	name string,
	gatewayName string,
	conditionType string,
	opts *WaitOptions,
) (*gatewayv1.HTTPRoute, error) {
	resource := &gatewayv1.HTTPRoute{}
	key := ctrlclient.ObjectKey{Namespace: namespace, Name: name}

	err := waitFor(
		ctx,
		fmt.Sprintf("wait for HTTPRoute %s/%s %s=True", namespace, name, conditionType),
		opts,
		func(ctx context.Context) (bool, string, error) {
			if err := kubeClient.Get(ctx, key, resource); err != nil {
				return false, "", err
			}

			return hasRouteParentCondition(
				resource.Status.Parents,
				gatewayName,
				conditionType,
				resource.Generation,
			)
		},
	)
	if err != nil {
		return nil, err
	}

	return resource, nil
}

func waitFor(
	ctx context.Context,
	description string,
	opts *WaitOptions,
	check func(context.Context) (bool, string, error),
) error {
	pollInterval := defaultPollInterval
	if opts != nil && opts.PollInterval > 0 {
		pollInterval = opts.PollInterval
	}

	var lastMessage string
	for {
		done, message, err := check(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", description, err)
		}
		if done {
			return nil
		}

		lastMessage = message

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastMessage != "" {
				return fmt.Errorf("%s: %s: %w", description, lastMessage, ctx.Err())
			}

			return fmt.Errorf("%s: %w", description, ctx.Err())
		case <-timer.C:
		}
	}
}

func hasCondition(conditions []metav1.Condition, conditionType string, generation int64) (bool, string, error) {
	for _, condition := range conditions {
		if condition.Type != conditionType {
			continue
		}

		if condition.ObservedGeneration > 0 && condition.ObservedGeneration < generation {
			return false, "condition has stale observed generation", nil
		}

		if condition.Status == metav1.ConditionTrue {
			return true, "", nil
		}

		message := condition.Message
		if message == "" {
			message = fmt.Sprintf("condition status is %s with reason %s", condition.Status, condition.Reason)
		}

		return false, message, nil
	}

	return false, fmt.Sprintf("condition %s is not reported yet", conditionType), nil
}

func hasRouteParentCondition(
	parents []gatewayv1.RouteParentStatus,
	gatewayName string,
	conditionType string,
	generation int64,
) (bool, string, error) {
	for _, parent := range parents {
		if gatewayName != "" && string(parent.ParentRef.Name) != gatewayName {
			continue
		}

		return hasCondition(parent.Conditions, conditionType, generation)
	}

	if gatewayName == "" {
		return false, "route has no parent status entries yet", nil
	}

	return false, fmt.Sprintf("route has no parent status for Gateway %q", gatewayName), nil
}

func deploymentAvailable(conditions []appsv1.DeploymentCondition) (bool, string) {
	for _, condition := range conditions {
		if condition.Type != appsv1.DeploymentAvailable {
			continue
		}

		if condition.Status == corev1.ConditionTrue {
			return true, ""
		}

		message := condition.Message
		if message == "" {
			message = fmt.Sprintf("deployment condition is %s with reason %s", condition.Status, condition.Reason)
		}

		return false, message
	}

	return false, "deployment Available condition is not reported yet"
}
