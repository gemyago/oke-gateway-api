package app

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
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
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

const (
	backendTLSHostnameValidationDisabled = "Disabled"
	backendTLSCABundleNamePrefix         = "oke-btls-"
	backendTLSCAHashTag                  = "oke-gateway-api-ca-sha256"
	backendTLSManagedByTag               = "oke-gateway-api-managed-by"
	backendTLSPolicyTag                  = "oke-gateway-api-policy"
	backendTLSManagedByValue             = "backend-tls-policy"
	defaultBackendTLSVerifyDepth         = 3
)

var (
	errBackendTLSPolicyNotFound          = errors.New("backend TLS policy not found")
	errBackendTLSCABundleStillAssociated = errors.New("backend TLS CA bundle still associated")
)

type resolveBackendTLSPolicyParams struct {
	gateway    gatewayv1.Gateway
	config     types.GatewayConfig
	service    corev1.Service
	backendRef gatewayv1.BackendRef
}

type backendTLSPolicyModel interface {
	resolveForBackendRef(
		ctx context.Context,
		params resolveBackendTLSPolicyParams,
	) (*loadbalancer.SslConfigurationDetails, error)
	cleanupDeletingPolicy(ctx context.Context, policy gatewayv1.BackendTLSPolicy) error
}

type backendTLSPolicyModelImpl struct {
	logger             *slog.Logger
	k8sClient          k8sClient
	loadBalancerClient ociLoadBalancerClient
	certsClient        ociCertificatesManagementClient
}

type backendTLSPolicyStatusError struct {
	policy  gatewayv1.BackendTLSPolicy
	reason  gatewayv1.PolicyConditionReason
	message string
}

func (e backendTLSPolicyStatusError) Error() string {
	return e.message
}

type backendTLSPolicyCandidate struct {
	policy         gatewayv1.BackendTLSPolicy
	targetRef      gatewayv1.LocalPolicyTargetReferenceWithSectionName
	servicePort    *corev1.ServicePort
	invalidReason  gatewayv1.PolicyConditionReason
	invalidMessage string
}

type backendTLSResolvedCARef struct {
	ref   gatewayv1.LocalObjectReference
	caPEM string
}

func (m *backendTLSPolicyModelImpl) resolveForBackendRef(
	ctx context.Context,
	params resolveBackendTLSPolicyParams,
) (*loadbalancer.SslConfigurationDetails, error) {
	candidates, err := m.matchingPolicies(ctx, params.service, params.backendRef)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errBackendTLSPolicyNotFound
	}

	sortBackendTLSPolicyCandidates(candidates)
	selected := candidates[0]
	if selected.invalidReason != "" {
		statusErr := backendTLSPolicyStatusError{
			policy:  selected.policy,
			reason:  selected.invalidReason,
			message: selected.invalidMessage,
		}
		_ = m.setPolicyErrorConditions(ctx, statusErr.policy, params.gateway, statusErr.reason, statusErr.message)
		return nil, statusErr
	}
	for _, conflicted := range candidates[1:] {
		_ = m.setPolicyCondition(ctx, conflicted.policy, params.gateway, metav1.ConditionFalse,
			gatewayv1.PolicyReasonConflicted,
			fmt.Sprintf("BackendTLSPolicy %s/%s has lower precedence than %s/%s for Service %s/%s",
				conflicted.policy.Namespace,
				conflicted.policy.Name,
				selected.policy.Namespace,
				selected.policy.Name,
				params.service.Namespace,
				params.service.Name,
			),
		)
	}

	sslConfig, err := m.resolveAcceptedPolicy(ctx, selected, params)
	if err != nil {
		var statusErr backendTLSPolicyStatusError
		if ok := errors.As(err, &statusErr); ok {
			_ = m.setPolicyErrorConditions(ctx, statusErr.policy, params.gateway, statusErr.reason, statusErr.message)
		}
		return nil, err
	}

	if err = m.setPolicyConditions(
		ctx,
		selected.policy,
		params.gateway,
		backendTLSPolicyCondition(
			selected.policy.Generation,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionTrue,
			gatewayv1.PolicyReasonAccepted,
			"OCI backend TLS is configured with CA-chain validation only; hostname/SAN validation is disabled by explicit option.",
		),
		backendTLSPolicyCondition(
			selected.policy.Generation,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionTrue,
			gatewayv1.BackendTLSPolicyReasonResolvedRefs,
			"BackendTLSPolicy references are resolved.",
		),
	); err != nil {
		return nil, err
	}
	return sslConfig, nil
}

