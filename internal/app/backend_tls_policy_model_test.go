package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

type stubCertificatesManagementClient struct {
	bundles            map[string]certificatesmanagement.CaBundleSummary
	createCalls        []certificatesmanagement.CreateCaBundleRequest
	updateCalls        []certificatesmanagement.UpdateCaBundleRequest
	deleteCalls        []certificatesmanagement.DeleteCaBundleRequest
	getErrByID         map[string]error
	createErr          error
	createErrByName    map[string]error
	createState        certificatesmanagement.CaBundleLifecycleStateEnum
	listErr            error
	listEmptyResponses int
	updateErr          error
	deleteErr          error
	nextID             int
}

func newStubCertificatesManagementClient() *stubCertificatesManagementClient {
	return &stubCertificatesManagementClient{
		bundles:         map[string]certificatesmanagement.CaBundleSummary{},
		getErrByID:      map[string]error{},
		createErrByName: map[string]error{},
	}
}

func (s *stubCertificatesManagementClient) CreateCaBundle(
	_ context.Context,
	request certificatesmanagement.CreateCaBundleRequest,
) (certificatesmanagement.CreateCaBundleResponse, error) {
	s.createCalls = append(s.createCalls, request)
	if s.createErr != nil {
		return certificatesmanagement.CreateCaBundleResponse{}, s.createErr
	}
	name := lo.FromPtr(request.CreateCaBundleDetails.Name)
	if err := s.createErrByName[name]; err != nil {
		return certificatesmanagement.CreateCaBundleResponse{}, err
	}
	s.nextID++
	id := "ocid1.cabundle.oc1..created" + string(rune('a'+s.nextID))
	lifecycleState := s.createState
	if lifecycleState == "" {
		lifecycleState = certificatesmanagement.CaBundleLifecycleStateActive
	}
	bundle := certificatesmanagement.CaBundleSummary{
		Id:             &id,
		Name:           &name,
		CompartmentId:  request.CreateCaBundleDetails.CompartmentId,
		LifecycleState: lifecycleState,
		FreeformTags:   request.CreateCaBundleDetails.FreeformTags,
	}
	s.bundles[name] = bundle
	return certificatesmanagement.CreateCaBundleResponse{
		CaBundle: certificatesmanagement.CaBundle{
			Id:             &id,
			Name:           &name,
			CompartmentId:  request.CreateCaBundleDetails.CompartmentId,
			LifecycleState: lifecycleState,
			FreeformTags:   request.CreateCaBundleDetails.FreeformTags,
		},
	}, nil
}

func (s *stubCertificatesManagementClient) GetCaBundle(
	_ context.Context,
	request certificatesmanagement.GetCaBundleRequest,
) (certificatesmanagement.GetCaBundleResponse, error) {
	if err := s.getErrByID[lo.FromPtr(request.CaBundleId)]; err != nil {
		return certificatesmanagement.GetCaBundleResponse{}, err
	}
	return certificatesmanagement.GetCaBundleResponse{
		CaBundle: certificatesmanagement.CaBundle{
			Id:             request.CaBundleId,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
		},
	}, nil
}

func (s *stubCertificatesManagementClient) ListCaBundles(
	_ context.Context,
	request certificatesmanagement.ListCaBundlesRequest,
) (certificatesmanagement.ListCaBundlesResponse, error) {
	if s.listErr != nil {
		return certificatesmanagement.ListCaBundlesResponse{}, s.listErr
	}
	if s.listEmptyResponses > 0 {
		s.listEmptyResponses--
		return certificatesmanagement.ListCaBundlesResponse{
			CaBundleCollection: certificatesmanagement.CaBundleCollection{Items: nil},
		}, nil
	}
	items := make([]certificatesmanagement.CaBundleSummary, 0)
	if request.Name != nil {
		if bundle, ok := s.bundles[lo.FromPtr(request.Name)]; ok {
			items = append(items, bundle)
		}
	} else {
		for _, bundle := range s.bundles {
			items = append(items, bundle)
		}
	}
	return certificatesmanagement.ListCaBundlesResponse{
		CaBundleCollection: certificatesmanagement.CaBundleCollection{Items: items},
	}, nil
}

func (s *stubCertificatesManagementClient) UpdateCaBundle(
	_ context.Context,
	request certificatesmanagement.UpdateCaBundleRequest,
) (certificatesmanagement.UpdateCaBundleResponse, error) {
	s.updateCalls = append(s.updateCalls, request)
	if s.updateErr != nil {
		return certificatesmanagement.UpdateCaBundleResponse{}, s.updateErr
	}
	for name, bundle := range s.bundles {
		if lo.FromPtr(bundle.Id) == lo.FromPtr(request.CaBundleId) {
			bundle.FreeformTags = request.UpdateCaBundleDetails.FreeformTags
			s.bundles[name] = bundle
			break
		}
	}
	return certificatesmanagement.UpdateCaBundleResponse{}, nil
}

func (s *stubCertificatesManagementClient) DeleteCaBundle(
	_ context.Context,
	request certificatesmanagement.DeleteCaBundleRequest,
) (certificatesmanagement.DeleteCaBundleResponse, error) {
	s.deleteCalls = append(s.deleteCalls, request)
	if s.deleteErr != nil {
		return certificatesmanagement.DeleteCaBundleResponse{}, s.deleteErr
	}
	return certificatesmanagement.DeleteCaBundleResponse{}, nil
}

