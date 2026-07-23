package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apitypes "k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	frontendMTLSCABundleNamePrefix = "oke-fmtls-"
	frontendMTLSManagedByTag       = "oke-gateway-api-managed-by"
	frontendMTLSManagedByValue     = "frontend-mtls"
	frontendMTLSGatewayTag         = "oke-gateway-api-gateway"
	frontendMTLSPortTag            = "oke-gateway-api-port"
	frontendMTLSCAHashTag          = "oke-gateway-api-ca-sha256"
	defaultFrontendMTLSVerifyDepth = 3
)

type cleanupFrontendMTLSCABundlesParams struct {
	gateway            *gatewayv1.Gateway
	compartmentID      string
	desiredBundleNames map[string]struct{}
}

type frontendMTLSResolvedCARef struct {
	ref   gatewayv1.ObjectReference
	caPEM string
}

type frontendMTLSSettings struct {
	caRefs      []gatewayv1.ObjectReference
	ociCAIDs    []string
	verifyDepth int
}

func frontendMTLSPortTrustedCABundleOCIDsAnnotation(port gatewayv1.PortNumber) string {
	return fmt.Sprintf("oci.oraclecloud.com/frontend-mtls-%d-trusted-ca-bundle-ocids", port)
}

func frontendMTLSPortVerifyDepthAnnotation(port gatewayv1.PortNumber) string {
	return fmt.Sprintf("oci.oraclecloud.com/frontend-mtls-%d-verify-depth", port)
}

func (m *ociLoadBalancerModelImpl) applyFrontendMTLS(
	ctx context.Context,
	params reconcileHTTPListenerParams,
	sslConfig *loadbalancer.SslConfigurationDetails,
) (*loadbalancer.SslConfigurationDetails, error) {
	if params.listenerSpec == nil || params.listenerSpec.TLS == nil {
		return sslConfig, nil
	}
	if params.gateway == nil {
		return sslConfig, nil
	}
	if params.listenerSpec.Protocol != gatewayv1.HTTPSProtocolType &&
		params.listenerSpec.Protocol != gatewayv1.TLSProtocolType {
		return nil, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
			"frontend mTLS can only be used with HTTPS or TLS listeners",
		)
	}

	optionCAIDs := frontendMTLSOCICABundleIDs(*params.gateway, params.listenerSpec.Port)
	validation := effectiveFrontendTLSValidation(*params.gateway, params.listenerSpec.Port)
	if validation == nil && len(optionCAIDs) == 0 {
		return sslConfig, nil
	}
	settings, err := resolveFrontendMTLSSettings(*params.gateway, params.listenerSpec.Port, validation, optionCAIDs)
	if err != nil {
		return nil, err
	}

	if sslConfig == nil {
		sslConfig = &loadbalancer.SslConfigurationDetails{}
	}
	verifyPeer := true
	sslConfig.VerifyPeerCertificate = &verifyPeer
	sslConfig.VerifyDepth = &settings.verifyDepth
	if sslConfig.CertificateName != nil {
		if len(settings.ociCAIDs) > 0 {
			return nil, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
				fmt.Sprintf(
					"frontend mTLS OCI CA bundle OCID annotations require listener %s to use %s",
					params.listenerSpec.Name,
					ListenerTLSOptionOCICertificateOCID,
				),
			)
		}
		for _, ref := range settings.caRefs {
			if _, err = m.resolveFrontendMTLSCARef(ctx, *params.gateway, ref); err != nil {
				return nil, err
			}
		}
		sslConfig.TrustedCertificateAuthorityIds = nil
		return sslConfig, nil
	}

	if m.certsClient == nil {
		return nil, errors.New("OCI certificates management client is required for frontend mTLS")
	}
	trustedCAIDs, err := m.resolveFrontendMTLSTrustedCAIDs(ctx, params, settings)
	if err != nil {
		return nil, err
	}

	trustedCAIDs = lo.Uniq(trustedCAIDs)
	sort.Strings(trustedCAIDs)
	sslConfig.TrustedCertificateAuthorityIds = trustedCAIDs
	return sslConfig, nil
}