func (m *backendTLSPolicyModelImpl) matchingPolicies(
	ctx context.Context,
	service corev1.Service,
	backendRef gatewayv1.BackendRef,
) ([]backendTLSPolicyCandidate, error) {
	var policyList gatewayv1.BackendTLSPolicyList
	if err := m.k8sClient.List(ctx, &policyList, client.InNamespace(service.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list BackendTLSPolicies: %w", err)
	}
	matches := make([]backendTLSPolicyCandidate, 0)
	for _, policy := range policyList.Items {
		if policy.DeletionTimestamp != nil {
			continue
		}
		for _, targetRef := range policy.Spec.TargetRefs {
			servicePort, matched := backendTLSPolicyTargetMatchesService(targetRef, service, backendRef)
			if matched {
				matches = append(matches, backendTLSPolicyCandidate{
					policy:      policy,
					targetRef:   targetRef,
					servicePort: servicePort,
				})
				continue
			}
			if backendTLSPolicyTargetRefUnsupportedKind(targetRef, service) {
				matches = append(matches, backendTLSPolicyCandidate{
					policy:        policy,
					targetRef:     targetRef,
					invalidReason: gatewayv1.BackendTLSPolicyReasonInvalidKind,
					invalidMessage: fmt.Sprintf(
						"targetRef %s/%s must reference a core Service",
						targetRef.Group,
						targetRef.Kind,
					),
				})
				continue
			}
			if backendTLSPolicyTargetRefMissingServicePort(targetRef, service) {
				matches = append(matches, backendTLSPolicyCandidate{
					policy:        policy,
					targetRef:     targetRef,
					invalidReason: gatewayv1.PolicyReasonTargetNotFound,
					invalidMessage: fmt.Sprintf(
						"targetRef sectionName %q does not match any port on Service %s/%s",
						lo.FromPtr(targetRef.SectionName),
						service.Namespace,
						service.Name,
					),
				})
			}
		}
	}
	return matches, nil
}

func backendTLSPolicyTargetMatchesService(
	targetRef gatewayv1.LocalPolicyTargetReferenceWithSectionName,
	service corev1.Service,
	backendRef gatewayv1.BackendRef,
) (*corev1.ServicePort, bool) {
	if targetRef.Group != "" || targetRef.Kind != "Service" || string(targetRef.Name) != service.Name {
		return nil, false
	}
	if targetRef.SectionName == nil {
		return nil, true
	}
	for i := range service.Spec.Ports {
		port := &service.Spec.Ports[i]
		if port.Name == string(*targetRef.SectionName) &&
			(backendRef.Port == nil || lo.FromPtr(backendRef.Port) == port.Port) {
			return port, true
		}
	}
	return nil, false
}

func backendTLSPolicyTargetRefUnsupportedKind(
	targetRef gatewayv1.LocalPolicyTargetReferenceWithSectionName,
	service corev1.Service,
) bool {
	return string(targetRef.Name) == service.Name &&
		(targetRef.Group != "" || targetRef.Kind != "Service")
}

func backendTLSPolicyTargetRefMissingServicePort(
	targetRef gatewayv1.LocalPolicyTargetReferenceWithSectionName,
	service corev1.Service,
) bool {
	if targetRef.Group != "" ||
		targetRef.Kind != "Service" ||
		string(targetRef.Name) != service.Name ||
		targetRef.SectionName == nil {
		return false
	}
	for i := range service.Spec.Ports {
		if service.Spec.Ports[i].Name == string(*targetRef.SectionName) {
			return false
		}
	}
	return true
}

func sortBackendTLSPolicyCandidates(candidates []backendTLSPolicyCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i].policy
		right := candidates[j].policy
		if !left.CreationTimestamp.Equal(&right.CreationTimestamp) {
			return left.CreationTimestamp.Before(&right.CreationTimestamp)
		}
		return fmt.Sprintf("%s/%s", left.Namespace, left.Name) < fmt.Sprintf("%s/%s", right.Namespace, right.Name)
	})
}

