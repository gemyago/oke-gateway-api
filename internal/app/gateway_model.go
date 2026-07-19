package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

func listenerOCICertificateOCID(listener gatewayv1.Listener) string {
	if listener.TLS == nil || listener.TLS.Options == nil {
		return ""
	}
	return string(listener.TLS.Options[gatewayv1.AnnotationKey(ListenerTLSOptionOCICertificateOCID)])
}

func gatewayCertificateIDsByListener(gateway gatewayv1.Gateway) map[string]string {
	return certificateIDsByListener(gateway.Spec.Listeners)
}

func certificateIDsByListener(listeners []gatewayv1.Listener) map[string]string {
	result := make(map[string]string)
	for _, listener := range listeners {
		if certificateID := listenerOCICertificateOCID(listener); certificateID != "" {
			result[string(listener.Name)] = certificateID
		}
	}
	return result
}

func validateGatewayCertificateOptions(gateway gatewayv1.Gateway) error {
	for _, listener := range gateway.Spec.Listeners {
		certificateID := listenerOCICertificateOCID(listener)
		if certificateID == "" {
			continue
		}
		if listener.Protocol != gatewayv1.HTTPSProtocolType && listener.Protocol != gatewayv1.TLSProtocolType {
			return &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message: fmt.Sprintf(
					"listener %s option %s can only be used with HTTPS or TLS listeners",
					listener.Name,
					ListenerTLSOptionOCICertificateOCID,
				),
			}
		}
		if listener.TLS.Mode != nil && *listener.TLS.Mode != gatewayv1.TLSModeTerminate {
			return &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message: fmt.Sprintf(
					"listener %s option %s can only be used with Terminate TLS mode",
					listener.Name,
					ListenerTLSOptionOCICertificateOCID,
				),
			}
		}
		if len(listener.TLS.CertificateRefs) > 0 {
			return &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message: fmt.Sprintf(
					"listener %s option %s cannot be used together with listener.tls.certificateRefs",
					listener.Name,
					ListenerTLSOptionOCICertificateOCID,
				),
			}
		}
	}
	return nil
}

type resolvedGatewayDetails struct {
	gateway      gatewayv1.Gateway
	gatewayClass gatewayv1.GatewayClass
	listenerSets []gatewayv1.ListenerSet

	// Map of secret full name to the secret object
	// holds all secrets that are used by the gateway (mostly listeners certificates)
	gatewaySecrets map[string]corev1.Secret

	config types.GatewayConfig

	loadBalancer *loadbalancer.LoadBalancer

	effectiveListeners []effectiveListener
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
	listenerSetEnabled   bool
}

func (m *gatewayModelImpl) setListenerSetEnabled(enabled bool) {
	m.listenerSetEnabled = enabled
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

	if err := validateGatewayCertificateOptions(receiver.gateway); err != nil {
		return false, err
	}

	if m.listenerSetEnabled {
		if err := populateAttachedListenerSets(ctx, m.client, receiver); err != nil {
			return false, err
		}
	}
	if err := m.populateGatewaySecrets(ctx, receiver); err != nil {
		return false, err
	}

	// TODO: Make sure config is complete

	return true, nil
}

func (m *gatewayModelImpl) populateGatewaySecrets(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
) error {
	receiver.gatewaySecrets = make(map[string]corev1.Secret)
	if len(receiver.effectiveListeners) == 0 {
		for _, listener := range receiver.gateway.Spec.Listeners {
			if listenerOCICertificateOCID(listener) != "" {
				continue
			}
			if populateErr := m.populateGatewayListenerSecrets(
				ctx,
				receiver,
				gatewayv1.Kind("Gateway"),
				receiver.gateway.Namespace,
				listener,
			); populateErr != nil {
				return populateErr
			}
		}
		return nil
	}

	for _, listener := range receiver.effectiveListeners {
		if listener.conflicted || listenerOCICertificateOCID(listener.listener) != "" {
			continue
		}
		if populateErr := m.populateGatewayListenerSecrets(
			ctx,
			receiver,
			gatewayv1.Kind(listener.sourceKind),
			listener.sourceNamespace,
			listener.listener,
		); populateErr != nil {
			return populateErr
		}
	}

	return nil
}