func resolveFrontendMTLSSettings(
	gateway gatewayv1.Gateway,
	port gatewayv1.PortNumber,
	validation *gatewayv1.FrontendTLSValidation,
	optionCAIDs []string,
) (frontendMTLSSettings, error) {
	mode := gatewayv1.AllowValidOnly
	if validation != nil && validation.Mode != "" {
		mode = validation.Mode
	}
	if mode == gatewayv1.AllowInsecureFallback {
		return frontendMTLSSettings{}, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
			"frontend mTLS mode AllowInsecureFallback is not supported by OCI Load Balancer",
		)
	}
	if mode != gatewayv1.AllowValidOnly {
		return frontendMTLSSettings{}, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
			fmt.Sprintf("frontend mTLS mode %q is not supported", mode),
		)
	}

	caRefs := []gatewayv1.ObjectReference(nil)
	if validation != nil {
		caRefs = validation.CACertificateRefs
	}
	if len(caRefs) > 0 && len(optionCAIDs) > 0 {
		return frontendMTLSSettings{}, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
			"frontend mTLS cannot mix caCertificateRefs with OCI CA bundle OCID annotations",
		)
	}
	if len(caRefs) == 0 && len(optionCAIDs) == 0 {
		return frontendMTLSSettings{}, frontendMTLSStatusError(string(gatewayv1.ListenerReasonNoValidCACertificate),
			"frontend mTLS requires at least one caCertificateRef or OCI CA bundle OCID annotation",
		)
	}

	verifyDepth, err := frontendMTLSVerifyDepth(gateway, port)
	if err != nil {
		return frontendMTLSSettings{}, err
	}
	return frontendMTLSSettings{
		caRefs:      caRefs,
		ociCAIDs:    optionCAIDs,
		verifyDepth: verifyDepth,
	}, nil
}

func (m *ociLoadBalancerModelImpl) resolveFrontendMTLSTrustedCAIDs(
	ctx context.Context,
	params reconcileHTTPListenerParams,
	settings frontendMTLSSettings,
) ([]string, error) {
	trustedCAIDs := make([]string, 0, len(settings.caRefs)+len(settings.ociCAIDs))
	for _, caID := range settings.ociCAIDs {
		if _, getErr := m.certsClient.GetCaBundle(ctx, certificatesmanagement.GetCaBundleRequest{
			CaBundleId: &caID,
		}); getErr != nil {
			return nil, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
				fmt.Sprintf("frontend mTLS OCI CA bundle %s cannot be resolved", caID),
			)
		}
		trustedCAIDs = append(trustedCAIDs, caID)
	}

	for _, ref := range settings.caRefs {
		resolved, resolveErr := m.resolveFrontendMTLSCARef(ctx, *params.gateway, ref)
		if resolveErr != nil {
			return nil, resolveErr
		}
		caID, ensureErr := m.ensureFrontendMTLSCABundle(
			ctx,
			*params.gateway,
			params.listenerSpec.Port,
			resolved.ref,
			params.loadBalancerCompartmentID,
			resolved.caPEM,
		)
		if ensureErr != nil {
			return nil, ensureErr
		}
		trustedCAIDs = append(trustedCAIDs, caID)
	}
	return trustedCAIDs, nil
}

func effectiveFrontendTLSValidation(
	gateway gatewayv1.Gateway,
	port gatewayv1.PortNumber,
) *gatewayv1.FrontendTLSValidation {
	if gateway.Spec.TLS == nil || gateway.Spec.TLS.Frontend == nil {
		return nil
	}
	for i := range gateway.Spec.TLS.Frontend.PerPort {
		portConfig := gateway.Spec.TLS.Frontend.PerPort[i]
		if portConfig.Port == port {
			return portConfig.TLS.Validation
		}
	}
	return gateway.Spec.TLS.Frontend.Default.Validation
}

func frontendMTLSOCICABundleIDs(gateway gatewayv1.Gateway, port gatewayv1.PortNumber) []string {
	if gateway.Annotations == nil {
		return nil
	}
	value := gateway.Annotations[frontendMTLSPortTrustedCABundleOCIDsAnnotation(port)]
	if strings.TrimSpace(value) != "" {
		return splitCSVOption(gatewayv1.AnnotationValue(value))
	}
	return splitCSVOption(gatewayv1.AnnotationValue(gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation]))
}