func (m *backendTLSPolicyModelImpl) resolveAcceptedPolicy(
	ctx context.Context,
	candidate backendTLSPolicyCandidate,
	params resolveBackendTLSPolicyParams,
) (*loadbalancer.SslConfigurationDetails, error) {
	policy := candidate.policy
	if err := validateBackendTLSPolicyShape(policy); err != nil {
		return nil, err
	}

	resolvedCARefs := make([]backendTLSResolvedCARef, 0, len(policy.Spec.Validation.CACertificateRefs))
	for _, caRef := range policy.Spec.Validation.CACertificateRefs {
		caPEM, resolveErr := m.resolveCACertificateRefPEM(ctx, policy, caRef)
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolvedCARefs = append(resolvedCARefs, backendTLSResolvedCARef{
			ref:   caRef,
			caPEM: caPEM,
		})
	}
	optionCAIDs := splitCSVOption(policy.Spec.Options[BackendTLSOptionTrustedCABundleOCIDs])
	for _, caID := range optionCAIDs {
		if _, err := m.certsClient.GetCaBundle(ctx, certificatesmanagement.GetCaBundleRequest{
			CaBundleId: &caID,
		}); err != nil {
			return nil, backendTLSPolicyStatusError{
				policy: policy,
				reason: gatewayv1.PolicyReasonInvalid,
				message: fmt.Sprintf("option %s references an OCI CA bundle that cannot be resolved: %s",
					BackendTLSOptionTrustedCABundleOCIDs,
					caID,
				),
			}
		}
	}
	sslConfig, err := backendTLSSSLConfigFromOptions(policy, nil)
	if err != nil {
		return nil, err
	}

	lbResp, err := m.loadBalancerClient.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: &params.config.Spec.LoadBalancerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get Load Balancer %s for BackendTLSPolicy: %w",
			params.config.Spec.LoadBalancerID,
			err,
		)
	}
	compartmentID := lo.FromPtr(lbResp.LoadBalancer.CompartmentId)
	if compartmentID == "" {
		return nil, fmt.Errorf("failed to resolve Load Balancer compartment for BackendTLSPolicy %s/%s",
			policy.Namespace,
			policy.Name,
		)
	}
	if err = m.ensurePolicyFinalizerAndCompartment(ctx, policy, compartmentID); err != nil {
		return nil, err
	}

	trustedCAIDs := make([]string, 0, len(resolvedCARefs)+len(optionCAIDs))
	for _, caRef := range resolvedCARefs {
		caID, resolveErr := m.ensureOCIManagedCABundle(
			ctx,
			policy,
			candidate.targetRef,
			caRef.ref,
			compartmentID,
			caRef.caPEM,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		trustedCAIDs = append(trustedCAIDs, caID)
	}
	trustedCAIDs = append(trustedCAIDs, optionCAIDs...)
	trustedCAIDs = lo.Uniq(trustedCAIDs)
	sort.Strings(trustedCAIDs)
	sslConfig.TrustedCertificateAuthorityIds = trustedCAIDs
	return sslConfig, nil
}

func validateBackendTLSPolicyShape(policy gatewayv1.BackendTLSPolicy) error {
	if policy.Spec.Validation.WellKnownCACertificates != nil &&
		string(lo.FromPtr(policy.Spec.Validation.WellKnownCACertificates)) != "" {
		return backendTLSPolicyStatusError{
			policy:  policy,
			reason:  gatewayv1.PolicyReasonInvalid,
			message: "wellKnownCACertificates is not supported by OCI backend TLS",
		}
	}
	if len(policy.Spec.Validation.SubjectAltNames) > 0 {
		return backendTLSPolicyStatusError{
			policy:  policy,
			reason:  gatewayv1.PolicyReasonInvalid,
			message: "subjectAltNames is not supported because OCI backend TLS does not enforce hostname/SAN identity",
		}
	}
	hostnameValidation := string(policy.Spec.Options[BackendTLSOptionHostnameValidation])
	if hostnameValidation != backendTLSHostnameValidationDisabled {
		return backendTLSPolicyStatusError{
			policy: policy,
			reason: gatewayv1.PolicyReasonInvalid,
			message: fmt.Sprintf(
				"option %s must be set to %q because OCI backend TLS does not enforce hostname/SAN identity",
				BackendTLSOptionHostnameValidation,
				backendTLSHostnameValidationDisabled,
			),
		}
	}
	for key := range policy.Spec.Options {
		if !supportedBackendTLSOption(string(key)) {
			return backendTLSPolicyStatusError{
				policy:  policy,
				reason:  gatewayv1.PolicyReasonInvalid,
				message: fmt.Sprintf("unsupported BackendTLSPolicy option %s", key),
			}
		}
	}
	if len(policy.Spec.Validation.CACertificateRefs) == 0 {
		return backendTLSPolicyStatusError{
			policy:  policy,
			reason:  gatewayv1.BackendTLSPolicyReasonNoValidCACertificate,
			message: "at least one caCertificateRef is required for OCI backend TLS",
		}
	}
	return nil
}