func TestBackendTLSPolicyModel(t *testing.T) {
	t.Run("resolves multiple ConfigMap CA refs into OCI backend SSL config", func(t *testing.T) {
		fakeData := faker.New()
		namespace := fakeData.Lorem().Word()
		serviceName := fakeData.Lorem().Word()
		compartmentID := "ocid1.compartment.oc1.." + fakeData.UUID().V4()
		lbID := "ocid1.loadbalancer.oc1.." + fakeData.UUID().V4()
		gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "edge"}}
		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: serviceName},
			Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{
				Name:       "tls",
				Port:       8443,
				TargetPort: intstr.FromInt(8443),
			}}},
		}
		options := map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
			BackendTLSOptionHostnameValidation:   backendTLSHostnameValidationDisabled,
			BackendTLSOptionProtocols:            "TLSv1.2,TLSv1.3",
			BackendTLSOptionCipherSuiteName:      "oci-tls-12-ssl-cipher-suite-v3",
			BackendTLSOptionVerifyDepth:          "4",
			BackendTLSOptionSessionResumption:    "true",
			BackendTLSOptionTrustedCABundleOCIDs: "ocid1.cabundle.oc1..existing",
		}
		policy := backendTLSPolicy(namespace, "backend-tls", serviceName, "tls", options, "ca-one", "ca-two")
		caOne := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca-one"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		caTwo := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca-two"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy, &service, &caOne, &caTwo).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		lbClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{
				CompartmentId: &compartmentID,
			}}, nil)
		certsClient := newStubCertificatesManagementClient()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: certsClient,
		})

		sslConfig, err := model.resolveForBackendRef(t.Context(), resolveBackendTLSPolicyParams{
			gateway: gateway,
			config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: lbID}},
			service: service,
			backendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName(serviceName),
					Port: lo.ToPtr(gatewayv1.PortNumber(8443)),
				},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, sslConfig)
		assert.True(t, lo.FromPtr(sslConfig.VerifyPeerCertificate))
		assert.Equal(t, 4, lo.FromPtr(sslConfig.VerifyDepth))
		assert.ElementsMatch(t, []string{"TLSv1.2", "TLSv1.3"}, sslConfig.Protocols)
		assert.Equal(t, "oci-tls-12-ssl-cipher-suite-v3", lo.FromPtr(sslConfig.CipherSuiteName))
		assert.True(t, lo.FromPtr(sslConfig.HasSessionResumption))
		assert.Len(t, certsClient.createCalls, 2)
		assert.Contains(t, sslConfig.TrustedCertificateAuthorityIds, "ocid1.cabundle.oc1..existing")
		assert.Len(t, sslConfig.TrustedCertificateAuthorityIds, 3)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "backend-tls",
		}, &updated))
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionTrue,
			gatewayv1.PolicyReasonAccepted,
		)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionTrue,
			gatewayv1.BackendTLSPolicyReasonResolvedRefs,
		)
	})

	t.Run("rejects policy without explicit OCI hostname validation opt out", func(t *testing.T) {
		fakeData := faker.New()
		namespace := fakeData.Lorem().Word()
		serviceName := fakeData.Lorem().Word()
		backendService := corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: serviceName}}
		policy := backendTLSPolicy(namespace, "backend-tls", serviceName, "", nil, "ca")
		ca := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy, &backendService, &ca).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		_, err := model.resolveForBackendRef(t.Context(), resolveBackendTLSPolicyParams{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "edge"}},
			config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "lb"}},
			service: backendService,
			backendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName(serviceName),
			}},
		})

		require.Error(t, err)
		require.ErrorContains(t, err, BackendTLSOptionHostnameValidation)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "backend-tls",
		}, &updated))
		require.Len(t, updated.Status.Ancestors, 1)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionFalse,
			gatewayv1.PolicyReasonInvalid,
		)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionFalse,
			gatewayv1.PolicyReasonInvalid,
		)
	})
}