func frontendMTLSVerifyDepth(gateway gatewayv1.Gateway, port gatewayv1.PortNumber) (int, error) {
	if gateway.Annotations == nil {
		return defaultFrontendMTLSVerifyDepth, nil
	}
	value := strings.TrimSpace(gateway.Annotations[frontendMTLSPortVerifyDepthAnnotation(port)])
	if value == "" {
		value = strings.TrimSpace(gateway.Annotations[FrontendMTLSVerifyDepthAnnotation])
	}
	if value == "" {
		return defaultFrontendMTLSVerifyDepth, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, frontendMTLSStatusError(string(gatewayv1.GatewayReasonInvalidParameters),
			fmt.Sprintf("annotation %s must be a positive integer", FrontendMTLSVerifyDepthAnnotation),
		)
	}
	return parsed, nil
}

func (m *ociLoadBalancerModelImpl) resolveFrontendMTLSCARef(
	ctx context.Context,
	gateway gatewayv1.Gateway,
	ref gatewayv1.ObjectReference,
) (frontendMTLSResolvedCARef, error) {
	if ref.Group != "" || ref.Kind != "ConfigMap" {
		return frontendMTLSResolvedCARef{}, frontendMTLSStatusError(
			string(gatewayv1.ListenerReasonInvalidCACertificateKind),
			fmt.Sprintf("frontend mTLS caCertificateRef %s/%s must reference a core ConfigMap", ref.Group, ref.Kind),
		)
	}
	refNamespace := gateway.Namespace
	if ref.Namespace != nil {
		refNamespace = string(*ref.Namespace)
	}
	fullName := apitypes.NamespacedName{Namespace: refNamespace, Name: string(ref.Name)}
	allowed, err := referenceGrantAllowsCoreRef(
		ctx,
		m.k8sClient,
		gatewayv1.Kind("Gateway"),
		gateway.Namespace,
		"ConfigMap",
		fullName,
	)
	if err != nil {
		return frontendMTLSResolvedCARef{}, err
	}
	if !allowed {
		return frontendMTLSResolvedCARef{}, frontendMTLSStatusError(
			string(gatewayv1.GatewayReasonRefNotPermitted),
			fmt.Sprintf("frontend mTLS caCertificateRef %s is not permitted by a ReferenceGrant", fullName.String()),
		)
	}

	var configMap corev1.ConfigMap
	if err = m.k8sClient.Get(ctx, fullName, &configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return frontendMTLSResolvedCARef{}, frontendMTLSStatusError(
				string(gatewayv1.ListenerReasonInvalidCACertificateRef),
				fmt.Sprintf("frontend mTLS caCertificateRef ConfigMap %s was not found", fullName.String()),
			)
		}
		return frontendMTLSResolvedCARef{}, fmt.Errorf(
			"failed to get frontend mTLS ConfigMap %s: %w",
			fullName.String(),
			err,
		)
	}
	caPEM, ok := configMap.Data["ca.crt"]
	if !ok {
		return frontendMTLSResolvedCARef{}, frontendMTLSStatusError(
			string(gatewayv1.ListenerReasonInvalidCACertificateRef),
			fmt.Sprintf("frontend mTLS caCertificateRef ConfigMap %s is missing ca.crt", fullName.String()),
		)
	}
	if err = validateCABundlePEM(caPEM); err != nil {
		return frontendMTLSResolvedCARef{}, frontendMTLSStatusError(
			string(gatewayv1.ListenerReasonInvalidCACertificateRef),
			fmt.Sprintf("frontend mTLS caCertificateRef ConfigMap %s has invalid ca.crt: %v", fullName.String(), err),
		)
	}
	return frontendMTLSResolvedCARef{ref: ref, caPEM: caPEM}, nil
}

