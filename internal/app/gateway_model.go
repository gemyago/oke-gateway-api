package app

import (
	"context"
	"errors"
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

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

	gatewayFrontendMTLSConfigMaps      map[string]corev1.ConfigMap
	gatewayFrontendMTLSReferenceGrants map[string]gatewayv1beta1.ReferenceGrant

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

	deprovisionGateway(ctx context.Context, data *resolvedGatewayDetails) error

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

	configResolved, configErr := m.resolveGatewayConfig(ctx, receiver)
	if configErr != nil || !configResolved {
		return false, configErr
	}

	if m.listenerSetEnabled {
		if err := populateAttachedListenerSets(ctx, m.client, receiver); err != nil {
			return false, err
		}
	}

	if receiver.gateway.DeletionTimestamp != nil &&
		controllerutil.ContainsFinalizer(&receiver.gateway, LoadBalancerGatewayProgrammedFinalizer) {
		return true, nil
	}

	if err := validateGatewayCertificateOptions(receiver.gateway); err != nil {
		return false, err
	}

	if err := m.populateGatewaySecrets(ctx, receiver); err != nil {
		return false, err
	}
	if err := m.populateGatewayFrontendMTLSDependencies(ctx, receiver); err != nil {
		return false, err
	}

	// TODO: Make sure config is complete

	return true, nil
}

func (m *gatewayModelImpl) resolveGatewayConfig(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
) (bool, error) {
	if receiver.gateway.Spec.Infrastructure == nil || receiver.gateway.Spec.Infrastructure.ParametersRef == nil {
		if loadBalancerGatewayCanFinalizeWithoutConfig(receiver.gateway) {
			receiver.config.Spec.LoadBalancerID = receiver.gateway.Annotations[LoadBalancerGatewayIDAnnotation]
			return true, nil
		}
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
			if loadBalancerGatewayCanFinalizeWithoutConfig(receiver.gateway) {
				receiver.config.Spec.LoadBalancerID = receiver.gateway.Annotations[LoadBalancerGatewayIDAnnotation]
				return true, nil
			}
			return false, &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message:       "spec.infrastructure is pointing to a non-existent GatewayConfig",
			}
		}
		return false, fmt.Errorf("failed to get GatewayConfig %s: %w", configName, err)
	}
	return true, nil
}

func loadBalancerGatewayCanFinalizeWithoutConfig(gateway gatewayv1.Gateway) bool {
	return gateway.DeletionTimestamp != nil &&
		controllerutil.ContainsFinalizer(&gateway, LoadBalancerGatewayProgrammedFinalizer) &&
		gateway.Annotations[LoadBalancerGatewayIDAnnotation] != ""
}

func (m *gatewayModelImpl) populateGatewayFrontendMTLSDependencies(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
) error {
	receiver.gatewayFrontendMTLSConfigMaps = make(map[string]corev1.ConfigMap)
	receiver.gatewayFrontendMTLSReferenceGrants = make(map[string]gatewayv1beta1.ReferenceGrant)
	refs := frontendMTLSConfigMapRefs(receiver.gateway)
	if len(refs) == 0 {
		return nil
	}

	listedGrantNamespaces := make(map[string]struct{})
	for _, fullName := range refs {
		if err := m.populateGatewayFrontendMTLSConfigMapDependency(ctx, receiver, fullName); err != nil {
			return err
		}
		if frontendMTLSReferenceGrantsAlreadyListed(receiver.gateway, fullName, listedGrantNamespaces) {
			continue
		}
		listedGrantNamespaces[fullName.Namespace] = struct{}{}
		if err := m.populateGatewayFrontendMTLSReferenceGrantDependencies(
			ctx,
			receiver,
			refs,
			fullName.Namespace,
		); err != nil {
			return err
		}
	}
	return nil
}