func populateAttachedListenerSets(ctx context.Context, k8sClient k8sClient, receiver *resolvedGatewayDetails) error {
	var listenerSetList gatewayv1.ListenerSetList
	gatewayKey := client.ObjectKeyFromObject(&receiver.gateway)
	if err := k8sClient.List(ctx, &listenerSetList, client.MatchingFields{
		listenerSetParentGatewayIndexKey: gatewayKey.String(),
	}); err != nil {
		return fmt.Errorf("failed to list ListenerSets for Gateway %s: %w", gatewayKey.String(), err)
	}

	attached := make([]gatewayv1.ListenerSet, 0, len(listenerSetList.Items))
	if err := filterAttachedListenerSets(ctx, k8sClient, receiver, listenerSetList.Items, &attached); err != nil {
		return err
	}

	receiver.listenerSets = attached
	receiver.effectiveListeners = effectiveListenersForGateway(receiver.gateway, attached)
	markUnsupportedListenerSetListeners(receiver.effectiveListeners, receiver.gatewayClass.Spec.ControllerName)
	return nil
}

func populateAttachedListenerSetsUnindexed(
	ctx context.Context,
	k8sClient k8sClient,
	receiver *resolvedGatewayDetails,
) error {
	var listenerSetList gatewayv1.ListenerSetList
	if err := k8sClient.List(ctx, &listenerSetList); err != nil {
		return fmt.Errorf("failed to list ListenerSets for Gateway %s/%s: %w",
			receiver.gateway.Namespace,
			receiver.gateway.Name,
			err,
		)
	}

	attached := make([]gatewayv1.ListenerSet, 0, len(listenerSetList.Items))
	if err := filterAttachedListenerSets(ctx, k8sClient, receiver, listenerSetList.Items, &attached); err != nil {
		return err
	}

	receiver.listenerSets = attached
	receiver.effectiveListeners = effectiveListenersForGateway(receiver.gateway, attached)
	markUnsupportedListenerSetListeners(receiver.effectiveListeners, receiver.gatewayClass.Spec.ControllerName)
	return nil
}

func filterAttachedListenerSets(
	ctx context.Context,
	k8sClient k8sClient,
	receiver *resolvedGatewayDetails,
	listenerSets []gatewayv1.ListenerSet,
	attached *[]gatewayv1.ListenerSet,
) error {
	gatewayKey := client.ObjectKeyFromObject(&receiver.gateway).String()
	for _, listenerSet := range listenerSets {
		parentGatewayName, ok := listenerSetParentGatewayName(listenerSet)
		if !ok || parentGatewayName != gatewayKey {
			continue
		}
		var namespace corev1.Namespace
		if err := k8sClient.Get(ctx, apitypes.NamespacedName{Name: listenerSet.Namespace}, &namespace); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to get ListenerSet namespace %s: %w", listenerSet.Namespace, err)
		}
		if listenerSetAllowedByGateway(receiver.gateway, listenerSet, namespace) {
			*attached = append(*attached, listenerSet)
		}
	}
	return nil
}

func (m *gatewayModelImpl) populateGatewayListenerSecrets(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
	sourceKind gatewayv1.Kind,
	defaultNamespace string,
	listener gatewayv1.Listener,
) error {
	if listener.TLS == nil || len(listener.TLS.CertificateRefs) == 0 {
		return nil
	}

	for _, certRef := range listener.TLS.CertificateRefs {
		secretName := string(certRef.Name)
		secretNamespace := defaultNamespace
		if certRef.Namespace != nil {
			secretNamespace = string(*certRef.Namespace)
		}
		fullSecretName := apitypes.NamespacedName{Namespace: secretNamespace, Name: secretName}
		if sourceKind == gatewayv1.Kind(effectiveListenerSourceListenerSet) {
			allowed, err := referenceGrantAllowsSecretRef(ctx, m.client, sourceKind, defaultNamespace, fullSecretName)
			if err != nil {
				return err
			}
			if !allowed {
				return &resourceStatusError{
					conditionType: string(gatewayv1.GatewayConditionAccepted),
					reason:        string(gatewayv1.GatewayReasonInvalidParameters),
					message: fmt.Sprintf(
						"certificateRef %s is not permitted by a ReferenceGrant",
						fullSecretName.String(),
					),
				}
			}
		}

		if err := m.populateGatewaySecret(ctx, receiver, secretNamespace, secretName); err != nil {
			return err
		}
	}

	return nil
}