func (m *ociLoadBalancerModelImpl) ensureFrontendMTLSCABundle(
	ctx context.Context,
	gateway gatewayv1.Gateway,
	port gatewayv1.PortNumber,
	ref gatewayv1.ObjectReference,
	compartmentID string,
	caPEM string,
) (string, error) {
	if err := m.ensureFrontendMTLSCompartment(ctx, gateway, compartmentID); err != nil {
		return "", err
	}
	name := frontendMTLSCABundleName(gateway, port, ref)
	caHash := sha256Hex(caPEM)
	tags := frontendMTLSCABundleTags(gateway, port, caHash)
	listResp, err := m.certsClient.ListCaBundles(ctx, certificatesmanagement.ListCaBundlesRequest{
		CompartmentId: &compartmentID,
		Name:          &name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list OCI CA bundles for frontend mTLS Gateway %s/%s: %w",
			gateway.Namespace,
			gateway.Name,
			err,
		)
	}
	for _, bundle := range listResp.Items {
		if bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleted {
			continue
		}
		if !isOwnedFrontendMTLSCABundle(bundle.FreeformTags, gateway) {
			return "", fmt.Errorf("OCI CA bundle %s already exists and is not owned by frontend mTLS Gateway %s/%s",
				name,
				gateway.Namespace,
				gateway.Name,
			)
		}
		if usableErr := ensureFrontendMTLSCABundleUsable(bundle); usableErr != nil {
			return "", usableErr
		}
		if bundle.FreeformTags[frontendMTLSCAHashTag] != caHash {
			m.logger.InfoContext(ctx, "Updating OCI CA bundle for frontend mTLS",
				slog.String("gateway", fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name)),
				slog.String("caBundleName", name),
			)
			_, err = m.certsClient.UpdateCaBundle(ctx, certificatesmanagement.UpdateCaBundleRequest{
				CaBundleId: bundle.Id,
				UpdateCaBundleDetails: certificatesmanagement.UpdateCaBundleDetails{
					CaBundlePem:  &caPEM,
					FreeformTags: tags,
				},
			})
			if err != nil {
				return "", fmt.Errorf("failed to update OCI CA bundle %s: %w", name, err)
			}
		}
		return lo.FromPtr(bundle.Id), nil
	}

	m.logger.InfoContext(ctx, "Creating OCI CA bundle for frontend mTLS",
		slog.String("gateway", fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name)),
		slog.String("caBundleName", name),
	)
	createResp, err := m.certsClient.CreateCaBundle(ctx, certificatesmanagement.CreateCaBundleRequest{
		CreateCaBundleDetails: certificatesmanagement.CreateCaBundleDetails{
			Name:          &name,
			CompartmentId: &compartmentID,
			CaBundlePem:   &caPEM,
			FreeformTags:  tags,
		},
	})
	if err != nil {
		if isFrontendMTLSCABundleAlreadyExists(err) {
			return m.resolveExistingFrontendMTLSCABundle(ctx, gateway, compartmentID, name, caHash)
		}
		return "", fmt.Errorf("failed to create OCI CA bundle %s: %w", name, err)
	}
	if createResp.CaBundle.LifecycleState != certificatesmanagement.CaBundleLifecycleStateActive {
		return "", fmt.Errorf("OCI CA bundle %s is %s and is not ready for frontend mTLS",
			name,
			createResp.CaBundle.LifecycleState,
		)
	}
	return lo.FromPtr(createResp.CaBundle.Id), nil
}

func (m *ociLoadBalancerModelImpl) ensureFrontendMTLSCompartment(
	ctx context.Context,
	gateway gatewayv1.Gateway,
	compartmentID string,
) error {
	if compartmentID == "" || lo.Contains(frontendMTLSCompartmentIDs(gateway), compartmentID) {
		return nil
	}
	gatewayToUpdate := gateway.DeepCopy()
	if gatewayToUpdate.Annotations == nil {
		gatewayToUpdate.Annotations = map[string]string{}
	}
	compartments := append(frontendMTLSCompartmentIDs(gateway), compartmentID)
	sort.Strings(compartments)
	gatewayToUpdate.Annotations[GatewayFrontendMTLSCABundleCompartmentsAnnotation] = strings.Join(compartments, ",")
	if err := m.k8sClient.Update(ctx, gatewayToUpdate); err != nil {
		return fmt.Errorf("failed to record frontend mTLS CA bundle compartment on Gateway %s/%s: %w",
			gateway.Namespace,
			gateway.Name,
			err,
		)
	}
	return nil
}