func (m *gatewayModelImpl) populateGatewayFrontendMTLSConfigMapDependency(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
	fullName apitypes.NamespacedName,
) error {
	var configMap corev1.ConfigMap
	if err := m.client.Get(ctx, fullName, &configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get frontend mTLS ConfigMap %s: %w", fullName.String(), err)
	}
	receiver.gatewayFrontendMTLSConfigMaps[fullName.String()] = configMap
	return nil
}

func frontendMTLSReferenceGrantsAlreadyListed(
	gateway gatewayv1.Gateway,
	fullName apitypes.NamespacedName,
	listedGrantNamespaces map[string]struct{},
) bool {
	if fullName.Namespace == gateway.Namespace {
		return true
	}
	_, listed := listedGrantNamespaces[fullName.Namespace]
	return listed
}

func (m *gatewayModelImpl) populateGatewayFrontendMTLSReferenceGrantDependencies(
	ctx context.Context,
	receiver *resolvedGatewayDetails,
	refs []apitypes.NamespacedName,
	namespace string,
) error {
	var grants gatewayv1beta1.ReferenceGrantList
	if err := m.client.List(ctx, &grants, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf(
			"failed to list frontend mTLS ReferenceGrants in namespace %s: %w",
			namespace,
			err,
		)
	}
	for _, grant := range grants.Items {
		m.addGatewayFrontendMTLSReferenceGrantDependency(receiver, refs, grant)
	}
	return nil
}

func (m *gatewayModelImpl) addGatewayFrontendMTLSReferenceGrantDependency(
	receiver *resolvedGatewayDetails,
	refs []apitypes.NamespacedName,
	grant gatewayv1beta1.ReferenceGrant,
) {
	if !referenceGrantHasMatchingFrom(grant, gatewayv1.Kind("Gateway"), receiver.gateway.Namespace) {
		return
	}
	for _, ref := range refs {
		if ref.Namespace != grant.Namespace {
			continue
		}
		if referenceGrantHasMatchingCoreTo(grant, "ConfigMap", ref.Name) {
			receiver.gatewayFrontendMTLSReferenceGrants[client.ObjectKeyFromObject(&grant).String()] = grant
			return
		}
	}
}

func frontendMTLSConfigMapRefs(gateway gatewayv1.Gateway) []apitypes.NamespacedName {
	if gateway.Spec.TLS == nil || gateway.Spec.TLS.Frontend == nil {
		return nil
	}
	refsByKey := make(map[apitypes.NamespacedName]struct{})
	addRefs := func(validation *gatewayv1.FrontendTLSValidation) {
		if validation == nil {
			return
		}
		for _, ref := range validation.CACertificateRefs {
			if ref.Group != "" || ref.Kind != "ConfigMap" {
				continue
			}
			refNamespace := gateway.Namespace
			if ref.Namespace != nil {
				refNamespace = string(*ref.Namespace)
			}
			refsByKey[apitypes.NamespacedName{
				Namespace: refNamespace,
				Name:      string(ref.Name),
			}] = struct{}{}
		}
	}
	addRefs(gateway.Spec.TLS.Frontend.Default.Validation)
	for _, portConfig := range gateway.Spec.TLS.Frontend.PerPort {
		addRefs(portConfig.TLS.Validation)
	}
	refs := lo.Keys(refsByKey)
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].String() < refs[j].String()
	})
	return refs
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

	for i := range receiver.effectiveListeners {
		listener := receiver.effectiveListeners[i]
		if listener.conflicted || listener.unsupported || listenerOCICertificateOCID(listener.listener) != "" {
			continue
		}
		if populateErr := m.populateGatewayListenerSecrets(
			ctx,
			receiver,
			gatewayv1.Kind(listener.sourceKind),
			listener.sourceNamespace,
			listener.listener,
		); populateErr != nil {
			if markListenerSetSecretError(&receiver.effectiveListeners[i], populateErr) {
				continue
			}
			return populateErr
		}
	}

	return nil
}