func (m *gatewayModelImpl) populateGatewaySecret(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
	secretNamespace string,
	secretName string,
) error {
	fullSecretName := secretNamespace + "/" + secretName
	if _, exists := receiver.gatewaySecrets[fullSecretName]; exists {
		return nil
	}

	var secret corev1.Secret
	getErr := m.client.Get(ctx, apitypes.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}, &secret)
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message:       fmt.Sprintf("referenced secret %s not found", fullSecretName),
			}
		}
		return fmt.Errorf("failed to get secret %s: %w", fullSecretName, getErr)
	}

	receiver.gatewaySecrets[fullSecretName] = secret
	return nil
}

func programmedCertificateNamesFromSecrets(gatewaySecrets map[string]corev1.Secret) []string {
	names := make([]string, 0, len(gatewaySecrets))
	for _, secret := range gatewaySecrets {
		names = append(names, ociCertificateNameFromSecret(secret))
	}
	return normalizeProgrammedCertificateNames(names)
}

func programmedGatewayCertificatesAnnotation(certNames []string) string {
	return strings.Join(normalizeProgrammedCertificateNames(certNames), ",")
}

func parseProgrammedGatewayCertificatesAnnotation(annotationValue string) []string {
	if annotationValue == "" {
		return nil
	}

	certNames := strings.Split(annotationValue, ",")
	for idx := range certNames {
		certNames[idx] = strings.TrimSpace(certNames[idx])
	}
	return normalizeProgrammedCertificateNames(certNames)
}