func (m *ociLoadBalancerModelImpl) resolveExistingFrontendMTLSCABundle(
	ctx context.Context,
	gateway gatewayv1.Gateway,
	compartmentID string,
	name string,
	caHash string,
) (string, error) {
	listResp, err := m.certsClient.ListCaBundles(ctx, certificatesmanagement.ListCaBundlesRequest{
		CompartmentId: &compartmentID,
		Name:          &name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to re-list OCI CA bundle %s after create conflict: %w", name, err)
	}
	for _, bundle := range listResp.Items {
		if bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleted {
			continue
		}
		if !isOwnedFrontendMTLSCABundle(bundle.FreeformTags, gateway) {
			return "", fmt.Errorf("OCI CA bundle %s already exists and is not owned by frontend mTLS Gateway %s/%s",
				name,
				gateway.Namespace,
				gateway.Name,
			)
		}
		if usableErr := ensureFrontendMTLSCABundleUsable(bundle); usableErr != nil {
			return "", usableErr
		}
		if bundle.FreeformTags[frontendMTLSCAHashTag] != caHash {
			return "", fmt.Errorf(
				"OCI CA bundle %s already exists with stale CA data and is not ready for update",
				name,
			)
		}
		return lo.FromPtr(bundle.Id), nil
	}
	return "", fmt.Errorf("OCI CA bundle %s already exists but was not visible in list response", name)
}

func (m *ociLoadBalancerModelImpl) cleanupFrontendMTLSCABundles(
	ctx context.Context,
	params cleanupFrontendMTLSCABundlesParams,
) error {
	if params.gateway == nil || params.compartmentID == "" || m.certsClient == nil {
		return nil
	}
	compartments := frontendMTLSCompartmentIDs(*params.gateway)
	if len(compartments) == 0 {
		compartments = []string{params.compartmentID}
	}
	for _, compartmentID := range compartments {
		if err := m.cleanupFrontendMTLSCABundlesInCompartment(ctx, params, compartmentID); err != nil {
			return err
		}
	}
	return nil
}

func (m *ociLoadBalancerModelImpl) cleanupFrontendMTLSCABundlesInCompartment(
	ctx context.Context,
	params cleanupFrontendMTLSCABundlesParams,
	compartmentID string,
) error {
	listResp, err := m.certsClient.ListCaBundles(ctx, certificatesmanagement.ListCaBundlesRequest{
		CompartmentId: &compartmentID,
	})
	if err != nil {
		return fmt.Errorf("failed to list OCI CA bundles for frontend mTLS cleanup: %w", err)
	}
	for _, bundle := range listResp.Items {
		if !isOwnedFrontendMTLSCABundle(bundle.FreeformTags, *params.gateway) {
			continue
		}
		if _, desired := params.desiredBundleNames[lo.FromPtr(bundle.Name)]; desired {
			continue
		}
		if bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleted ||
			bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleting {
			continue
		}
		if err = m.deleteFrontendMTLSCABundle(ctx, *params.gateway, bundle); err != nil {
			return err
		}
	}
	return nil
}

func (m *ociLoadBalancerModelImpl) deleteFrontendMTLSCABundle(
	ctx context.Context,
	gateway gatewayv1.Gateway,
	bundle certificatesmanagement.CaBundleSummary,
) error {
	m.logger.InfoContext(ctx, "Deleting OCI CA bundle for frontend mTLS",
		slog.String("gateway", fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name)),
		slog.String("caBundleName", lo.FromPtr(bundle.Name)),
	)
	_, err := m.certsClient.DeleteCaBundle(ctx, certificatesmanagement.DeleteCaBundleRequest{
		CaBundleId: bundle.Id,
	})
	if err == nil || isFrontendMTLSCABundleAlreadyDeleted(err) {
		return nil
	}
	return fmt.Errorf("failed to delete OCI CA bundle %s: %w", lo.FromPtr(bundle.Name), err)
}