func TestBackendTLSPolicyModelValidationAndLifecycle(t *testing.T) {
	fakeData := faker.New()
	namespace := "btls-" + fakeData.Lorem().Word()
	serviceName := "svc-" + fakeData.Lorem().Word()
	compartmentID := "ocid1.compartment.oc1.." + fakeData.UUID().V4()
	lbID := "ocid1.loadbalancer.oc1.." + fakeData.UUID().V4()
	baseOptions := map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
		BackendTLSOptionHostnameValidation: backendTLSHostnameValidationDisabled,
	}
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: serviceName},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "tls", Port: 8443}}},
	}
	gateway := gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "edge"},
		Spec: gatewayv1.GatewaySpec{Infrastructure: &gatewayv1.GatewayInfrastructure{
			ParametersRef: &gatewayv1.LocalParametersReference{
				Group: gatewayv1.Group(types.GroupName),
				Kind:  gatewayv1.Kind(ConfigRefKind),
				Name:  "edge-config",
			},
		}},
	}
	config := types.GatewayConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "edge-config"},
		Spec:       types.GatewayConfigSpec{LoadBalancerID: lbID},
	}

	makeModel := func(
		t *testing.T,
		certsClient *stubCertificatesManagementClient,
		objects ...client.Object,
	) (*backendTLSPolicyModelImpl, *MockociLoadBalancerClient) {
		t.Helper()
		lbClient := NewMockociLoadBalancerClient(t)
		return newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(objects...).
				Build(),
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: certsClient,
		}), lbClient
	}
	resolveParams := resolveBackendTLSPolicyParams{
		gateway: gateway,
		config:  config,
		service: service,
		backendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
			Name: gatewayv1.ObjectName(serviceName),
			Port: lo.ToPtr(gatewayv1.PortNumber(8443)),
		}},
	}

	t.Run("returns sentinel when no policy matches backend service", func(t *testing.T) {
		otherNamespacePolicy := backendTLSPolicy("other", "other-namespace", serviceName, "tls", baseOptions, "ca")
		model, _ := makeModel(t, newStubCertificatesManagementClient(), &service, &otherNamespacePolicy)

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.ErrorIs(t, err, errBackendTLSPolicyNotFound)
	})

	t.Run("lists policies only in backend service namespace", func(t *testing.T) {
		k8sClient := NewMockk8sClient(t)
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		k8sClient.EXPECT().
			List(t.Context(), &gatewayv1.BackendTLSPolicyList{}, mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
				listOptions := &client.ListOptions{}
				for _, opt := range opts {
					opt.ApplyToList(listOptions)
				}
				assert.Equal(t, namespace, listOptions.Namespace)
				reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.BackendTLSPolicy{}))
				return nil
			})

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.ErrorIs(t, err, errBackendTLSPolicyNotFound)
	})

	t.Run("selects oldest policy and marks lower precedence policy conflicted", func(t *testing.T) {
		oldPolicy := backendTLSPolicy(namespace, "old", serviceName, "tls", baseOptions, "ca")
		newPolicy := backendTLSPolicy(namespace, "new", serviceName, "tls", baseOptions, "ca")
		oldPolicy.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
		newPolicy.CreationTimestamp = metav1.Now()
		ca := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&service, &oldPolicy, &newPolicy, &ca).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		lbClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{CompartmentId: &compartmentID},
			}, nil)
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		sslConfig, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.NoError(t, err)
		require.NotNil(t, sslConfig)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "new",
		}, &updated))
		require.Len(t, updated.Status.Ancestors, 1)
		assert.Equal(t, string(gatewayv1.PolicyReasonConflicted), updated.Status.Ancestors[0].Conditions[0].Reason)
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "old",
		}, &updated))
		require.Len(t, updated.Status.Ancestors, 1)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionTrue,
			gatewayv1.PolicyReasonAccepted,
		)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionTrue,
			gatewayv1.BackendTLSPolicyReasonResolvedRefs,
		)
	})

	t.Run("rejects target sectionName that does not exist on service", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "missing-section", serviceName, "missing", baseOptions, "ca")
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&service, &policy).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.Error(t, err)
		require.ErrorContains(t, err, "sectionName")
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "missing-section",
		}, &updated))
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionFalse,
			gatewayv1.PolicyReasonTargetNotFound,
		)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionFalse,
			gatewayv1.PolicyReasonTargetNotFound,
		)
	})

	t.Run("rejects unsupported target kind for backend service", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "bad-target-kind", serviceName, "", baseOptions, "ca")
		policy.Spec.TargetRefs[0].Group = "apps"
		policy.Spec.TargetRefs[0].Kind = "Deployment"
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&service, &policy).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.Error(t, err)
		require.ErrorContains(t, err, "must reference a core Service")
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "bad-target-kind",
		}, &updated))
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.PolicyConditionAccepted,
			metav1.ConditionFalse,
			gatewayv1.BackendTLSPolicyReasonInvalidKind,
		)
		assertBackendTLSPolicyCondition(
			t,
			updated,
			gatewayv1.BackendTLSPolicyConditionResolvedRefs,
			metav1.ConditionFalse,
			gatewayv1.BackendTLSPolicyReasonInvalidKind,
		)
	})

	t.Run("rejects unsupported validation and option shapes", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*gatewayv1.BackendTLSPolicy)
			want   string
		}{
			{
				name: "well known CA certificates",
				mutate: func(policy *gatewayv1.BackendTLSPolicy) {
					wellKnown := gatewayv1.WellKnownCACertificatesType("System")
					policy.Spec.Validation.WellKnownCACertificates = &wellKnown
				},
				want: "wellKnownCACertificates",
			},
			{
				name: "subject alternative names",
				mutate: func(policy *gatewayv1.BackendTLSPolicy) {
					policy.Spec.Validation.SubjectAltNames = []gatewayv1.SubjectAltName{{
						Hostname: "backend.example.com",
					}}
				},
				want: "subjectAltNames",
			},
			{
				name: "unsupported option",
				mutate: func(policy *gatewayv1.BackendTLSPolicy) {
					policy.Spec.Options["example.com/unsupported"] = "true"
				},
				want: "unsupported BackendTLSPolicy option",
			},
			{
				name: "missing CA refs",
				mutate: func(policy *gatewayv1.BackendTLSPolicy) {
					policy.Spec.Validation.CACertificateRefs = nil
				},
				want: "at least one caCertificateRef",
			},
			{
				name: "invalid verify depth",
				mutate: func(policy *gatewayv1.BackendTLSPolicy) {
					policy.Spec.Options[BackendTLSOptionVerifyDepth] = "nope"
				},
				want: BackendTLSOptionVerifyDepth,
			},
			{
				name: "invalid session resumption",
				mutate: func(policy *gatewayv1.BackendTLSPolicy) {
					policy.Spec.Options[BackendTLSOptionSessionResumption] = "maybe"
				},
				want: BackendTLSOptionSessionResumption,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				options := lo.Assign(map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{}, baseOptions)
				policy := backendTLSPolicy(namespace, tt.name, serviceName, "tls", options, "ca")
				tt.mutate(&policy)
				ca := corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
					Data:       map[string]string{"ca.crt": testCAPEM(t)},
				}
				model, _ := makeModel(t, newStubCertificatesManagementClient(), &service, &policy, &ca)

				_, err := model.resolveForBackendRef(t.Context(), resolveParams)

				require.Error(t, err)
				assert.ErrorContains(t, err, tt.want)
			})
		}
	})

	t.Run("rejects invalid CA references", func(t *testing.T) {
		tests := []struct {
			name   string
			policy gatewayv1.BackendTLSPolicy
			ca     *corev1.ConfigMap
			want   string
		}{
			{
				name: "wrong kind",
				policy: func() gatewayv1.BackendTLSPolicy {
					p := backendTLSPolicy(namespace, "wrong-kind", serviceName, "tls", baseOptions, "ca")
					p.Spec.Validation.CACertificateRefs[0].Kind = "Secret"
					return p
				}(),
				want: "must reference a core ConfigMap",
			},
			{
				name:   "missing configmap",
				policy: backendTLSPolicy(namespace, "missing", serviceName, "tls", baseOptions, "ca"),
				want:   "was not found",
			},
			{
				name:   "missing data key",
				policy: backendTLSPolicy(namespace, "missing-key", serviceName, "tls", baseOptions, "ca"),
				ca:     &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"}},
				want:   "missing ca.crt",
			},
			{
				name:   "invalid pem",
				policy: backendTLSPolicy(namespace, "invalid-pem", serviceName, "tls", baseOptions, "ca"),
				ca: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
					Data:       map[string]string{"ca.crt": "not-pem"},
				},
				want: "no CA certificates",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				objects := []client.Object{&service, &tt.policy}
				if tt.ca != nil {
					objects = append(objects, tt.ca)
				}
				model, _ := makeModel(t, newStubCertificatesManagementClient(), objects...)

				_, err := model.resolveForBackendRef(t.Context(), resolveParams)

				require.Error(t, err)
				assert.ErrorContains(t, err, tt.want)
			})
		}
	})

	t.Run("does not create OCI CA bundles until all refs and options are valid", func(t *testing.T) {
		policy := backendTLSPolicy(
			namespace,
			"partial-invalid",
			serviceName,
			"tls",
			baseOptions,
			"ca-one",
			"ca-missing",
		)
		caOne := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca-one"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		certsClient := newStubCertificatesManagementClient()
		model, _ := makeModel(t, certsClient, &service, &policy, &caOne)

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.ErrorContains(t, err, "caCertificateRef ConfigMap")
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("does not create OCI CA bundles when finalizer update fails", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "finalizer-error", serviceName, "tls", baseOptions, "ca")
		ca := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		baseClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&service, &policy, &ca).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		lbClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{CompartmentId: &compartmentID},
			}, nil)
		certsClient := newStubCertificatesManagementClient()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingUpdateClient{k8sClient: baseClient, err: errors.New("update failed")},
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: certsClient,
		})

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.ErrorContains(t, err, "failed to add BackendTLSPolicy finalizer")
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("updates owned OCI CA bundle when PEM changes", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "update", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		certsClient := newStubCertificatesManagementClient()
		bundleID := "ocid1.cabundle.oc1..owned"
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:           &bundleID,
			Name:         &name,
			FreeformTags: backendTLSCABundleTags(policy, "old-hash"),
		}
		model, _ := makeModel(t, certsClient)

		caID, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))

		require.NoError(t, err)
		assert.Equal(t, bundleID, caID)
		assert.Len(t, certsClient.updateCalls, 1)
	})

	t.Run("reuses owned OCI CA bundle when hash matches", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "reuse", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		caPEM := testCAPEM(t)
		certsClient := newStubCertificatesManagementClient()
		bundleID := "ocid1.cabundle.oc1..reuse"
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:           &bundleID,
			Name:         &name,
			FreeformTags: backendTLSCABundleTags(policy, sha256Hex(caPEM)),
		}
		model, _ := makeModel(t, certsClient)

		caID, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, caPEM)

		require.NoError(t, err)
		assert.Equal(t, bundleID, caID)
		assert.Empty(t, certsClient.createCalls)
		assert.Empty(t, certsClient.updateCalls)
	})

	t.Run("waits for existing owned OCI CA bundle to become active", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "creating", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		caPEM := testCAPEM(t)
		certsClient := newStubCertificatesManagementClient()
		bundleID := "ocid1.cabundle.oc1..creating"
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             &bundleID,
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateCreating,
			FreeformTags:   backendTLSCABundleTags(policy, sha256Hex(caPEM)),
		}
		model, _ := makeModel(t, certsClient)

		_, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, caPEM)

		require.ErrorContains(t, err, "not ready")
		assert.Empty(t, certsClient.createCalls)
		assert.Empty(t, certsClient.updateCalls)
	})

	t.Run("waits when newly created OCI CA bundle is not active yet", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "create-wait", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		certsClient := newStubCertificatesManagementClient()
		certsClient.createState = certificatesmanagement.CaBundleLifecycleStateCreating
		model, _ := makeModel(t, certsClient)

		_, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))

		require.ErrorContains(t, err, "not ready")
		assert.Len(t, certsClient.createCalls, 1)
	})

	t.Run("classifies OCI CA bundle lifecycle states for backend TLS", func(t *testing.T) {
		name := "managed-ca"
		require.NoError(t, ensureBackendTLSCABundleUsable(certificatesmanagement.CaBundleSummary{
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
		}))
		require.ErrorContains(t, ensureBackendTLSCABundleUsable(certificatesmanagement.CaBundleSummary{
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleting,
		}), "cannot be reused")
		require.ErrorContains(t, ensureBackendTLSCABundleUsable(certificatesmanagement.CaBundleSummary{
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateFailed,
		}), "not ready")
		require.ErrorContains(t, ensureBackendTLSCABundleUsable(certificatesmanagement.CaBundleSummary{
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateEnum("UNKNOWN"),
		}), "not ready")
	})

	t.Run("uses stable BackendTLSPolicy object identity for OCI CA bundle names", func(t *testing.T) {
		creationTime := metav1.NewTime(time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC))

		assert.Equal(t, "policy-uid", backendTLSPolicyObjectIdentity(gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{UID: apitypes.UID("policy-uid"), CreationTimestamp: creationTime},
		}))
		assert.Equal(
			t,
			creationTime.UTC().Format(time.RFC3339Nano),
			backendTLSPolicyObjectIdentity(gatewayv1.BackendTLSPolicy{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: creationTime, Generation: 7},
			}),
		)
		assert.Equal(t, "7", backendTLSPolicyObjectIdentity(gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{Generation: 7},
		}))
	})

	t.Run("reuses owned OCI CA bundle after create already exists conflict", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "create-conflict", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		caPEM := testCAPEM(t)
		bundleID := "ocid1.cabundle.oc1..conflict"
		certsClient := newStubCertificatesManagementClient()
		certsClient.createErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+name+"' already exists."),
		)
		certsClient.listEmptyResponses = 1
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             &bundleID,
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   backendTLSCABundleTags(policy, sha256Hex(caPEM)),
		}
		model, _ := makeModel(t, certsClient)

		caID, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, caPEM)

		require.NoError(t, err)
		assert.Equal(t, bundleID, caID)
		assert.Len(t, certsClient.createCalls, 1)
		assert.Empty(t, certsClient.updateCalls)
	})

	t.Run("creates usable OCI CA bundle when deleted legacy name still conflicts", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "create-conflict-deleted", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		legacyName := legacyBackendTLSCABundleName(policy, targetRef, ref)
		caPEM := testCAPEM(t)
		deletedID := "ocid1.cabundle.oc1..deleted"
		certsClient := newStubCertificatesManagementClient()
		certsClient.createErrByName[legacyName] = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+legacyName+"' already exists."),
		)
		certsClient.bundles[legacyName] = certificatesmanagement.CaBundleSummary{
			Id:             &deletedID,
			Name:           &legacyName,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleted,
			FreeformTags:   backendTLSCABundleTags(policy, sha256Hex(caPEM)),
		}
		model, _ := makeModel(t, certsClient)

		caID, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, caPEM)

		require.NoError(t, err)
		require.NotEmpty(t, caID)
		require.Len(t, certsClient.createCalls, 1)
		assert.NotEqual(t, legacyName, lo.FromPtr(certsClient.createCalls[0].CreateCaBundleDetails.Name))
	})

	t.Run("rejects unowned OCI CA bundle after create already exists conflict", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "create-conflict-unowned", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		bundleID := "ocid1.cabundle.oc1..unowned"
		certsClient := newStubCertificatesManagementClient()
		certsClient.createErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+name+"' already exists."),
		)
		certsClient.listEmptyResponses = 1
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             &bundleID,
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
		}
		model, _ := makeModel(t, certsClient)

		_, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))

		require.ErrorContains(t, err, "not owned")
		assert.Len(t, certsClient.createCalls, 1)
		assert.Empty(t, certsClient.updateCalls)
	})

	t.Run("updates OCI CA bundle when referenced ConfigMap CA changes", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "rotate", serviceName, "tls", baseOptions, "ca")
		oldPEM := testCAPEM(t)
		newPEM := testCAPEM(t)
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		bundleID := "ocid1.cabundle.oc1..rotate"
		certsClient := newStubCertificatesManagementClient()
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:           &bundleID,
			Name:         &name,
			FreeformTags: backendTLSCABundleTags(policy, sha256Hex(oldPEM)),
		}
		ca := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
			Data:       map[string]string{"ca.crt": newPEM},
		}
		model, lbClient := makeModel(t, certsClient, &service, &policy, &ca)
		lbClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{CompartmentId: &compartmentID},
			}, nil)

		sslConfig, err := model.resolveAcceptedPolicy(t.Context(), backendTLSPolicyCandidate{
			policy:    policy,
			targetRef: targetRef,
		}, resolveParams)

		require.NoError(t, err)
		require.NotNil(t, sslConfig)
		assert.Len(t, certsClient.updateCalls, 1)
		assert.Equal(t, newPEM, lo.FromPtr(certsClient.updateCalls[0].UpdateCaBundleDetails.CaBundlePem))
	})

	t.Run("rejects unowned existing OCI CA bundle", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "unowned", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		name := backendTLSCABundleName(policy, targetRef, ref)
		certsClient := newStubCertificatesManagementClient()
		bundleID := "ocid1.cabundle.oc1..unowned"
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{Id: &bundleID, Name: &name}
		model, _ := makeModel(t, certsClient)

		_, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))

		require.Error(t, err)
		assert.ErrorContains(t, err, "not owned")
	})

	t.Run("cleans up owned OCI CA bundles and removes finalizer", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		certsClient := newStubCertificatesManagementClient()
		ownedID := "ocid1.cabundle.oc1..owned"
		ownedName := "owned"
		certsClient.bundles[ownedName] = certificatesmanagement.CaBundleSummary{
			Id:           &ownedID,
			Name:         &ownedName,
			FreeformTags: backendTLSCABundleTags(policy, "hash"),
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy, &gateway, &config).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		lbClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{CompartmentId: &compartmentID},
			}, nil)
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.NoError(t, err)
		assert.Len(t, certsClient.deleteCalls, 1)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup",
		}, &updated))
		assert.NotContains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
	})

	t.Run("wraps OCI and Kubernetes errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "oci-errors", serviceName, "tls", baseOptions, "ca")
		ca := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		model, lbClient := makeModel(t, newStubCertificatesManagementClient(), &service, &policy, &ca)
		lbClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{}, errors.New("oci unavailable"))

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.Error(t, err)
		assert.ErrorContains(t, err, "failed to get Load Balancer")
	})

	t.Run("covers direct branch helpers and OCI CA errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "branches", serviceName, "tls", baseOptions, "ca")
		targetRef := policy.Spec.TargetRefs[0]
		ref := policy.Spec.Validation.CACertificateRefs[0]
		servicePort := corev1.ServicePort{Name: "tls", Port: 8443}

		_, matched := backendTLSPolicyTargetMatchesService(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "apps",
					Kind:  "Service",
					Name:  gatewayv1.ObjectName(serviceName),
				},
			},
			service,
			resolveParams.backendRef,
		)
		require.False(t, matched)
		_, matched = backendTLSPolicyTargetMatchesService(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  gatewayv1.ObjectName(serviceName),
				},
				SectionName: lo.ToPtr(gatewayv1.SectionName("missing")),
			},
			corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{servicePort}}},
			resolveParams.backendRef,
		)
		require.False(t, matched)
		require.True(t, backendTLSPolicyTargetRefUnsupportedKind(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "apps",
					Kind:  "Deployment",
					Name:  gatewayv1.ObjectName(serviceName),
				},
			},
			service,
		))
		require.False(t, backendTLSPolicyTargetRefUnsupportedKind(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "apps",
					Kind:  "Deployment",
					Name:  gatewayv1.ObjectName("other"),
				},
			},
			service,
		))
		require.False(t, backendTLSPolicyTargetRefMissingServicePort(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  gatewayv1.ObjectName(serviceName),
				},
				SectionName: lo.ToPtr(gatewayv1.SectionName("tls")),
			},
			service,
		))
		require.False(t, backendTLSPolicyTargetRefMissingServicePort(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  gatewayv1.ObjectName(serviceName),
				},
			},
			service,
		))
		require.True(t, backendTLSPolicyTargetRefMissingServicePort(
			gatewayv1.LocalPolicyTargetReferenceWithSectionName{
				LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
					Group: "",
					Kind:  "Service",
					Name:  gatewayv1.ObjectName(serviceName),
				},
				SectionName: lo.ToPtr(gatewayv1.SectionName("missing")),
			},
			service,
		))

		left := backendTLSPolicy(namespace, "a", serviceName, "tls", baseOptions, "ca")
		right := backendTLSPolicy(namespace, "b", serviceName, "tls", baseOptions, "ca")
		left.CreationTimestamp = right.CreationTimestamp
		candidates := []backendTLSPolicyCandidate{{policy: right}, {policy: left}}
		sortBackendTLSPolicyCandidates(candidates)
		require.Equal(t, "a", candidates[0].policy.Name)
		sectionName := backendTLSCABundleName(policy, targetRef, ref)
		wholeServiceTargetRef := targetRef
		wholeServiceTargetRef.SectionName = nil
		wholeServiceName := backendTLSCABundleName(policy, wholeServiceTargetRef, ref)
		require.NotEqual(t, sectionName, wholeServiceName)
		require.Len(t, sectionName, len(backendTLSCABundleNamePrefix)+24)
		require.True(t, strings.HasPrefix(sectionName, backendTLSCABundleNamePrefix))
		require.True(t, parentRefsEqual(
			gatewayv1.ParentReference{Name: "edge"},
			gatewayv1.ParentReference{Name: "edge"},
		))
		require.False(t, parentRefsEqual(
			gatewayv1.ParentReference{Name: "edge"},
			gatewayv1.ParentReference{Name: "other"},
		))

		require.ErrorContains(t, validateCABundlePEM(nonCAPEM(t)), "not a CA certificate")
		require.ErrorContains(t, validateCABundlePEM(testCAPEM(t)+"trailing"), "unexpected trailing data")
		privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("abc")}))
		require.ErrorContains(t, validateCABundlePEM(privateKeyPEM), "unexpected PEM block")
		badCertPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("abc")}))
		require.ErrorContains(t, validateCABundlePEM(badCertPEM), "failed to parse")

		certsClient := newStubCertificatesManagementClient()
		certsClient.listErr = errors.New("list failed")
		model, _ := makeModel(t, certsClient)
		_, err := model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))
		require.ErrorContains(t, err, "failed to list OCI CA bundles")

		certsClient = newStubCertificatesManagementClient()
		certsClient.createErr = errors.New("create failed")
		model, _ = makeModel(t, certsClient)
		_, err = model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))
		require.ErrorContains(t, err, "failed to create OCI CA bundle")

		certsClient = newStubCertificatesManagementClient()
		certsClient.updateErr = errors.New("update failed")
		bundleName := backendTLSCABundleName(policy, targetRef, ref)
		bundleID := "ocid1.cabundle.oc1..updateerr"
		certsClient.bundles[bundleName] = certificatesmanagement.CaBundleSummary{
			Id:           &bundleID,
			Name:         &bundleName,
			FreeformTags: backendTLSCABundleTags(policy, "old"),
		}
		model, _ = makeModel(t, certsClient)
		_, err = model.ensureOCIManagedCABundle(t.Context(), policy, targetRef, ref, compartmentID, testCAPEM(t))
		require.ErrorContains(t, err, "failed to update OCI CA bundle")

		require.NoError(t, model.deleteOwnedCABundles(t.Context(), policy, ""))
		certsClient.deleteErr = errors.New("delete failed")
		err = model.deleteOwnedCABundles(t.Context(), policy, compartmentID)
		require.ErrorContains(t, err, "failed to delete OCI CA bundle")

		certsClient = newStubCertificatesManagementClient()
		unownedID := "ocid1.cabundle.oc1..unownedcleanup"
		unownedName := "unowned"
		certsClient.bundles[unownedName] = certificatesmanagement.CaBundleSummary{
			Id:           &unownedID,
			Name:         &unownedName,
			FreeformTags: map[string]string{backendTLSManagedByTag: "someone-else"},
		}
		model, _ = makeModel(t, certsClient)
		require.NoError(t, model.deleteOwnedCABundles(t.Context(), policy, compartmentID))
		require.Empty(t, certsClient.deleteCalls)

		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		require.NoError(t, model.ensurePolicyFinalizerAndCompartment(t.Context(), policy, ""))

		noFinalizerPolicy := backendTLSPolicy(namespace, "add-finalizer", serviceName, "tls", baseOptions, "ca")
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).WithObjects(&noFinalizerPolicy).Build()
		model = newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		require.NoError(t, model.ensurePolicyFinalizerAndCompartment(t.Context(), noFinalizerPolicy, compartmentID))
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "add-finalizer",
		}, &updated))
		require.Contains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
		require.Equal(t, compartmentID, updated.Annotations[BackendTLSPolicyCompartmentsAnnotation])
	})

	t.Run("rejects missing LB compartment and invalid existing CA bundle option", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "lb-compartment", serviceName, "tls", baseOptions, "ca")
		ca := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ca"},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
		model, lbClient := makeModel(t, newStubCertificatesManagementClient(), &service, &policy, &ca)
		lbClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{}}, nil)

		_, err := model.resolveForBackendRef(t.Context(), resolveParams)

		require.ErrorContains(t, err, "failed to resolve Load Balancer compartment")

		policy = backendTLSPolicy(namespace, "bad-option-ca", serviceName, "tls", lo.Assign(
			map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{},
			baseOptions,
			map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
				BackendTLSOptionTrustedCABundleOCIDs: "ocid1.cabundle.oc1..missing",
			},
		), "ca")
		certsClient := newStubCertificatesManagementClient()
		certsClient.getErrByID["ocid1.cabundle.oc1..missing"] = errors.New("not found")
		model, _ = makeModel(t, certsClient, &service, &policy, &ca)

		_, err = model.resolveForBackendRef(t.Context(), resolveParams)

		require.ErrorContains(t, err, "cannot be resolved")
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("cleanup no-ops without finalizer and wraps list errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-no-finalizer", serviceName, "tls", baseOptions, "ca")
		model, _ := makeModel(t, newStubCertificatesManagementClient(), &policy)
		require.NoError(t, model.cleanupDeletingPolicy(t.Context(), policy))

		model = newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingListClient{err: errors.New("list failed")},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.ErrorContains(t, err, "failed to list Gateways")
	})

	t.Run("cleanup wraps GatewayConfig lookup errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-config-error", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		gatewayWithConfig := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "config-error"},
			Spec: gatewayv1.GatewaySpec{Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "config-error"},
			}},
		}
		baseClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy, &gatewayWithConfig).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: &failingBackendTLSPolicyClient{
				k8sClient: baseClient,
				err:       errors.New("get failed"),
			},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.ErrorContains(t, err, "failed to get GatewayConfig")
	})

	t.Run("cleanup removes finalizer when no gateway references a load balancer", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-no-gateway-lb", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.NoError(t, err)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup-no-gateway-lb",
		}, &updated))
		require.NotContains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
	})

	t.Run("cleanup uses recorded compartments when gateway is gone", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-recorded-compartment", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		policy.Annotations = map[string]string{
			BackendTLSPolicyCompartmentsAnnotation: " " + compartmentID + ", " + compartmentID + " ",
		}
		certsClient := newStubCertificatesManagementClient()
		ownedID := "ocid1.cabundle.oc1..recorded"
		ownedName := "recorded"
		certsClient.bundles[ownedName] = certificatesmanagement.CaBundleSummary{
			Id:           &ownedID,
			Name:         &ownedName,
			FreeformTags: backendTLSCABundleTags(policy, "hash"),
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.NoError(t, err)
		require.Len(t, certsClient.deleteCalls, 1)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup-recorded-compartment",
		}, &updated))
		require.NotContains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
		require.NotContains(t, updated.Annotations, BackendTLSPolicyCompartmentsAnnotation)
	})

	t.Run("cleanup prefers recorded compartments over live load balancer lookup", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-recorded-over-live", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		policy.Annotations = map[string]string{BackendTLSPolicyCompartmentsAnnotation: compartmentID}
		gatewayWithDeletedLB := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "deleted-lb"},
			Spec: gatewayv1.GatewaySpec{Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "deleted-lb-config"},
			}},
		}
		deletedLBConfig := types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "deleted-lb-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "deleted-lb"},
		}
		certsClient := newStubCertificatesManagementClient()
		ownedID := "ocid1.cabundle.oc1..recordedlive"
		ownedName := "recorded-live"
		certsClient.bundles[ownedName] = certificatesmanagement.CaBundleSummary{
			Id:           &ownedID,
			Name:         &ownedName,
			FreeformTags: backendTLSCABundleTags(policy, "hash"),
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy, &gatewayWithDeletedLB, &deletedLBConfig).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.NoError(t, err)
		require.Len(t, certsClient.deleteCalls, 1)
	})

	t.Run("cleanup ignores deleted owned OCI CA bundles", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-deleted-bundle", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		policy.Annotations = map[string]string{BackendTLSPolicyCompartmentsAnnotation: compartmentID}
		certsClient := newStubCertificatesManagementClient()
		ownedID := "ocid1.cabundle.oc1..deleted"
		ownedName := "deleted"
		certsClient.bundles[ownedName] = certificatesmanagement.CaBundleSummary{
			Id:             &ownedID,
			Name:           &ownedName,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleted,
			FreeformTags:   backendTLSCABundleTags(policy, "hash"),
		}
		deletingID := "ocid1.cabundle.oc1..deleting"
		deletingName := "deleting"
		certsClient.bundles[deletingName] = certificatesmanagement.CaBundleSummary{
			Id:             &deletingID,
			Name:           &deletingName,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleting,
			FreeformTags:   backendTLSCABundleTags(policy, "hash"),
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.NoError(t, err)
		assert.Empty(t, certsClient.deleteCalls)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup-deleted-bundle",
		}, &updated))
		require.NotContains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
	})

	t.Run("cleanup treats missing OCI CA bundle as already deleted", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-missing-bundle", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		policy.Annotations = map[string]string{BackendTLSPolicyCompartmentsAnnotation: compartmentID}
		certsClient := newStubCertificatesManagementClient()
		ownedID := "ocid1.cabundle.oc1..missing"
		ownedName := "missing"
		certsClient.bundles[ownedName] = certificatesmanagement.CaBundleSummary{
			Id:           &ownedID,
			Name:         &ownedName,
			FreeformTags: backendTLSCABundleTags(policy, "hash"),
		}
		certsClient.deleteErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound),
			ociapi.RandomServiceErrorWithCode("NotAuthorizedOrNotFound"),
		)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.NoError(t, err)
		assert.Len(t, certsClient.deleteCalls, 1)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup-missing-bundle",
		}, &updated))
		require.NotContains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
	})

	t.Run("cleanup keeps finalizer when OCI CA bundle is still associated", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-associated-bundle", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		policy.Annotations = map[string]string{BackendTLSPolicyCompartmentsAnnotation: compartmentID}
		certsClient := newStubCertificatesManagementClient()
		ownedID := "ocid1.cabundle.oc1..associated"
		ownedName := "associated"
		certsClient.bundles[ownedName] = certificatesmanagement.CaBundleSummary{
			Id:           &ownedID,
			Name:         &ownedName,
			FreeformTags: backendTLSCABundleTags(policy, "hash"),
		}
		certsClient.deleteErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
			ociapi.RandomServiceErrorWithCode("IncorrectState"),
			ociapi.RandomServiceErrorWithMessage(
				"A dependency exists between the child entity Association and the parent entity "+ownedID+".",
			),
		)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.ErrorIs(t, err, errBackendTLSCABundleStillAssociated)
		assert.Len(t, certsClient.deleteCalls, 1)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup-associated-bundle",
		}, &updated))
		require.Contains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
	})

	t.Run("cleanup skips incomplete gateway state and returns CA delete errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-branches", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		gatewayNoInfra := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "no-infra"}}
		gatewayMissingConfig := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "missing-config"},
			Spec: gatewayv1.GatewaySpec{Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "missing"},
			}},
		}
		gatewayDeleteError := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "delete-error"},
			Spec: gatewayv1.GatewaySpec{Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "delete-error-config"},
			}},
		}
		deleteErrorConfig := types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "delete-error-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "delete-error"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(
				&policy,
				&gatewayNoInfra,
				&gatewayMissingConfig,
				&gatewayDeleteError,
				&deleteErrorConfig,
			).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		lbClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.MatchedBy(func(request loadbalancer.GetLoadBalancerRequest) bool {
				return lo.FromPtr(request.LoadBalancerId) == "delete-error"
			})).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{CompartmentId: &compartmentID},
			}, nil)
		certsClient := newStubCertificatesManagementClient()
		certsClient.listErr = errors.New("ca list failed")
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: certsClient,
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.ErrorContains(t, err, "failed to list OCI CA bundles")
	})

	t.Run("cleanup returns load balancer lookup errors and keeps finalizer", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-lb-error", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		gatewayLBError := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "lb-error"},
			Spec: gatewayv1.GatewaySpec{Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "lb-error-config"},
			}},
		}
		lbErrorConfig := types.GatewayConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "lb-error-config"},
			Spec:       types.GatewayConfigSpec{LoadBalancerID: "lb-error"},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy, &gatewayLBError, &lbErrorConfig).
			Build()
		lbClient := NewMockociLoadBalancerClient(t)
		lbClient.EXPECT().
			GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{}, errors.New("lb get failed"))
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     lbClient,
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.ErrorContains(t, err, "failed to get Load Balancer lb-error for BackendTLSPolicy cleanup")
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "cleanup-lb-error",
		}, &updated))
		require.Contains(t, updated.Finalizers, BackendTLSPolicyProgrammedFinalizer)
	})

	t.Run("cleanup wraps finalizer removal errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "cleanup-update-error", serviceName, "tls", baseOptions, "ca")
		policy.Finalizers = []string{BackendTLSPolicyProgrammedFinalizer}
		baseClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).WithObjects(&policy).Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingUpdateClient{k8sClient: baseClient, err: errors.New("update failed")},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		err := model.cleanupDeletingPolicy(t.Context(), policy)

		require.ErrorContains(t, err, "failed to remove BackendTLSPolicy finalizer")
	})

	t.Run("setPolicyCondition updates existing ancestor status", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "existing-status", serviceName, "tls", baseOptions, "ca")
		gatewayNamespace := gatewayv1.Namespace(gateway.Namespace)
		policy.Status.Ancestors = []gatewayv1.PolicyAncestorStatus{{
			AncestorRef: gatewayv1.ParentReference{
				Group:     lo.ToPtr(gatewayv1.Group(gatewayv1.GroupName)),
				Kind:      lo.ToPtr(gatewayv1.Kind("Gateway")),
				Namespace: &gatewayNamespace,
				Name:      gatewayv1.ObjectName(gateway.Name),
			},
			ControllerName: gatewayv1.GatewayController(ControllerClassName),
			Conditions: []metav1.Condition{{
				Type:   string(gatewayv1.PolicyConditionAccepted),
				Status: metav1.ConditionFalse,
				Reason: string(gatewayv1.PolicyReasonInvalid),
			}},
		}}
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithObjects(&policy).
			WithStatusSubresource(&gatewayv1.BackendTLSPolicy{}).
			Build()
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		err := model.setPolicyCondition(
			t.Context(),
			policy,
			gateway,
			metav1.ConditionTrue,
			gatewayv1.PolicyReasonAccepted,
			"accepted",
		)

		require.NoError(t, err)
		var updated gatewayv1.BackendTLSPolicy
		require.NoError(t, k8sClient.Get(t.Context(), apitypes.NamespacedName{
			Namespace: namespace,
			Name:      "existing-status",
		}, &updated))
		require.Len(t, updated.Status.Ancestors, 1)
		require.Equal(t, metav1.ConditionTrue, updated.Status.Ancestors[0].Conditions[0].Status)
	})

	t.Run("setPolicyCondition skips status update when condition is unchanged", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "unchanged-status", serviceName, "tls", baseOptions, "ca")
		policy.Generation = 7
		gatewayNamespace := gatewayv1.Namespace(gateway.Namespace)
		policy.Status.Ancestors = []gatewayv1.PolicyAncestorStatus{{
			AncestorRef: gatewayv1.ParentReference{
				Group:     lo.ToPtr(gatewayv1.Group(gatewayv1.GroupName)),
				Kind:      lo.ToPtr(gatewayv1.Kind("Gateway")),
				Namespace: &gatewayNamespace,
				Name:      gatewayv1.ObjectName(gateway.Name),
			},
			ControllerName: gatewayv1.GatewayController(ControllerClassName),
			Conditions: []metav1.Condition{{
				Type:               string(gatewayv1.PolicyConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.PolicyReasonAccepted),
				Message:            "accepted",
				ObservedGeneration: policy.Generation,
				LastTransitionTime: metav1.Now(),
			}},
		}}
		k8sClient := NewMockk8sClient(t)
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 k8sClient,
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		k8sClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: namespace, Name: "unchanged-status"}, mock.Anything).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.BackendTLSPolicy) = policy
				return nil
			})

		err := model.setPolicyCondition(
			t.Context(),
			policy,
			gateway,
			metav1.ConditionTrue,
			gatewayv1.PolicyReasonAccepted,
			"accepted",
		)

		require.NoError(t, err)
	})

	t.Run("wraps direct Kubernetes write and lookup errors", func(t *testing.T) {
		policy := backendTLSPolicy(namespace, "k8s-errors", serviceName, "tls", baseOptions, "ca")
		backendService := corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: serviceName}}
		model := newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingListClient{err: errors.New("policy list failed")},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})

		_, err := model.resolveForBackendRef(t.Context(), resolveBackendTLSPolicyParams{
			gateway: gateway,
			config:  config,
			service: backendService,
			backendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName(serviceName),
			}},
		})
		require.ErrorContains(t, err, "failed to list BackendTLSPolicies")

		model = newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingUpdateClient{err: errors.New("update failed")},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		err = model.ensurePolicyFinalizerAndCompartment(t.Context(), policy, compartmentID)
		require.ErrorContains(t, err, "failed to add BackendTLSPolicy finalizer")

		model = newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingBackendTLSPolicyClient{err: errors.New("get failed")},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		err = model.setPolicyCondition(
			t.Context(),
			policy,
			gateway,
			metav1.ConditionTrue,
			gatewayv1.PolicyReasonAccepted,
			"accepted",
		)
		require.ErrorContains(t, err, "failed to get BackendTLSPolicy")

		model = newBackendTLSPolicyModel(backendTLSPolicyModelDeps{
			RootLogger:                diag.RootTestLogger(),
			K8sClient:                 &failingBackendTLSPolicyClient{err: errors.New("configmap get failed")},
			OciLoadBalancerClient:     NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: newStubCertificatesManagementClient(),
		})
		_, err = model.resolveCACertificateRefPEM(
			t.Context(),
			policy,
			gatewayv1.LocalObjectReference{Group: "", Kind: "ConfigMap", Name: "ca"},
		)
		require.ErrorContains(t, err, "failed to get caCertificateRef ConfigMap")
	})
}

