package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"go.uber.org/dig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type acceptedGatewayDetails struct {
	gateway      gatewayv1.Gateway
	gatewayClass gatewayv1.GatewayClass
	config       types.GatewayConfig
}

type gatewayModel interface {
	// acceptReconcileRequest accepts a reconcile request and returns true if the request is accepted.
	// If returns false if the request is not relevant for this controller.
	// It returns true if the request is relevant for this controller.
	// It may return an error if there was error accepting the request.
	// If error happens, it may not be always known if the request is relevant.
	acceptReconcileRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *acceptedGatewayDetails,
	) (bool, error)

	programGateway(ctx context.Context, data *acceptedGatewayDetails) error
}

type gatewayModelImpl struct {
	client               k8sClient
	logger               *slog.Logger
	ociClient            ociLoadBalancerClient
	ociLoadBalancerModel ociLoadBalancerModel
}

func (m *gatewayModelImpl) acceptReconcileRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *acceptedGatewayDetails,
) (bool, error) {
	if err := m.client.Get(ctx, req.NamespacedName, &receiver.gateway); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.InfoContext(ctx, fmt.Sprintf("Gateway %s removed", req.NamespacedName))
			// TODO: We may need to handle deprovisioning, maybe via finalizer?
			return false, nil
		}
		return false, fmt.Errorf("failed to get Gateway %s: %w", req.NamespacedName, err)
	}

	if err := m.client.Get(ctx, apitypes.NamespacedName{
		Name: string(receiver.gateway.Spec.GatewayClassName),
	}, &receiver.gatewayClass); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.InfoContext(ctx, fmt.Sprintf("GatewayClass %s not found", receiver.gateway.Spec.GatewayClassName))
			return false, nil
		}
		return false, fmt.Errorf("failed to get GatewayClass %s: %w", receiver.gateway.Spec.GatewayClassName, err)
	}

	if receiver.gateway.Spec.Infrastructure == nil || receiver.gateway.Spec.Infrastructure.ParametersRef == nil {
		return false, &resourceStatusError{
			conditionType: string(gatewayv1.GatewayConditionAccepted),
			reason:        string(gatewayv1.GatewayReasonInvalidParameters),
			message:       "spec.infrastructure is missing parametersRef",
		}
	}

	configName := apitypes.NamespacedName{
		Namespace: receiver.gateway.Namespace,
		Name:      receiver.gateway.Spec.Infrastructure.ParametersRef.Name,
	}

	if err := m.client.Get(ctx, configName, &receiver.config); err != nil {
		return false, fmt.Errorf("failed to get GatewayConfig %s: %w", configName, err)
	}

	// TODO: Make sure config is complete

	return true, nil
}

func (m *gatewayModelImpl) programGateway(ctx context.Context, data *acceptedGatewayDetails) error {
	loadBalancerID := data.config.Spec.LoadBalancerID
	m.logger.DebugContext(ctx, "Fetching OCI Load Balancer details",
		slog.String("loadBalancerId", loadBalancerID),
	)

	request := loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: &loadBalancerID,
	}

	response, err := m.ociClient.GetLoadBalancer(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to get OCI Load Balancer %s: %w", loadBalancerID, err)
	}

	m.logger.DebugContext(ctx, "Successfully retrieved OCI Load Balancer details",
		slog.Any("loadBalancer", response.LoadBalancer),
	)

	defaultBackendSet, err := m.ociLoadBalancerModel.reconcileDefaultBackendSet(ctx, reconcileDefaultBackendParams{
		loadBalancerID:   loadBalancerID,
		knownBackendSets: response.LoadBalancer.BackendSets,
		gateway:          &data.gateway,
	})
	if err != nil {
		return fmt.Errorf("failed to program default backend set: %w", err)
	}

	for _, listenerSpec := range data.gateway.Spec.Listeners {
		// TODO: Support listener with hostname

		_, err = m.ociLoadBalancerModel.reconcileHTTPListener(ctx, reconcileHTTPListenerParams{
			loadBalancerID:        loadBalancerID,
			defaultBackendSetName: *defaultBackendSet.Name,
			knownListeners:        response.LoadBalancer.Listeners,
			listenerSpec:          &listenerSpec,
		})
		if err != nil {
			return fmt.Errorf("failed to program listener %s: %w", listenerSpec.Name, err)
		}
	}

	return nil
}

type gatewayModelDeps struct {
	dig.In

	K8sClient            k8sClient
	RootLogger           *slog.Logger
	OciClient            ociLoadBalancerClient
	OciLoadBalancerModel ociLoadBalancerModel
}

func newGatewayModel(deps gatewayModelDeps) gatewayModel {
	return &gatewayModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger,
		ociClient:            deps.OciClient,
		ociLoadBalancerModel: deps.OciLoadBalancerModel,
	}
}