func supportedBackendTLSOption(key string) bool {
	switch key {
	case BackendTLSOptionHostnameValidation,
		BackendTLSOptionTrustedCABundleOCIDs,
		BackendTLSOptionProtocols,
		BackendTLSOptionCipherSuiteName,
		BackendTLSOptionVerifyDepth,
		BackendTLSOptionSessionResumption:
		return true
	default:
		return false
	}
}

func (m *backendTLSPolicyModelImpl) resolveCACertificateRefPEM(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	ref gatewayv1.LocalObjectReference,
) (string, error) {
	if ref.Group != "" || ref.Kind != "ConfigMap" {
		return "", backendTLSPolicyStatusError{
			policy:  policy,
			reason:  gatewayv1.BackendTLSPolicyReasonInvalidKind,
			message: fmt.Sprintf("caCertificateRef %s/%s must reference a core ConfigMap", ref.Group, ref.Kind),
		}
	}
	var configMap corev1.ConfigMap
	if err := m.k8sClient.Get(ctx, apitypes.NamespacedName{
		Namespace: policy.Namespace,
		Name:      string(ref.Name),
	}, &configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return "", backendTLSPolicyStatusError{
				policy:  policy,
				reason:  gatewayv1.BackendTLSPolicyReasonInvalidCACertificateRef,
				message: fmt.Sprintf("caCertificateRef ConfigMap %s/%s was not found", policy.Namespace, ref.Name),
			}
		}
		return "", fmt.Errorf("failed to get caCertificateRef ConfigMap %s/%s: %w", policy.Namespace, ref.Name, err)
	}
	caPEM, ok := configMap.Data["ca.crt"]
	if !ok {
		return "", backendTLSPolicyStatusError{
			policy:  policy,
			reason:  gatewayv1.BackendTLSPolicyReasonInvalidCACertificateRef,
			message: fmt.Sprintf("caCertificateRef ConfigMap %s/%s is missing ca.crt", policy.Namespace, ref.Name),
		}
	}
	if err := validateCABundlePEM(caPEM); err != nil {
		return "", backendTLSPolicyStatusError{
			policy: policy,
			reason: gatewayv1.BackendTLSPolicyReasonInvalidCACertificateRef,
			message: fmt.Sprintf(
				"caCertificateRef ConfigMap %s/%s has invalid ca.crt: %v",
				policy.Namespace,
				ref.Name,
				err,
			),
		}
	}
	return caPEM, nil
}

