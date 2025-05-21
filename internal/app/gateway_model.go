package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type resolvedGatewayDetails struct {
	gateway      gatewayv1.Gateway
	gatewayClass gatewayv1.GatewayClass

	// Map of secret full name to the secret object
	// holds all secrets that are used by the gateway (mostly listeners certificates)
	gatewaySecrets map[string]corev1.Secret

	config types.GatewayConfig
}

type gatewayModel interface {
	// resolveReconcileRequest will resolve related resources for the reconcile request.
	// If returns false if the request is not relevant for this controller.
	// It returns true if the request is relevant for this controller.
	// It may return an error if there was error resolving the request.
	// If error happens, it may not be always known if the request is relevant.
	resolveReconcileRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *resolvedGatewayDetails,
	) (bool, error)

	programGateway(ctx context.Context, data *resolvedGatewayDetails) error

	isProgrammed(ctx context.Context, data *resolvedGatewayDetails) bool

	setProgrammed(ctx context.Context, data *resolvedGatewayDetails) error
}

type gatewayModelImpl struct {
	client               k8sClient
	logger               *slog.Logger
	ociClient            ociLoadBalancerClient
	ociLoadBalancerModel ociLoadBalancerModel
	resourcesModel       resourcesModel
}

func (m *gatewayModelImpl) resolveReconcileRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *resolvedGatewayDetails,
) (bool, error) {
	if err := m.client.Get(ctx, req.NamespacedName, &receiver.gateway); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.InfoContext(ctx, fmt.Sprintf("Gateway %s not found", req.NamespacedName))
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

	if receiver.gatewayClass.Spec.ControllerName != gatewayv1.GatewayController(ControllerClassName) {
		m.logger.InfoContext(
			ctx,
			fmt.Sprintf("GatewayClass %s is not managed by this controller", receiver.gateway.Spec.GatewayClassName),
		)
		return false, nil
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
		if apierrors.IsNotFound(err) {
			return false, &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message:       "spec.infrastructure is pointing to a non-existent GatewayConfig",
			}
		}
		return false, fmt.Errorf("failed to get GatewayConfig %s: %w", configName, err)
	}

	if err := m.populateGatewaySecrets(ctx, receiver); err != nil {
		return false, err
	}

	// TODO: Make sure config is complete

	return true, nil
}

func (m *gatewayModelImpl) populateGatewaySecrets(ctx context.Context, receiver *resolvedGatewayDetails) error {
	receiver.gatewaySecrets = make(map[string]corev1.Secret)

	for _, listener := range receiver.gateway.Spec.Listeners {
		if listener.TLS == nil || len(listener.TLS.CertificateRefs) == 0 {
			continue
		}

		for _, certRef := range listener.TLS.CertificateRefs {
			secretName := string(certRef.Name)
			secretNamespace := receiver.gateway.Namespace
			if certRef.Namespace != nil {
				secretNamespace = string(*certRef.Namespace)
			}

			fullSecretName := secretNamespace + "/" + secretName

			if _, exists := receiver.gatewaySecrets[fullSecretName]; exists {
				continue
			}

			var secret corev1.Secret
			if err := m.client.Get(ctx, apitypes.NamespacedName{
				Name:      secretName,
				Namespace: secretNamespace,
			}, &secret); err != nil {
				if apierrors.IsNotFound(err) {
					return &resourceStatusError{
						conditionType: string(gatewayv1.GatewayConditionAccepted),
						reason:        string(gatewayv1.GatewayReasonInvalidParameters),
						message:       fmt.Sprintf("referenced secret %s not found", fullSecretName),
					}
				}
				return fmt.Errorf("failed to get secret %s: %w", fullSecretName, err)
			}

			receiver.gatewaySecrets[fullSecretName] = secret
		}
	}

	return nil
}