func markListenerSetSecretError(listener *effectiveListener, err error) bool {
	if listener.sourceKind != effectiveListenerSourceListenerSet {
		return false
	}
	listener.unsupported = true
	listener.unsupportedReason = gatewayv1.ListenerReasonInvalidCertificateRef
	listener.unsupportedMessage = err.Error()

	var statusErr *resourceStatusError
	if errors.As(err, &statusErr) &&
		statusErr.reason == string(gatewayv1.GatewayReasonInvalidParameters) &&
		strings.Contains(statusErr.message, "not permitted") {
		listener.unsupportedReason = gatewayv1.ListenerReasonRefNotPermitted
	}
	return true
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

func programmedGatewayListenersAnnotation(listeners []gatewayv1.Listener) string {
	listenerNames := make([]string, 0, len(listeners))
	for _, listener := range listeners {
		listenerNames = append(listenerNames, string(listener.Name))
	}
	sort.Strings(listenerNames)
	return strings.Join(listenerNames, ",")
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
	reconcileListenersCertificatesResult, err := m.reconcileGatewayListenerCertificates(
		ctx,
		data,
		gatewayListeners,
		response.LoadBalancer,
	)
	if err != nil {
		return err
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

	if err = m.removeMissingGatewayListeners(
		ctx,
		data,
		gatewayManagedListeners,
		response.LoadBalancer,
		gatewayListeners,
	); err != nil {
		return err
	}

	if err = m.cleanupFrontendMTLSCABundles(ctx, data, gatewayManagedListeners, response.LoadBalancer); err != nil {
		return err
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

func (m *gatewayModelImpl) deprovisionGateway(ctx context.Context, data *resolvedGatewayDetails) error {
	loadBalancerID := data.config.Spec.LoadBalancerID
	if loadBalancerID == "" {
		loadBalancerID = data.gateway.Annotations[LoadBalancerGatewayIDAnnotation]
	}
	if loadBalancerID != "" {
		if err := m.deprovisionGatewayLoadBalancerResources(ctx, data, loadBalancerID); err != nil {
			return err
		}
	}

	gatewayToUpdate := data.gateway.DeepCopy()
	controllerutil.RemoveFinalizer(gatewayToUpdate, LoadBalancerGatewayProgrammedFinalizer)
	annotations := gatewayToUpdate.GetAnnotations()
	delete(annotations, LoadBalancerGatewayIDAnnotation)
	delete(annotations, GatewayProgrammingRevisionAnnotation)
	delete(annotations, GatewayProgrammedCertificatesAnnotation)
	delete(annotations, LoadBalancerGatewayProgrammedListenersAnnotation)
	delete(annotations, GatewayFrontendMTLSCABundleCompartmentsAnnotation)
	for key := range annotations {
		if strings.HasPrefix(key, GatewayUsedSecretsAnnotationPrefix+"/") {
			delete(annotations, key)
		}
	}
	gatewayToUpdate.SetAnnotations(annotations)
	if updateErr := m.client.Update(ctx, gatewayToUpdate); updateErr != nil {
		if apierrors.IsNotFound(updateErr) {
			m.logger.InfoContext(ctx, "Gateway already deleted while removing finalizer",
				slog.String("gateway", client.ObjectKeyFromObject(gatewayToUpdate).String()),
			)
			return nil
		}
		return fmt.Errorf("failed to remove finalizer from Gateway %s/%s: %w",
			gatewayToUpdate.Namespace,
			gatewayToUpdate.Name,
			updateErr,
		)
	}
	return nil
}

func (m *gatewayModelImpl) deprovisionGatewayLoadBalancerResources(
	ctx context.Context,
	data *resolvedGatewayDetails,
	loadBalancerID string,
) error {
	response, err := m.ociClient.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: &loadBalancerID,
	})
	if err != nil {
		if serviceErr, ok := common.IsServiceError(err); ok &&
			serviceErr.GetHTTPStatusCode() == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("failed to get OCI Load Balancer %s for Gateway deprovision: %w", loadBalancerID, err)
	}

	data.loadBalancer = &response.LoadBalancer
	cleanupListenerNames := gatewayCleanupListenerNames(
		data.gateway,
		gatewayManagedOCIListenersForLoadBalancer(data),
	)
	if err = m.ociLoadBalancerModel.removeMissingListeners(ctx, removeMissingListenersParams{
		loadBalancerID:       loadBalancerID,
		knownListeners:       response.LoadBalancer.Listeners,
		knownRoutingPolicies: response.LoadBalancer.RoutingPolicies,
		cleanupListenerNames: cleanupListenerNames,
		gatewayListeners:     nil,
	}); err != nil {
		return fmt.Errorf("failed to remove Gateway listeners: %w", err)
	}

	if err = m.ociLoadBalancerModel.removeUnusedCertificates(ctx, removeUnusedCertificatesParams{
		loadBalancerID: loadBalancerID,
		previouslyProgrammedCertificates: parseProgrammedGatewayCertificatesAnnotation(
			data.gateway.Annotations[GatewayProgrammedCertificatesAnnotation],
		),
		desiredCertificates: loadBalancerListenerCertificatesOutsideCleanup(
			response.LoadBalancer,
			cleanupListenerNames,
		),
		knownCertificates: response.LoadBalancer.Certificates,
	}); err != nil {
		return fmt.Errorf("failed to remove Gateway certificates: %w", err)
	}

	if err = m.ociLoadBalancerModel.cleanupFrontendMTLSCABundles(ctx, cleanupFrontendMTLSCABundlesParams{
		gateway:            &data.gateway,
		compartmentID:      lo.FromPtr(response.LoadBalancer.CompartmentId),
		desiredBundleNames: map[string]struct{}{},
	}); err != nil {
		return fmt.Errorf("failed to clean up frontend mTLS CA bundles: %w", err)
	}

	if err = m.ociLoadBalancerModel.deprovisionBackendSetByName(
		ctx,
		loadBalancerID,
		gatewayDefaultBackendSetName(data.gateway),
	); err != nil {
		return fmt.Errorf("failed to remove Gateway default backend set: %w", err)
	}

	return nil
}

func gatewayCleanupListenerNames(gateway gatewayv1.Gateway, desiredListeners []gatewayv1.Listener) map[string]struct{} {
	return mergeNameSets(
		annotatedResourceNames(gateway, LoadBalancerGatewayProgrammedListenersAnnotation),
		listenerNamesSet(desiredListeners),
	)
}

func loadBalancerListenerCertificatesOutsideCleanup(
	loadBalancer loadbalancer.LoadBalancer,
	cleanupListenerNames map[string]struct{},
) []string {
	certNames := map[string]struct{}{}
	for listenerName, listener := range loadBalancer.Listeners {
		if _, cleanup := cleanupListenerNames[listenerName]; cleanup {
			continue
		}
		if listener.SslConfiguration == nil || listener.SslConfiguration.CertificateName == nil {
			continue
		}
		certNames[*listener.SslConfiguration.CertificateName] = struct{}{}
	}

	names := lo.Keys(certNames)
	sort.Strings(names)
	return names
}

func listenerNamesSet(listeners []gatewayv1.Listener) map[string]struct{} {
	listenerNames := make(map[string]struct{}, len(listeners))
	for _, listener := range listeners {
		listenerNames[string(listener.Name)] = struct{}{}
	}
	return listenerNames
}

func gatewayDefaultBackendSetName(gateway gatewayv1.Gateway) string {
	return gateway.Name + "-default"
}

func (m *gatewayModelImpl) reconcileGatewayListenerCertificates(
	ctx context.Context,
	data *resolvedGatewayDetails,
	gatewayListeners []gatewayv1.Listener,
	loadBalancer loadbalancer.LoadBalancer,
) (reconcileListenersCertificatesResult, error) {
	result, err := m.ociLoadBalancerModel.reconcileListenersCertificates(ctx, reconcileListenersCertificatesParams{
		loadBalancerID:    data.config.Spec.LoadBalancerID,
		gateway:           &data.gateway,
		gatewayListeners:  gatewayListeners,
		knownCertificates: loadBalancer.Certificates,
	})
	if err != nil {
		if isFrontendMTLSStatusError(err) {
			if removeErr := m.failClosedFrontendMTLSListeners(
				ctx,
				data,
				gatewayListeners,
				loadBalancer,
			); removeErr != nil {
				return reconcileListenersCertificatesResult{}, removeErr
			}
		}
		return reconcileListenersCertificatesResult{}, fmt.Errorf("failed to reconcile listeners certificates: %w", err)
	}
	return result, nil
}

func (m *gatewayModelImpl) removeMissingGatewayListeners(
	ctx context.Context,
	data *resolvedGatewayDetails,
	gatewayManagedListeners []gatewayv1.Listener,
	loadBalancer loadbalancer.LoadBalancer,
	gatewayListeners []gatewayv1.Listener,
) error {
	if err := m.ociLoadBalancerModel.removeMissingListeners(ctx, removeMissingListenersParams{
		loadBalancerID:       data.config.Spec.LoadBalancerID,
		knownListeners:       loadBalancer.Listeners,
		knownRoutingPolicies: loadBalancer.RoutingPolicies,
		cleanupListenerNames: gatewayCleanupListenerNames(data.gateway, gatewayManagedListeners),
		gatewayListeners:     gatewayListeners,
	}); err != nil {
		return fmt.Errorf("failed to remove missing listeners: %w", err)
	}
	return nil
}

func (m *gatewayModelImpl) failClosedFrontendMTLSListeners(
	ctx context.Context,
	data *resolvedGatewayDetails,
	gatewayListeners []gatewayv1.Listener,
	loadBalancer loadbalancer.LoadBalancer,
) error {
	if err := m.ociLoadBalancerModel.removeMissingListeners(ctx, removeMissingListenersParams{
		loadBalancerID:       data.config.Spec.LoadBalancerID,
		knownListeners:       loadBalancer.Listeners,
		knownRoutingPolicies: loadBalancer.RoutingPolicies,
		cleanupListenerNames: gatewayCleanupListenerNames(
			data.gateway,
			gatewayListenersWithoutFrontendMTLS(data.gateway, gatewayListeners),
		),
		gatewayListeners: gatewayListenersWithoutFrontendMTLS(data.gateway, gatewayListeners),
	}); err != nil {
		return fmt.Errorf("failed to fail closed frontend mTLS listeners: %w", err)
	}
	return nil
}

func gatewayListenersWithoutFrontendMTLS(
	gateway gatewayv1.Gateway,
	gatewayListeners []gatewayv1.Listener,
) []gatewayv1.Listener {
	keptListeners := []gatewayv1.Listener(nil)
	for _, listener := range gatewayListeners {
		if listenerUsesFrontendMTLS(gateway, listener) {
			continue
		}
		keptListeners = append(keptListeners, listener)
	}
	return keptListeners
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

func listenerUsesFrontendMTLS(gateway gatewayv1.Gateway, listener gatewayv1.Listener) bool {
	if listener.TLS == nil {
		return false
	}
	if validation := effectiveFrontendTLSValidation(gateway, listener.Port); validation != nil {
		return true
	}
	return len(frontendMTLSOCICABundleIDs(gateway, listener.Port)) > 0
}

func gatewayFrontendMTLSConfigured(gateway gatewayv1.Gateway) bool {
	if gateway.Spec.TLS != nil && gateway.Spec.TLS.Frontend != nil {
		frontend := gateway.Spec.TLS.Frontend
		if frontend.Default.Validation != nil {
			return true
		}
		for _, portConfig := range frontend.PerPort {
			if portConfig.TLS.Validation != nil {
				return true
			}
		}
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
	return m.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &data.gateway,
		conditions:    data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionProgrammed),
		annotations:   programmedGatewayAnnotations(data),
	})
}

func (m *gatewayModelImpl) setProgrammed(ctx context.Context, data *resolvedGatewayDetails) error {
	annotations := programmedGatewayAnnotations(data)

	data.gateway.Status.Addresses = gatewayStatusAddressesFromLoadBalancer(data.loadBalancer)
	data.gateway.Status.AttachedListenerSets = attachedListenerSetCount(data.listenerSets, data.effectiveListeners)
	if err := m.resourcesModel.setCondition(ctx, setConditionParams{
		resource:          &data.gateway,
		conditions:        &data.gateway.Status.Conditions,
		conditionType:     string(gatewayv1.GatewayConditionProgrammed),
		status:            metav1.ConditionTrue,
		reason:            string(gatewayv1.GatewayReasonProgrammed),
		message:           fmt.Sprintf("Gateway %s programmed by %s", data.gateway.Name, ControllerClassName),
		annotations:       annotations,
		removeAnnotations: staleProgrammedGatewayAnnotations(annotations),
		finalizer:         LoadBalancerGatewayProgrammedFinalizer,
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

func programmedGatewayAnnotations(data *resolvedGatewayDetails) map[string]string {
	annotations := map[string]string{
		GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
		GatewayProgrammedCertificatesAnnotation: programmedGatewayCertificatesAnnotation(
			programmedCertificateNamesFromSecrets(data.gatewaySecrets),
		),
		LoadBalancerGatewayProgrammedListenersAnnotation: programmedGatewayListenersAnnotation(
			gatewayManagedOCIListenersForLoadBalancer(data),
		),
	}
	if data.config.Spec.LoadBalancerID != "" {
		annotations[LoadBalancerGatewayIDAnnotation] = data.config.Spec.LoadBalancerID
	}

	if len(data.gatewaySecrets) > 0 {
		for _, secret := range data.gatewaySecrets {
			secretUID := string(secret.UID)
			annotationKey := GatewayUsedSecretsAnnotationPrefix + "/" + secretUID
			annotations[annotationKey] = secret.ResourceVersion
		}
	}

	if len(frontendMTLSConfigMapRefs(data.gateway)) > 0 {
		annotations[GatewayFrontendMTLSConfigMapsAnnotation] = configMapRevisionsAnnotation(
			data.gatewayFrontendMTLSConfigMaps,
		)
	}
	if len(data.gatewayFrontendMTLSReferenceGrants) > 0 ||
		gatewayHasCrossNamespaceFrontendMTLSConfigMapRefs(data.gateway) {
		annotations[GatewayFrontendMTLSReferenceGrantsAnnotation] = referenceGrantRevisionsAnnotation(
			data.gatewayFrontendMTLSReferenceGrants,
		)
	}
	return annotations
}

func staleProgrammedGatewayAnnotations(annotations map[string]string) []string {
	keys := []string{
		GatewayFrontendMTLSConfigMapsAnnotation,
		GatewayFrontendMTLSReferenceGrantsAnnotation,
	}
	stale := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, found := annotations[key]; !found {
			stale = append(stale, key)
		}
	}
	return stale
}

func gatewayHasCrossNamespaceFrontendMTLSConfigMapRefs(gateway gatewayv1.Gateway) bool {
	for _, ref := range frontendMTLSConfigMapRefs(gateway) {
		if ref.Namespace != gateway.Namespace {
			return true
		}
	}
	return false
}

func configMapRevisionsAnnotation(objects map[string]corev1.ConfigMap) string {
	keys := lo.Keys(objects)
	sort.Strings(keys)
	revisions := make([]string, 0, len(keys))
	for _, key := range keys {
		object := objects[key]
		revisions = append(revisions, fmt.Sprintf("%s=%s/%s", key, object.UID, object.ResourceVersion))
	}
	return strings.Join(revisions, ",")
}

func referenceGrantRevisionsAnnotation(objects map[string]gatewayv1beta1.ReferenceGrant) string {
	keys := lo.Keys(objects)
	sort.Strings(keys)
	revisions := make([]string, 0, len(keys))
	for _, key := range keys {
		object := objects[key]
		revisions = append(revisions, fmt.Sprintf("%s=%s/%s", key, object.UID, object.ResourceVersion))
	}
	return strings.Join(revisions, ",")
}

func setListenerSetsProgrammed(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	controllerName gatewayv1.GatewayController,
) error {
	for _, listenerSet := range data.listenerSets {
		attachedRoutes, err := listenerSetAttachedRouteCounts(ctx, k8sClient, data, listenerSet)
		if err != nil {
			return err
		}
		desiredStatus := listenerSetStatusForGateway(
			data.gateway,
			listenerSet,
			data.effectiveListeners,
			controllerName,
			attachedRoutes,
		)
		if listenerSetStatusSemanticallyEqual(listenerSet.Status, desiredStatus) {
			continue
		}
		listenerSetToUpdate := listenerSet.DeepCopy()
		listenerSetToUpdate.Status = desiredStatus
		if updateErr := k8sClient.Status().Update(ctx, listenerSetToUpdate); updateErr != nil {
			return fmt.Errorf("failed to update ListenerSet %s/%s status: %w",
				listenerSet.Namespace,
				listenerSet.Name,
				updateErr,
			)
		}
	}
	return nil
}

func listenerSetAttachedRouteCounts(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
) (map[gatewayv1.SectionName]int32, error) {
	counts := map[gatewayv1.SectionName]int32{}
	if err := addListenerSetHTTPRouteCounts(ctx, k8sClient, data, listenerSet, counts); err != nil {
		return nil, err
	}
	if err := addListenerSetGRPCRouteCounts(ctx, k8sClient, data, listenerSet, counts); err != nil {
		return nil, err
	}
	if err := addListenerSetTCPRouteCounts(ctx, k8sClient, data, listenerSet, counts); err != nil {
		return nil, err
	}
	if err := addListenerSetUDPRouteCounts(ctx, k8sClient, data, listenerSet, counts); err != nil {
		return nil, err
	}
	if err := addListenerSetTLSRouteCounts(ctx, k8sClient, data, listenerSet, counts); err != nil {
		return nil, err
	}
	return counts, nil
}

func addListenerSetHTTPRouteCounts(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
) error {
	var routeList gatewayv1.HTTPRouteList
	if err := k8sClient.List(ctx, &routeList); err != nil {
		return fmt.Errorf("failed to list HTTPRoutes for ListenerSet attached route counts: %w", err)
	}
	for _, route := range routeList.Items {
		addListenerSetRouteCounts(
			data,
			listenerSet,
			counts,
			route.Namespace,
			route.Status.Parents,
			route.Spec.ParentRefs,
			func(ref gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
				if ref.SectionName != nil && listener.Name != *ref.SectionName {
					return false
				}
				return listener.Protocol == gatewayv1.HTTPProtocolType ||
					listener.Protocol == gatewayv1.HTTPSProtocolType
			},
		)
	}
	return nil
}

func addListenerSetGRPCRouteCounts(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
) error {
	var routeList gatewayv1.GRPCRouteList
	if err := k8sClient.List(ctx, &routeList); err != nil {
		return fmt.Errorf("failed to list GRPCRoutes for ListenerSet attached route counts: %w", err)
	}
	for _, route := range routeList.Items {
		addListenerSetRouteCounts(
			data,
			listenerSet,
			counts,
			route.Namespace,
			route.Status.Parents,
			route.Spec.ParentRefs,
			func(ref gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
				if ref.SectionName != nil && listener.Name != *ref.SectionName {
					return false
				}
				return grpcRouteListenerProtocolSupported(listener.Protocol)
			},
		)
	}
	return nil
}

func addListenerSetTCPRouteCounts(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
) error {
	var routeList gatewayv1.TCPRouteList
	if err := k8sClient.List(ctx, &routeList); err != nil {
		return fmt.Errorf("failed to list TCPRoutes for ListenerSet attached route counts: %w", err)
	}
	for _, route := range routeList.Items {
		addListenerSetRouteCounts(data, listenerSet, counts, route.Namespace,
			route.Status.Parents, route.Spec.ParentRefs, tcpRouteMatchesListener)
	}
	return nil
}

func addListenerSetUDPRouteCounts(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
) error {
	var routeList gatewayv1.UDPRouteList
	if err := k8sClient.List(ctx, &routeList); err != nil {
		return fmt.Errorf("failed to list UDPRoutes for ListenerSet attached route counts: %w", err)
	}
	for _, route := range routeList.Items {
		addListenerSetRouteCounts(data, listenerSet, counts, route.Namespace,
			route.Status.Parents, route.Spec.ParentRefs, udpRouteMatchesListener)
	}
	return nil
}

func addListenerSetTLSRouteCounts(
	ctx context.Context,
	k8sClient k8sClient,
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
) error {
	var routeList gatewayv1.TLSRouteList
	if err := k8sClient.List(ctx, &routeList); err != nil {
		return fmt.Errorf("failed to list TLSRoutes for ListenerSet attached route counts: %w", err)
	}
	for _, route := range routeList.Items {
		addListenerSetRouteCounts(data, listenerSet, counts, route.Namespace,
			route.Status.Parents, route.Spec.ParentRefs, tlsRouteMatchesListener)
	}
	return nil
}

func addListenerSetRouteCounts(
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
	routeNamespace string,
	parentStatuses []gatewayv1.RouteParentStatus,
	parentRefs []gatewayv1.ParentReference,
	matchesListener func(gatewayv1.ParentReference, gatewayv1.Listener) bool,
) {
	for _, parentStatus := range parentStatuses {
		if !listenerSetRouteParentStatusAccepted(data, listenerSet, routeNamespace, parentStatus) {
			continue
		}
		for _, parentRef := range parentRefs {
			addListenerSetRouteCountForParentRef(
				data,
				listenerSet,
				counts,
				routeNamespace,
				parentStatus.ParentRef,
				parentRef,
				matchesListener,
			)
		}
	}
}

func listenerSetRouteParentStatusAccepted(
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	routeNamespace string,
	parentStatus gatewayv1.RouteParentStatus,
) bool {
	return parentStatus.ControllerName == data.gatewayClass.Spec.ControllerName &&
		meta.IsStatusConditionTrue(parentStatus.Conditions, string(gatewayv1.RouteConditionAccepted)) &&
		parentRefTargetsListenerSet(parentStatus.ParentRef) &&
		parentRefTargetName(parentStatus.ParentRef, routeNamespace) == client.ObjectKeyFromObject(&listenerSet)
}

func addListenerSetRouteCountForParentRef(
	data *resolvedGatewayDetails,
	listenerSet gatewayv1.ListenerSet,
	counts map[gatewayv1.SectionName]int32,
	routeNamespace string,
	statusParentRef gatewayv1.ParentReference,
	parentRef gatewayv1.ParentReference,
	matchesListener func(gatewayv1.ParentReference, gatewayv1.Listener) bool,
) {
	if !parentRefSameTarget(parentRef, statusParentRef) {
		return
	}
	for _, listener := range effectiveListenersForParentRef(*data, parentRef, routeNamespace, matchesListener) {
		if entry, found := listenerSetEntryForEffectiveListener(data.gateway, listenerSet, listener.Name); found {
			counts[entry.Name]++
		}
	}
}

func listenerSetEntryForEffectiveListener(
	gateway gatewayv1.Gateway,
	listenerSet gatewayv1.ListenerSet,
	effectiveName gatewayv1.SectionName,
) (gatewayv1.ListenerEntry, bool) {
	for _, entry := range listenerSet.Spec.Listeners {
		listener := listenerFromListenerSetEntry(entry)
		ociListener := effectiveListenerOCIListener(effectiveListener{
			listener:        listener,
			sourceKind:      effectiveListenerSourceListenerSet,
			sourceNamespace: listenerSet.Namespace,
			sourceName:      listenerSet.Name,
			ociName:         listenerSetOCIListenerName(gateway, listenerSet, listener),
		})
		if ociListener.Name == effectiveName {
			return entry, true
		}
	}
	return gatewayv1.ListenerEntry{}, false
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