func backendTLSPolicy(
	namespace string,
	name string,
	serviceName string,
	sectionName string,
	options map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue,
	caRefs ...string,
) gatewayv1.BackendTLSPolicy {
	targetRef := gatewayv1.LocalPolicyTargetReferenceWithSectionName{
		LocalPolicyTargetReference: gatewayv1.LocalPolicyTargetReference{
			Group: "",
			Kind:  "Service",
			Name:  gatewayv1.ObjectName(serviceName),
		},
	}
	if sectionName != "" {
		targetSectionName := gatewayv1.SectionName(sectionName)
		targetRef.SectionName = &targetSectionName
	}
	refs := lo.Map(caRefs, func(name string, _ int) gatewayv1.LocalObjectReference {
		return gatewayv1.LocalObjectReference{Group: "", Kind: "ConfigMap", Name: gatewayv1.ObjectName(name)}
	})
	return gatewayv1.BackendTLSPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: metav1.Now(),
		},
		Spec: gatewayv1.BackendTLSPolicySpec{
			TargetRefs: []gatewayv1.LocalPolicyTargetReferenceWithSectionName{targetRef},
			Validation: gatewayv1.BackendTLSPolicyValidation{
				Hostname:          "backend.example.com",
				CACertificateRefs: refs,
			},
			Options: options,
		},
	}
}

func assertBackendTLSPolicyCondition(
	t *testing.T,
	policy gatewayv1.BackendTLSPolicy,
	conditionType gatewayv1.PolicyConditionType,
	status metav1.ConditionStatus,
	reason gatewayv1.PolicyConditionReason,
) {
	t.Helper()

	require.Len(t, policy.Status.Ancestors, 1)
	condition := meta.FindStatusCondition(policy.Status.Ancestors[0].Conditions, string(conditionType))
	require.NotNil(t, condition)
	assert.Equal(t, status, condition.Status)
	assert.Equal(t, string(reason), condition.Reason)
}

func legacyBackendTLSCABundleName(
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

func testCAPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func nonCAPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "not-ca"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

type failingListClient struct {
	k8sClient

	err error
}

func (c *failingListClient) List(
	_ context.Context,
	_ client.ObjectList,
	_ ...client.ListOption,
) error {
	return c.err
}

type failingUpdateClient struct {
	k8sClient

	err error
}

func (c *failingUpdateClient) Update(
	_ context.Context,
	_ client.Object,
	_ ...client.UpdateOption,
) error {
	return c.err
}