func validateCABundlePEM(caPEM string) error {
	remaining := []byte(caPEM)
	parsed := 0
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("unexpected PEM block type %q", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("failed to parse certificate: %w", err)
		}
		if !cert.IsCA {
			return fmt.Errorf("certificate %s is not a CA certificate", cert.Subject.CommonName)
		}
		parsed++
	}
	if parsed == 0 {
		return errors.New("no CA certificates found")
	}
	if strings.TrimSpace(string(remaining)) != "" {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func (m *backendTLSPolicyModelImpl) ensureOCIManagedCABundle(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	targetRef gatewayv1.LocalPolicyTargetReferenceWithSectionName,
	ref gatewayv1.LocalObjectReference,
	compartmentID string,
	caPEM string,
) (string, error) {
	name := backendTLSCABundleName(policy, targetRef, ref)
	caHash := sha256Hex(caPEM)
	tags := backendTLSCABundleTags(policy, caHash)
	listResp, err := m.certsClient.ListCaBundles(ctx, certificatesmanagement.ListCaBundlesRequest{
		CompartmentId: &compartmentID,
		Name:          &name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list OCI CA bundles for BackendTLSPolicy %s/%s: %w",
			policy.Namespace,
			policy.Name,
			err,
		)
	}
	for _, bundle := range listResp.Items {
		if bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleted {
			continue
		}
		if !isOwnedBackendTLSCABundle(bundle.FreeformTags, policy) {
			return "", fmt.Errorf("OCI CA bundle %s already exists and is not owned by BackendTLSPolicy %s/%s",
				name,
				policy.Namespace,
				policy.Name,
			)
		}
		if usableErr := ensureBackendTLSCABundleUsable(bundle); usableErr != nil {
			return "", usableErr
		}
		if bundle.FreeformTags[backendTLSCAHashTag] != caHash {
			m.logger.InfoContext(ctx, "Updating OCI CA bundle for BackendTLSPolicy",
				slog.String("policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)),
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

	m.logger.InfoContext(ctx, "Creating OCI CA bundle for BackendTLSPolicy",
		slog.String("policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)),
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
		if isBackendTLSCABundleAlreadyExists(err) {
			return m.resolveExistingOCIManagedCABundle(ctx, policy, compartmentID, name, caHash)
		}
		return "", fmt.Errorf("failed to create OCI CA bundle %s: %w", name, err)
	}
	if createResp.CaBundle.LifecycleState != certificatesmanagement.CaBundleLifecycleStateActive {
		return "", fmt.Errorf("OCI CA bundle %s is %s and is not ready for backend TLS",
			name,
			createResp.CaBundle.LifecycleState,
		)
	}
	return lo.FromPtr(createResp.CaBundle.Id), nil
}

func (m *backendTLSPolicyModelImpl) resolveExistingOCIManagedCABundle(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
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
		if !isOwnedBackendTLSCABundle(bundle.FreeformTags, policy) {
			return "", fmt.Errorf("OCI CA bundle %s already exists and is not owned by BackendTLSPolicy %s/%s",
				name,
				policy.Namespace,
				policy.Name,
			)
		}
		if usableErr := ensureBackendTLSCABundleUsable(bundle); usableErr != nil {
			return "", usableErr
		}
		if bundle.FreeformTags[backendTLSCAHashTag] != caHash {
			return "", fmt.Errorf(
				"OCI CA bundle %s already exists with stale CA data and is not ready for update",
				name,
			)
		}
		m.logger.InfoContext(ctx, "Reusing OCI CA bundle after create conflict",
			slog.String("policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)),
			slog.String("caBundleName", name),
		)
		return lo.FromPtr(bundle.Id), nil
	}
	return "", fmt.Errorf("OCI CA bundle %s already exists but was not visible in list response", name)
}

func ensureBackendTLSCABundleUsable(bundle certificatesmanagement.CaBundleSummary) error {
	switch bundle.LifecycleState {
	case "", certificatesmanagement.CaBundleLifecycleStateActive:
		return nil
	case certificatesmanagement.CaBundleLifecycleStateDeleting:
		return fmt.Errorf("OCI CA bundle %s is %s and cannot be reused for backend TLS",
			lo.FromPtr(bundle.Name),
			bundle.LifecycleState,
		)
	case certificatesmanagement.CaBundleLifecycleStateCreating,
		certificatesmanagement.CaBundleLifecycleStateUpdating,
		certificatesmanagement.CaBundleLifecycleStateDeleted,
		certificatesmanagement.CaBundleLifecycleStateFailed:
		return fmt.Errorf("OCI CA bundle %s is %s and is not ready for backend TLS",
			lo.FromPtr(bundle.Name),
			bundle.LifecycleState,
		)
	default:
		return fmt.Errorf("OCI CA bundle %s is %s and is not ready for backend TLS",
			lo.FromPtr(bundle.Name),
			bundle.LifecycleState,
		)
	}
}

func backendTLSCABundleName(
	policy gatewayv1.BackendTLSPolicy,
	targetRef gatewayv1.LocalPolicyTargetReferenceWithSectionName,
	ref gatewayv1.LocalObjectReference,
) string {
	hashInput := fmt.Sprintf("%s/%s/%s/%s/%s/%s/%s/%s/%s",
		policy.Namespace,
		policy.Name,
		targetRef.Group,
		targetRef.Kind,
		targetRef.Name,
		lo.FromPtr(targetRef.SectionName),
		ref.Group,
		ref.Kind,
		ref.Name,
	)
	return backendTLSCABundleNamePrefix + sha256Hex(hashInput)[:24]
}

func backendTLSCABundleTags(policy gatewayv1.BackendTLSPolicy, caHash string) map[string]string {
	return map[string]string{
		backendTLSManagedByTag: backendTLSManagedByValue,
		backendTLSPolicyTag:    fmt.Sprintf("%s/%s", policy.Namespace, policy.Name),
		backendTLSCAHashTag:    caHash,
	}
}

func isOwnedBackendTLSCABundle(tags map[string]string, policy gatewayv1.BackendTLSPolicy) bool {
	return tags[backendTLSManagedByTag] == backendTLSManagedByValue &&
		tags[backendTLSPolicyTag] == fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func backendTLSSSLConfigFromOptions(
	policy gatewayv1.BackendTLSPolicy,
	trustedCAIDs []string,
) (*loadbalancer.SslConfigurationDetails, error) {
	verifyDepth := defaultBackendTLSVerifyDepth
	if value := strings.TrimSpace(string(policy.Spec.Options[BackendTLSOptionVerifyDepth])); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return nil, backendTLSPolicyStatusError{
				policy:  policy,
				reason:  gatewayv1.PolicyReasonInvalid,
				message: fmt.Sprintf("option %s must be a positive integer", BackendTLSOptionVerifyDepth),
			}
		}
		verifyDepth = parsed
	}
	verifyPeer := true
	sslConfig := &loadbalancer.SslConfigurationDetails{
		VerifyPeerCertificate:          &verifyPeer,
		VerifyDepth:                    &verifyDepth,
		TrustedCertificateAuthorityIds: trustedCAIDs,
	}
	if protocols := splitCSVOption(policy.Spec.Options[BackendTLSOptionProtocols]); len(protocols) > 0 {
		sslConfig.Protocols = protocols
	}
	cipherSuite := strings.TrimSpace(string(policy.Spec.Options[BackendTLSOptionCipherSuiteName]))
	if cipherSuite != "" {
		sslConfig.CipherSuiteName = &cipherSuite
	}
	if value := strings.TrimSpace(string(policy.Spec.Options[BackendTLSOptionSessionResumption])); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, backendTLSPolicyStatusError{
				policy:  policy,
				reason:  gatewayv1.PolicyReasonInvalid,
				message: fmt.Sprintf("option %s must be a boolean", BackendTLSOptionSessionResumption),
			}
		}
		sslConfig.HasSessionResumption = &parsed
	}
	return sslConfig, nil
}

func splitCSVOption(value gatewayv1.AnnotationValue) []string {
	parts := strings.Split(string(value), ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func (m *backendTLSPolicyModelImpl) ensurePolicyFinalizerAndCompartment(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	compartmentID string,
) error {
	policyToUpdate := policy.DeepCopy()
	needsUpdate := controllerutil.AddFinalizer(policyToUpdate, BackendTLSPolicyProgrammedFinalizer)
	if addBackendTLSPolicyCompartment(policyToUpdate, compartmentID) {
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	if err := m.k8sClient.Update(ctx, policyToUpdate); err != nil {
		return fmt.Errorf("failed to add BackendTLSPolicy finalizer: %w", err)
	}
	return nil
}

func addBackendTLSPolicyCompartment(policy *gatewayv1.BackendTLSPolicy, compartmentID string) bool {
	if compartmentID == "" {
		return false
	}
	compartmentIDs := backendTLSPolicyCompartmentIDs(*policy)
	if lo.Contains(compartmentIDs, compartmentID) {
		return false
	}
	compartmentIDs = append(compartmentIDs, compartmentID)
	sort.Strings(compartmentIDs)
	if policy.Annotations == nil {
		policy.Annotations = map[string]string{}
	}
	policy.Annotations[BackendTLSPolicyCompartmentsAnnotation] = strings.Join(compartmentIDs, ",")
	return true
}

func backendTLSPolicyCompartmentIDs(policy gatewayv1.BackendTLSPolicy) []string {
	compartmentAnnotation := policy.Annotations[BackendTLSPolicyCompartmentsAnnotation]
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

func (m *backendTLSPolicyModelImpl) setPolicyCondition(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	gateway gatewayv1.Gateway,
	status metav1.ConditionStatus,
	reason gatewayv1.PolicyConditionReason,
	message string,
) error {
	return m.setPolicyConditions(ctx, policy, gateway, backendTLSPolicyCondition(
		policy.Generation,
		gatewayv1.PolicyConditionAccepted,
		status,
		reason,
		message,
	))
}

func (m *backendTLSPolicyModelImpl) setPolicyErrorConditions(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	gateway gatewayv1.Gateway,
	reason gatewayv1.PolicyConditionReason,
	message string,
) error {
	return m.setPolicyConditions(
		ctx,
		policy,
		gateway,
		backendTLSPolicyCondition(
			policy.Generation,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionFalse,
			reason,
			message,
		),
		backendTLSPolicyCondition(
			policy.Generation,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionFalse,
			reason,
			message,
		),
	)
}

func backendTLSPolicyCondition(
	observedGeneration int64,
	conditionType gatewayv1.PolicyConditionType,
	status metav1.ConditionStatus,
	reason gatewayv1.PolicyConditionReason,
	message string,
) metav1.Condition {
	return metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: observedGeneration,
		LastTransitionTime: metav1.Now(),
	}
}

func (m *backendTLSPolicyModelImpl) setPolicyConditions(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	gateway gatewayv1.Gateway,
	conditions ...metav1.Condition,
) error {
	var policyToUpdate gatewayv1.BackendTLSPolicy
	if err := m.k8sClient.Get(ctx, apitypes.NamespacedName{
		Namespace: policy.Namespace,
		Name:      policy.Name,
	}, &policyToUpdate); err != nil {
		return fmt.Errorf("failed to get BackendTLSPolicy %s/%s before status update: %w",
			policy.Namespace,
			policy.Name,
			err,
		)
	}
	gatewayNamespace := gatewayv1.Namespace(gateway.Namespace)
	ancestorRef := gatewayv1.ParentReference{
		Group:     lo.ToPtr(gatewayv1.Group(gatewayv1.GroupName)),
		Kind:      lo.ToPtr(gatewayv1.Kind("Gateway")),
		Namespace: &gatewayNamespace,
		Name:      gatewayv1.ObjectName(gateway.Name),
	}
	controllerName := gatewayv1.GatewayController(ControllerClassName)
	originalAncestors := policyToUpdate.DeepCopy().Status.Ancestors
	ancestorIndex := -1
	for i := range policyToUpdate.Status.Ancestors {
		ancestor := policyToUpdate.Status.Ancestors[i]
		if ancestor.ControllerName == controllerName &&
			parentRefsEqual(ancestor.AncestorRef, ancestorRef) {
			ancestorIndex = i
			break
		}
	}
	if ancestorIndex == -1 {
		policyToUpdate.Status.Ancestors = append(policyToUpdate.Status.Ancestors, gatewayv1.PolicyAncestorStatus{
			AncestorRef:    ancestorRef,
			ControllerName: controllerName,
			Conditions:     conditions,
		})
	} else {
		for _, condition := range conditions {
			meta.SetStatusCondition(&policyToUpdate.Status.Ancestors[ancestorIndex].Conditions, condition)
		}
	}
	if reflect.DeepEqual(originalAncestors, policyToUpdate.Status.Ancestors) {
		return nil
	}
	if err := m.k8sClient.Status().Update(ctx, &policyToUpdate); err != nil {
		return fmt.Errorf("failed to update BackendTLSPolicy %s/%s status: %w",
			policy.Namespace,
			policy.Name,
			err,
		)
	}
	return nil
}

func parentRefsEqual(left, right gatewayv1.ParentReference) bool {
	return lo.FromPtr(left.Group) == lo.FromPtr(right.Group) &&
		lo.FromPtr(left.Kind) == lo.FromPtr(right.Kind) &&
		lo.FromPtr(left.Namespace) == lo.FromPtr(right.Namespace) &&
		left.Name == right.Name &&
		lo.FromPtr(left.SectionName) == lo.FromPtr(right.SectionName) &&
		lo.FromPtr(left.Port) == lo.FromPtr(right.Port)
}

func (m *backendTLSPolicyModelImpl) cleanupDeletingPolicy(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
) error {
	if !controllerutil.ContainsFinalizer(&policy, BackendTLSPolicyProgrammedFinalizer) {
		return nil
	}
	compartmentIDs := lo.SliceToMap(backendTLSPolicyCompartmentIDs(policy), func(id string) (string, struct{}) {
		return id, struct{}{}
	})
	if len(compartmentIDs) == 0 {
		discoveredCompartments, err := m.discoverBackendTLSPolicyCleanupCompartments(ctx)
		if err != nil {
			return err
		}
		compartmentIDs = discoveredCompartments
	}
	for _, compartmentID := range lo.Keys(compartmentIDs) {
		if err := m.deleteOwnedCABundles(ctx, policy, compartmentID); err != nil {
			return err
		}
	}
	policyToUpdate := policy.DeepCopy()
	controllerutil.RemoveFinalizer(policyToUpdate, BackendTLSPolicyProgrammedFinalizer)
	delete(policyToUpdate.Annotations, BackendTLSPolicyCompartmentsAnnotation)
	if err := m.k8sClient.Update(ctx, policyToUpdate); err != nil {
		return fmt.Errorf("failed to remove BackendTLSPolicy finalizer: %w", err)
	}
	return nil
}

func (m *backendTLSPolicyModelImpl) discoverBackendTLSPolicyCleanupCompartments(
	ctx context.Context,
) (map[string]struct{}, error) {
	var gateways gatewayv1.GatewayList
	if err := m.k8sClient.List(ctx, &gateways); err != nil {
		return nil, fmt.Errorf("failed to list Gateways for BackendTLSPolicy cleanup: %w", err)
	}
	compartmentIDs := map[string]struct{}{}
	for _, gateway := range gateways.Items {
		compartmentID, err := m.backendTLSPolicyCleanupCompartment(ctx, gateway)
		if err != nil {
			return nil, err
		}
		if compartmentID == "" {
			continue
		}
		compartmentIDs[compartmentID] = struct{}{}
	}
	return compartmentIDs, nil
}

func (m *backendTLSPolicyModelImpl) backendTLSPolicyCleanupCompartment(
	ctx context.Context,
	gateway gatewayv1.Gateway,
) (string, error) {
	if gateway.Spec.Infrastructure == nil || gateway.Spec.Infrastructure.ParametersRef == nil {
		return "", nil
	}
	var config types.GatewayConfig
	configKey := apitypes.NamespacedName{
		Namespace: gateway.Namespace,
		Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
	}
	if getErr := m.k8sClient.Get(ctx, configKey, &config); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return "", nil
		}
		return "", fmt.Errorf(
			"failed to get GatewayConfig %s for BackendTLSPolicy cleanup: %w",
			configKey.String(),
			getErr,
		)
	}
	lbResp, err := m.loadBalancerClient.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: &config.Spec.LoadBalancerID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get Load Balancer %s for BackendTLSPolicy cleanup: %w",
			config.Spec.LoadBalancerID, err)
	}
	return lo.FromPtr(lbResp.LoadBalancer.CompartmentId), nil
}

func (m *backendTLSPolicyModelImpl) deleteOwnedCABundles(
	ctx context.Context,
	policy gatewayv1.BackendTLSPolicy,
	compartmentID string,
) error {
	if compartmentID == "" {
		return nil
	}
	listResp, err := m.certsClient.ListCaBundles(ctx, certificatesmanagement.ListCaBundlesRequest{
		CompartmentId: &compartmentID,
	})
	if err != nil {
		return fmt.Errorf("failed to list OCI CA bundles for BackendTLSPolicy cleanup: %w", err)
	}
	for _, bundle := range listResp.Items {
		if !isOwnedBackendTLSCABundle(bundle.FreeformTags, policy) {
			continue
		}
		if bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleted ||
			bundle.LifecycleState == certificatesmanagement.CaBundleLifecycleStateDeleting {
			continue
		}
		m.logger.InfoContext(ctx, "Deleting OCI CA bundle for BackendTLSPolicy",
			slog.String("policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name)),
			slog.String("caBundleName", lo.FromPtr(bundle.Name)),
		)
		if _, err = m.certsClient.DeleteCaBundle(ctx, certificatesmanagement.DeleteCaBundleRequest{
			CaBundleId: bundle.Id,
		}); err != nil {
			if isBackendTLSCABundleAlreadyDeleted(err) {
				continue
			}
			if isBackendTLSCABundleStillAssociated(err) {
				return errBackendTLSCABundleStillAssociated
			}
			return fmt.Errorf("failed to delete OCI CA bundle %s: %w", lo.FromPtr(bundle.Name), err)
		}
	}
	return nil
}

func isBackendTLSCABundleAlreadyDeleted(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok && serviceErr.GetHTTPStatusCode() == http.StatusNotFound
}

func isBackendTLSCABundleStillAssociated(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok &&
		serviceErr.GetHTTPStatusCode() == http.StatusConflict &&
		serviceErr.GetCode() == "IncorrectState" &&
		strings.Contains(serviceErr.GetMessage(), "Association")
}

func isBackendTLSCABundleAlreadyExists(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok &&
		serviceErr.GetHTTPStatusCode() == http.StatusBadRequest &&
		serviceErr.GetCode() == "InvalidParameter" &&
		strings.Contains(serviceErr.GetMessage(), "already exists")
}

type backendTLSPolicyModelDeps struct {
	dig.In `ignore-unexported:"true"`

	RootLogger                *slog.Logger
	K8sClient                 k8sClient
	OciLoadBalancerClient     ociLoadBalancerClient
	OciCertificatesMgmtClient ociCertificatesManagementClient
}

func newBackendTLSPolicyModel(deps backendTLSPolicyModelDeps) *backendTLSPolicyModelImpl {
	return &backendTLSPolicyModelImpl{
		logger:             deps.RootLogger.WithGroup("backend-tls-policy-model"),
		k8sClient:          deps.K8sClient,
		loadBalancerClient: deps.OciLoadBalancerClient,
		certsClient:        deps.OciCertificatesMgmtClient,
	}
}