func normalizeProgrammedCertificateNames(certNames []string) []string {
	normalized := make([]string, 0, len(certNames))
	for _, certName := range certNames {
		if certName == "" {
			continue
		}
		normalized = append(normalized, certName)
	}

	sort.Strings(normalized)
	return slices.Compact(normalized)
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
		if serviceErr, ok := common.IsServiceError(err); ok &&
			serviceErr.GetHTTPStatusCode() == http.StatusNotFound {
			return &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionProgrammed),
				reason:        string(gatewayv1.GatewayReasonPending),
				message:       fmt.Sprintf("referenced OCI Load Balancer %s not found", loadBalancerID),
			}
		}
		return fmt.Errorf("failed to get OCI Load Balancer %s: %w", loadBalancerID, err)
	}
	data.loadBalancer = &response.LoadBalancer

	// This is very verbose, uncomment if needed
	// m.logger.DebugContext(ctx, "Successfully retrieved OCI Load Balancer details",
	// 	slog.Any("loadBalancer", response.LoadBalancer),
	// )

	defaultBackendSet, err := m.ociLoadBalancerModel.reconcileDefaultBackendSet(ctx, reconcileDefaultBackendParams{
		loadBalancerID:   loadBalancerID,
		knownBackendSets: response.LoadBalancer.BackendSets,
		gateway:          &data.gateway,
	})
	if err != nil {
		return fmt.Errorf("failed to program default backend set: %w", err)
	}

	gatewayListeners := effectiveOCIListenersForGateway(data)
	gatewayManagedListeners := gatewayManagedOCIListenersForLoadBalancer(data)
	reconcileListenersCertificatesResult, err := m.ociLoadBalancerModel.reconcileListenersCertificates(ctx,
		reconcileListenersCertificatesParams{
			loadBalancerID:    loadBalancerID,
			gateway:           &data.gateway,
			gatewayListeners:  gatewayListeners,
			knownCertificates: response.LoadBalancer.Certificates,
		})
	if err != nil {
		return fmt.Errorf("failed to reconcile listeners certificates: %w", err)
	}

	for _, listener := range gatewayManagedListeners {
		listenerName := string(listener.Name)

		params := reconcileHTTPListenerParams{
			loadBalancerID:            loadBalancerID,
			loadBalancerCompartmentID: lo.FromPtr(response.LoadBalancer.CompartmentId),
			knownListeners:            response.LoadBalancer.Listeners,
			knownRoutingPolicies:      response.LoadBalancer.RoutingPolicies,
			listenerCertificates:      reconcileListenersCertificatesResult.certificatesByListener[listenerName],
			listenerCertificateID:     reconcileListenersCertificatesResult.certificateIDsByListener[listenerName],
			defaultBackendSetName:     *defaultBackendSet.Name,
			listenerSpec:              &listener,
		}
		if gatewayFrontendMTLSConfigured(data.gateway) {
			params.gateway = &data.gateway
		}

		if err = m.ociLoadBalancerModel.reconcileHTTPListener(ctx, params); err != nil {
			return fmt.Errorf("failed to reconcile listener %s: %w", listener.Name, err)
		}
	}

	if err = m.cleanupFrontendMTLSCABundles(ctx, data, gatewayManagedListeners, response.LoadBalancer); err != nil {
		return err
	}

	if err = m.ociLoadBalancerModel.removeMissingListeners(ctx, removeMissingListenersParams{
		loadBalancerID:   loadBalancerID,
		knownListeners:   response.LoadBalancer.Listeners,
		gatewayListeners: gatewayListeners,
	}); err != nil {
		return fmt.Errorf("failed to remove missing listeners: %w", err)
	}

	if err = m.ociLoadBalancerModel.removeUnusedCertificates(ctx, removeUnusedCertificatesParams{
		loadBalancerID: loadBalancerID,
		previouslyProgrammedCertificates: parseProgrammedGatewayCertificatesAnnotation(
			data.gateway.Annotations[GatewayProgrammedCertificatesAnnotation],
		),
		desiredCertificates: certificateNamesFromListenerCertificates(
			reconcileListenersCertificatesResult.certificatesByListener,
		),
		knownCertificates: response.LoadBalancer.Certificates,
	}); err != nil {
		return fmt.Errorf("failed to remove unused certificates: %w", err)
	}

	return nil
}

func (m *gatewayModelImpl) cleanupFrontendMTLSCABundles(
	ctx context.Context,
	data *resolvedGatewayDetails,
	gatewayManagedListeners []gatewayv1.Listener,
	loadBalancer loadbalancer.LoadBalancer,
) error {
	desiredBundleNames := desiredFrontendMTLSCABundleNames(data.gateway, gatewayManagedListeners)
	if len(desiredBundleNames) == 0 &&
		data.gateway.Annotations[GatewayFrontendMTLSCABundleCompartmentsAnnotation] == "" {
		return nil
	}
	if err := m.ociLoadBalancerModel.cleanupFrontendMTLSCABundles(ctx, cleanupFrontendMTLSCABundlesParams{
		gateway:            &data.gateway,
		compartmentID:      lo.FromPtr(loadBalancer.CompartmentId),
		desiredBundleNames: desiredBundleNames,
	}); err != nil {
		return fmt.Errorf("failed to clean up frontend mTLS CA bundles: %w", err)
	}
	return nil
}

func desiredFrontendMTLSCABundleNames(
	gateway gatewayv1.Gateway,
	gatewayManagedListeners []gatewayv1.Listener,
) map[string]struct{} {
	desiredBundleNames := make(map[string]struct{})
	for _, listener := range gatewayManagedListeners {
		if !gatewayFrontendMTLSConfigured(gateway) || listener.TLS == nil {
			continue
		}
		if listenerOCICertificateOCID(listener) == "" {
			continue
		}
		validation := effectiveFrontendTLSValidation(gateway, listener.Port)
		if validation == nil || len(validation.CACertificateRefs) == 0 {
			continue
		}
		for _, ref := range validation.CACertificateRefs {
			desiredBundleNames[frontendMTLSCABundleName(gateway, listener.Port, ref)] = struct{}{}
		}
	}
	return desiredBundleNames
}