func frontendMTLSCompartmentIDs(gateway gatewayv1.Gateway) []string {
	compartmentAnnotation := gateway.Annotations[GatewayFrontendMTLSCABundleCompartmentsAnnotation]
	if compartmentAnnotation == "" {
		return nil
	}
	compartments := strings.Split(compartmentAnnotation, ",")
	uniqueCompartments := make(map[string]struct{}, len(compartments))
	for _, compartment := range compartments {
		compartment = strings.TrimSpace(compartment)
		if compartment == "" {
			continue
		}
		uniqueCompartments[compartment] = struct{}{}
	}
	result := lo.Keys(uniqueCompartments)
	sort.Strings(result)
	return result
}

func frontendMTLSCABundleName(
	gateway gatewayv1.Gateway,
	port gatewayv1.PortNumber,
	ref gatewayv1.ObjectReference,
) string {
	refNamespace := gateway.Namespace
	if ref.Namespace != nil {
		refNamespace = string(*ref.Namespace)
	}
	hashInput := fmt.Sprintf("%s/%s/%s/%d/%s/%s/%s/%s",
		gateway.Namespace,
		gateway.Name,
		frontendMTLSGatewayIdentity(gateway),
		port,
		ref.Group,
		ref.Kind,
		refNamespace,
		ref.Name,
	)
	return frontendMTLSCABundleNamePrefix + sha256Hex(hashInput)[:24]
}

func frontendMTLSGatewayIdentity(gateway gatewayv1.Gateway) string {
	if gateway.UID != "" {
		return string(gateway.UID)
	}
	if !gateway.CreationTimestamp.IsZero() {
		return gateway.CreationTimestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return strconv.FormatInt(gateway.Generation, 10)
}

func frontendMTLSCABundleTags(
	gateway gatewayv1.Gateway,
	port gatewayv1.PortNumber,
	caHash string,
) map[string]string {
	return map[string]string{
		frontendMTLSManagedByTag: frontendMTLSManagedByValue,
		frontendMTLSGatewayTag:   fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name),
		frontendMTLSPortTag:      strconv.Itoa(int(port)),
		frontendMTLSCAHashTag:    caHash,
	}
}

func isOwnedFrontendMTLSCABundle(tags map[string]string, gateway gatewayv1.Gateway) bool {
	return tags[frontendMTLSManagedByTag] == frontendMTLSManagedByValue &&
		tags[frontendMTLSGatewayTag] == fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name)
}

func ensureFrontendMTLSCABundleUsable(bundle certificatesmanagement.CaBundleSummary) error {
	switch bundle.LifecycleState {
	case "", certificatesmanagement.CaBundleLifecycleStateActive:
		return nil
	case certificatesmanagement.CaBundleLifecycleStateDeleting:
		return fmt.Errorf("OCI CA bundle %s is %s and cannot be reused for frontend mTLS",
			lo.FromPtr(bundle.Name),
			bundle.LifecycleState,
		)
	case certificatesmanagement.CaBundleLifecycleStateCreating,
		certificatesmanagement.CaBundleLifecycleStateUpdating,
		certificatesmanagement.CaBundleLifecycleStateDeleted,
		certificatesmanagement.CaBundleLifecycleStateFailed:
		return fmt.Errorf("OCI CA bundle %s is %s and is not ready for frontend mTLS",
			lo.FromPtr(bundle.Name),
			bundle.LifecycleState,
		)
	default:
		return fmt.Errorf("OCI CA bundle %s is %s and is not ready for frontend mTLS",
			lo.FromPtr(bundle.Name),
			bundle.LifecycleState,
		)
	}
}

func isFrontendMTLSCABundleAlreadyExists(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok &&
		serviceErr.GetHTTPStatusCode() == http.StatusBadRequest &&
		serviceErr.GetCode() == "InvalidParameter" &&
		strings.Contains(serviceErr.GetMessage(), "already exists")
}

func isFrontendMTLSCABundleAlreadyDeleted(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok && serviceErr.GetHTTPStatusCode() == http.StatusNotFound
}

func frontendMTLSStatusError(reason string, message string) *resourceStatusError {
	return &resourceStatusError{
		conditionType: string(gatewayv1.GatewayConditionAccepted),
		reason:        reason,
		message:       message,
	}
}