func (m *gatewayModelImpl) programGateway(ctx context.Context, data *resolvedGatewayDetails) error {
	loadBalancerID := data.config.Spec.LoadBalancerID
	m.logger.DebugContext(ctx, "Fetching OCI Load Balancer details",
		slog.String("loadBalancerId", loadBalancerID),
	)

	// TODO: We probably need to reset Programmed condition if we're here

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

	reconcileListenersCertificatesResult, err := m.ociLoadBalancerModel.reconcileListenersCertificates(ctx,
		reconcileListenersCertificatesParams{
			loadBalancerID:    loadBalancerID,
			gateway:           &data.gateway,
			knownCertificates: response.LoadBalancer.Certificates,
		})
	if err != nil {
		return fmt.Errorf("failed to reconcile listeners certificates: %w", err)
	}

	for _, listener := range data.gateway.Spec.Listeners {
		// TODO: Support listener with hostname

		listenerName := string(listener.Name)

		params := reconcileHTTPListenerParams{
			loadBalancerID:        loadBalancerID,
			knownListeners:        response.LoadBalancer.Listeners,
			knownRoutingPolicies:  response.LoadBalancer.RoutingPolicies,
			listenerCertificates:  reconcileListenersCertificatesResult.certificatesByListener[listenerName],
			defaultBackendSetName: *defaultBackendSet.Name,
			listenerSpec:          &listener,
		}

		if err = m.ociLoadBalancerModel.reconcileHTTPListener(ctx, params); err != nil {
			return fmt.Errorf("failed to reconcile listener %s: %w", listener.Name, err)
		}
	}

	if err = m.ociLoadBalancerModel.removeMissingListeners(ctx, removeMissingListenersParams{
		loadBalancerID:   loadBalancerID,
		knownListeners:   response.LoadBalancer.Listeners,
		gatewayListeners: data.gateway.Spec.Listeners,
	}); err != nil {
		return fmt.Errorf("failed to remove missing listeners: %w", err)
	}

	if err = m.ociLoadBalancerModel.removeUnusedCertificates(ctx, removeUnusedCertificatesParams{
		loadBalancerID:       loadBalancerID,
		listenerCertificates: reconcileListenersCertificatesResult.certificatesByListener,
		knownCertificates:    response.LoadBalancer.Certificates,
	}); err != nil {
		return fmt.Errorf("failed to remove unused certificates: %w", err)
	}

	return nil
}

func (m *gatewayModelImpl) isProgrammed(_ context.Context, data *resolvedGatewayDetails) bool {
	return m.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &data.gateway,
		conditions:    data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionProgrammed),
		annotations: map[string]string{
			GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
		},
	})
}

func (m *gatewayModelImpl) setProgrammed(ctx context.Context, data *resolvedGatewayDetails) error {
	annotations := map[string]string{
		GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
	}

	if len(data.gatewaySecrets) > 0 {
		for fullName, secret := range data.gatewaySecrets {
			annotationKey := GatewayUsedSecretsAnnotationPrefix + "/" + fullName
			annotations[annotationKey] = secret.ResourceVersion
		}
	}

	if err := m.resourcesModel.setCondition(ctx, setConditionParams{
		resource:      &data.gateway,
		conditions:    &data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionProgrammed),
		status:        metav1.ConditionTrue,
		reason:        string(gatewayv1.GatewayReasonProgrammed),
		message:       fmt.Sprintf("Gateway %s programmed by %s", data.gateway.Name, ControllerClassName),
		annotations:   annotations,
	}); err != nil {
		return fmt.Errorf("failed to set programmed condition for Gateway %s: %w", data.gateway.Name, err)
	}
	return nil
}

type gatewayModelDeps struct {
	dig.In

	ResourcesModel       resourcesModel
	K8sClient            k8sClient
	RootLogger           *slog.Logger
	OciClient            ociLoadBalancerClient
	OciLoadBalancerModel ociLoadBalancerModel
}

func newGatewayModel(deps gatewayModelDeps) gatewayModel {
	return &gatewayModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger.WithGroup("gateway-model"),
		ociClient:            deps.OciClient,
		ociLoadBalancerModel: deps.OciLoadBalancerModel,
		resourcesModel:       deps.ResourcesModel,
	}
}