func gatewayFrontendMTLSConfigured(gateway gatewayv1.Gateway) bool {
	if gateway.Spec.TLS != nil && gateway.Spec.TLS.Frontend != nil {
		return true
	}
	if gateway.Annotations == nil {
		return false
	}
	for key, value := range gateway.Annotations {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if key == FrontendMTLSTrustedCABundleOCIDsAnnotation ||
			key == FrontendMTLSVerifyDepthAnnotation ||
			strings.HasPrefix(key, "oci.oraclecloud.com/frontend-mtls-") {
			return true
		}
	}
	return false
}

func gatewayStatusAddressesFromLoadBalancer(lb *loadbalancer.LoadBalancer) []gatewayv1.GatewayStatusAddress {
	if lb == nil || len(lb.IpAddresses) == 0 {
		return nil
	}

	values := make([]string, 0, len(lb.IpAddresses))
	for _, ipAddress := range lb.IpAddresses {
		if ipAddress.IpAddress == nil || *ipAddress.IpAddress == "" {
			continue
		}
		values = append(values, *ipAddress.IpAddress)
	}
	return gatewayStatusAddressesFromValues(values)
}

func (m *gatewayModelImpl) isProgrammed(_ context.Context, data *resolvedGatewayDetails) bool {
	annotations := map[string]string{
		GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
		GatewayProgrammedCertificatesAnnotation: programmedGatewayCertificatesAnnotation(
			programmedCertificateNamesFromSecrets(data.gatewaySecrets),
		),
	}

	// Include secrets annotations in the check
	if len(data.gatewaySecrets) > 0 {
		for _, secret := range data.gatewaySecrets {
			secretUID := string(secret.UID)
			annotationKey := GatewayUsedSecretsAnnotationPrefix + "/" + secretUID
			annotations[annotationKey] = secret.ResourceVersion
		}
	}

	return m.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &data.gateway,
		conditions:    data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionProgrammed),
		annotations:   annotations,
	})
}

func (m *gatewayModelImpl) setProgrammed(ctx context.Context, data *resolvedGatewayDetails) error {
	annotations := map[string]string{
		GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
		GatewayProgrammedCertificatesAnnotation: programmedGatewayCertificatesAnnotation(
			programmedCertificateNamesFromSecrets(data.gatewaySecrets),
		),
	}

	if len(data.gatewaySecrets) > 0 {
		for _, secret := range data.gatewaySecrets {
			secretUID := string(secret.UID)
			annotationKey := GatewayUsedSecretsAnnotationPrefix + "/" + secretUID
			annotations[annotationKey] = secret.ResourceVersion
		}
	}

	data.gateway.Status.Addresses = gatewayStatusAddressesFromLoadBalancer(data.loadBalancer)
	data.gateway.Status.AttachedListenerSets = attachedListenerSetCount(data.listenerSets)
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
	if err := setListenerSetsProgrammed(
		ctx,
		m.client,
		data,
		gatewayv1.GatewayController(ControllerClassName),
	); err != nil {
		return err
	}
	return nil
}

func setListenerSetsProgrammed(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	controllerName gatewayv1.GatewayController,
) error {
	for _, listenerSet := range data.listenerSets {
		desiredStatus := listenerSetStatusForGateway(data.gateway, listenerSet, data.effectiveListeners, controllerName)
		if listenerSetStatusSemanticallyEqual(listenerSet.Status, desiredStatus) {
			continue
		}
		listenerSetToUpdate := listenerSet.DeepCopy()
		listenerSetToUpdate.Status = desiredStatus
		if err := k8sClient.Status().Update(ctx, listenerSetToUpdate); err != nil {
			return fmt.Errorf("failed to update ListenerSet %s/%s status: %w",
				listenerSet.Namespace,
				listenerSet.Name,
				err,
			)
		}
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

func newGatewayModel(deps gatewayModelDeps) *gatewayModelImpl {
	return &gatewayModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger.WithGroup("gateway-model"),
		ociClient:            deps.OciClient,
		ociLoadBalancerModel: deps.OciLoadBalancerModel,
		resourcesModel:       deps.ResourcesModel,
	}
}
