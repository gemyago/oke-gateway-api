package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

func TestFrontendMTLSModel(t *testing.T) {
	makeGateway := func(t *testing.T, port gatewayv1.PortNumber, validation *gatewayv1.FrontendTLSValidation) gatewayv1.Gateway {
		t.Helper()
		fakeData := faker.New()
		mode := gatewayv1.TLSModeTerminate
		return gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fakeData.Lorem().Word(),
				Name:       "gw-" + fakeData.Lorem().Word(),
				Generation: 1,
				Annotations: map[string]string{
					ControllerClassName: "true",
				},
			},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     port,
					TLS: &gatewayv1.ListenerTLSConfig{
						Mode: &mode,
					},
				}},
				TLS: &gatewayv1.GatewayTLSConfig{
					Frontend: &gatewayv1.FrontendTLSConfig{
						Default: gatewayv1.TLSConfig{Validation: validation},
					},
				},
			},
		}
	}
	makeConfigMap := func(t *testing.T, namespace string, name gatewayv1.ObjectName) corev1.ConfigMap {
		t.Helper()
		return corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: string(name)},
			Data:       map[string]string{"ca.crt": testCAPEM(t)},
		}
	}
	makeModel := func(t *testing.T, objects ...client.Object) (*ociLoadBalancerModelImpl, *stubCertificatesManagementClient) {
		t.Helper()
		k8sObjects := make([]client.Object, 0, len(objects))
		k8sObjects = append(k8sObjects, objects...)
		certsClient := newStubCertificatesManagementClient()
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(k8sObjects...).
				Build(),
			OciClient:                 NewMockociLoadBalancerClient(t),
			OciCertificatesMgmtClient: certsClient,
			WorkRequestsWatcher:       NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:        NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		return model, certsClient
	}
	defaultCABundleName := func(gateway gatewayv1.Gateway) string {
		return frontendMTLSCABundleName(
			gateway,
			443,
			gateway.Spec.TLS.Frontend.Default.Validation.CACertificateRefs[0],
		)
	}

	t.Run("applies standard ConfigMap CA refs to listener SSL configuration", func(t *testing.T) {
		refName := gatewayv1.ObjectName("ca-" + faker.New().Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group: "",
				Kind:  "ConfigMap",
				Name:  refName,
			}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		sslConfig := &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")}

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + faker.New().Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, sslConfig)

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.True(t, lo.FromPtr(got.VerifyPeerCertificate))
		assert.Equal(t, defaultFrontendMTLSVerifyDepth, lo.FromPtr(got.VerifyDepth))
		require.Len(t, got.TrustedCertificateAuthorityIds, 1)
		assert.Len(t, certsClient.createCalls, 1)
		assert.Equal(t,
			defaultCABundleName(gateway),
			lo.FromPtr(certsClient.createCalls[0].CreateCaBundleDetails.Name),
		)
		assert.Contains(t, got.TrustedCertificateAuthorityIds[0], "ocid1.cabundle.oc1..created")
	})

	t.Run("per-port validation and annotations override defaults", func(t *testing.T) {
		fakeData := faker.New()
		defaultRef := gatewayv1.ObjectName("default-" + fakeData.Lorem().Word())
		portRef := gatewayv1.ObjectName("port-" + fakeData.Lorem().Word())
		port := gatewayv1.PortNumber(8443)
		gateway := makeGateway(t, port, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: defaultRef}},
		})
		gateway.Spec.TLS.Frontend.PerPort = []gatewayv1.TLSPortConfig{{
			Port: port,
			TLS: gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{
				CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: portRef}},
			}},
		}}
		gateway.Annotations[frontendMTLSPortVerifyDepthAnnotation(port)] = "5"
		configMap := makeConfigMap(t, gateway.Namespace, portRef)
		model, certsClient := makeModel(t, &gateway, &configMap)

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")})

		require.NoError(t, err)
		assert.Equal(t, 5, lo.FromPtr(got.VerifyDepth))
		assert.Len(t, got.TrustedCertificateAuthorityIds, 1)
		require.Len(t, certsClient.createCalls, 1)
		assert.Equal(t,
			frontendMTLSCABundleName(
				gateway,
				port,
				gateway.Spec.TLS.Frontend.PerPort[0].TLS.Validation.CACertificateRefs[0],
			),
			lo.FromPtr(certsClient.createCalls[0].CreateCaBundleDetails.Name),
		)
	})

	t.Run("uses existing OCI CA bundle OCIDs without creating bundles", func(t *testing.T) {
		fakeData := faker.New()
		ociCAID := "ocid1.cabundle.oc1.." + fakeData.UUID().V4()
		gateway := makeGateway(t, 443, nil)
		gateway.Spec.TLS = nil
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] = ociCAID
		gateway.Annotations[FrontendMTLSVerifyDepthAnnotation] = "4"
		model, certsClient := makeModel(t, &gateway)

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")})

		require.NoError(t, err)
		assert.Equal(t, []string{ociCAID}, got.TrustedCertificateAuthorityIds)
		assert.Equal(t, 4, lo.FromPtr(got.VerifyDepth))
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("per-port OCI CA bundle OCID annotations override defaults", func(t *testing.T) {
		fakeData := faker.New()
		defaultCAID := "ocid1.cabundle.oc1..default" + fakeData.UUID().V4()
		portCAID := "ocid1.cabundle.oc1..port" + fakeData.UUID().V4()
		gateway := makeGateway(t, 8443, nil)
		gateway.Spec.TLS = nil
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] = defaultCAID
		gateway.Annotations[frontendMTLSPortTrustedCABundleOCIDsAnnotation(8443)] = portCAID
		model, certsClient := makeModel(t, &gateway)

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, nil)

		require.NoError(t, err)
		assert.Equal(t, []string{portCAID}, got.TrustedCertificateAuthorityIds)
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("returns existing config when frontend mTLS is not configured", func(t *testing.T) {
		gateway := makeGateway(t, 443, nil)
		sslConfig := &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")}
		model, certsClient := makeModel(t, &gateway)

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, sslConfig)

		require.NoError(t, err)
		assert.Same(t, sslConfig, got)
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("returns existing config without listener TLS or Gateway context", func(t *testing.T) {
		gateway := makeGateway(t, 443, nil)
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] =
			"ocid1.cabundle.oc1.." + faker.New().UUID().V4()
		listenerWithoutTLS := gateway.Spec.Listeners[0]
		listenerWithoutTLS.TLS = nil
		sslConfig := &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")}
		model, certsClient := makeModel(t, &gateway)

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &listenerWithoutTLS,
			gateway:      &gateway,
		}, sslConfig)

		require.NoError(t, err)
		assert.Same(t, sslConfig, got)

		got, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
		}, sslConfig)

		require.NoError(t, err)
		assert.Same(t, sslConfig, got)
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("rejects frontend mTLS on non TLS listener protocols", func(t *testing.T) {
		gateway := makeGateway(t, 443, nil)
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] =
			"ocid1.cabundle.oc1.." + faker.New().UUID().V4()
		gateway.Spec.Listeners[0].Protocol = gatewayv1.HTTPProtocolType
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "HTTPS or TLS listeners")
	})

	t.Run("requires certificates management client when configured", func(t *testing.T) {
		gateway := makeGateway(t, 443, nil)
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] =
			"ocid1.cabundle.oc1.." + faker.New().UUID().V4()
		model, _ := makeModel(t, &gateway)
		model.certsClient = nil

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "certificates management client")
	})

	t.Run("rejects unsupported and invalid frontend mTLS settings", func(t *testing.T) {
		fakeData := faker.New()
		mode := gatewayv1.AllowInsecureFallback
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			Mode: mode,
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group: "",
				Kind:  "ConfigMap",
				Name:  gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word()),
			}},
		})
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")})

		require.ErrorContains(t, err, "AllowInsecureFallback")
		var statusErr *resourceStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
	})

	t.Run("rejects unknown frontend mTLS mode", func(t *testing.T) {
		mode := gatewayv1.FrontendValidationModeType("AuditOnly")
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			Mode: mode,
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group: "",
				Kind:  "ConfigMap",
				Name:  "ca-" + gatewayv1.ObjectName(faker.New().Lorem().Word()),
			}},
		})
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "AuditOnly")
	})

	t.Run("rejects mixed standard refs and OCI CA bundle OCIDs", func(t *testing.T) {
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group: "",
				Kind:  "ConfigMap",
				Name:  "ca-" + gatewayv1.ObjectName(faker.New().Lorem().Word()),
			}},
		})
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] =
			"ocid1.cabundle.oc1.." + faker.New().UUID().V4()
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "cannot mix")
	})

	t.Run("rejects validation without any trust anchor", func(t *testing.T) {
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{})
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "requires at least one")
	})

	t.Run("rejects invalid verify depth annotation", func(t *testing.T) {
		gateway := makeGateway(t, 443, nil)
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] =
			"ocid1.cabundle.oc1.." + faker.New().UUID().V4()
		gateway.Annotations[FrontendMTLSVerifyDepthAnnotation] = "zero"
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "positive integer")
	})

	t.Run("rejects unresolved OCI CA bundle OCID", func(t *testing.T) {
		fakeData := faker.New()
		ociCAID := "ocid1.cabundle.oc1.." + fakeData.UUID().V4()
		gateway := makeGateway(t, 443, nil)
		gateway.Spec.TLS = nil
		gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] = ociCAID
		model, certsClient := makeModel(t, &gateway)
		certsClient.getErrByID[ociCAID] = errors.New("missing")

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "cannot be resolved")
	})

	t.Run("rejects invalid ConfigMap refs and CA data", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group: gatewayv1.Group("example.com"),
				Kind:  "Secret",
				Name:  gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word()),
			}},
		})
		model, _ := makeModel(t, &gateway)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})

		require.ErrorContains(t, err, "core ConfigMap")

		gateway.Spec.TLS.Frontend.Default.Validation.CACertificateRefs[0] = gatewayv1.ObjectReference{
			Group: "", Kind: "ConfigMap", Name: gatewayv1.ObjectName("missing-" + fakeData.Lorem().Word()),
		}
		model, _ = makeModel(t, &gateway)
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})
		require.ErrorContains(t, err, "was not found")

		refName := gatewayv1.ObjectName("empty-" + fakeData.Lorem().Word())
		gateway.Spec.TLS.Frontend.Default.Validation.CACertificateRefs[0].Name = refName
		emptyConfigMap := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: gateway.Namespace, Name: string(refName)},
		}
		model, _ = makeModel(t, &gateway, &emptyConfigMap)
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})
		require.ErrorContains(t, err, "missing ca.crt")

		invalidConfigMap := emptyConfigMap
		invalidConfigMap.Data = map[string]string{"ca.crt": "not pem"}
		model, _ = makeModel(t, &gateway, &invalidConfigMap)
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			listenerSpec: &gateway.Spec.Listeners[0],
			gateway:      &gateway,
		}, &loadbalancer.SslConfigurationDetails{})
		require.ErrorContains(t, err, "invalid ca.crt")
	})

	t.Run("enforces ReferenceGrant for cross namespace ConfigMap refs", func(t *testing.T) {
		fakeData := faker.New()
		refNamespace := "ca-" + fakeData.Lorem().Word()
		refName := gatewayv1.ObjectName("bundle-" + fakeData.Lorem().Word())
		refNamespaceTyped := gatewayv1.Namespace(refNamespace)
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group:     "",
				Kind:      "ConfigMap",
				Name:      refName,
				Namespace: &refNamespaceTyped,
			}},
		})
		configMap := makeConfigMap(t, refNamespace, refName)
		model, _ := makeModel(t, &gateway, &configMap)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")})

		require.ErrorContains(t, err, "ReferenceGrant")
		var statusErr *resourceStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, string(gatewayv1.GatewayReasonRefNotPermitted), statusErr.reason)

		grant := gatewayv1beta1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{Namespace: refNamespace, Name: "grant-" + fakeData.Lorem().Word()},
			Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{{
					Group:     gatewayv1.Group(gatewayAPIGroup),
					Kind:      gatewayv1.Kind("Gateway"),
					Namespace: gatewayv1.Namespace(gateway.Namespace),
				}},
				To: []gatewayv1beta1.ReferenceGrantTo{{
					Group: "",
					Kind:  gatewayv1.Kind("ConfigMap"),
				}},
			},
		}
		model, _ = makeModel(t, &gateway, &configMap, &grant)

		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, &loadbalancer.SslConfigurationDetails{CertificateName: new("cert")})

		require.NoError(t, err)
	})

	t.Run("cleans up stale controller owned bundles but keeps desired and unowned bundles", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 443, nil)
		compartmentID := "compartment-" + fakeData.Lorem().Word()
		desiredName := "desired-" + fakeData.Lorem().Word()
		staleName := "stale-" + fakeData.Lorem().Word()
		unownedName := "unowned-" + fakeData.Lorem().Word()
		model, certsClient := makeModel(t, &gateway)
		certsClient.bundles[desiredName] = certificatesmanagement.CaBundleSummary{
			Id:             new("desired-id"),
			Name:           new(desiredName),
			CompartmentId:  new(compartmentID),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}
		certsClient.bundles[staleName] = certificatesmanagement.CaBundleSummary{
			Id:             new("stale-id"),
			Name:           new(staleName),
			CompartmentId:  new(compartmentID),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}
		certsClient.bundles[unownedName] = certificatesmanagement.CaBundleSummary{
			Id:             new("unowned-id"),
			Name:           new(unownedName),
			CompartmentId:  new(compartmentID),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
		}

		err := model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{
			gateway:       &gateway,
			compartmentID: compartmentID,
			desiredBundleNames: map[string]struct{}{
				desiredName: {},
			},
		})

		require.NoError(t, err)
		require.Len(t, certsClient.deleteCalls, 1)
		assert.Equal(t, "stale-id", lo.FromPtr(certsClient.deleteCalls[0].CaBundleId))
	})

	t.Run("reuses and updates owned OCI CA bundles", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		name := defaultCABundleName(gateway)
		id := "ocid1.cabundle.oc1.." + fakeData.UUID().V4()
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             &id,
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "stale-hash"),
		}

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)

		require.NoError(t, err)
		assert.Equal(t, []string{id}, got.TrustedCertificateAuthorityIds)
		require.Len(t, certsClient.updateCalls, 1)
		assert.Equal(t, id, lo.FromPtr(certsClient.updateCalls[0].CaBundleId))
		assert.Empty(t, certsClient.createCalls)
	})

	t.Run("rejects existing unowned or unusable OCI CA bundle", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		name := defaultCABundleName(gateway)
		model, certsClient := makeModel(t, &gateway, &configMap)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("ocid1.cabundle.oc1.." + fakeData.UUID().V4()),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
		}

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)

		require.ErrorContains(t, err, "not owned")

		model, certsClient = makeModel(t, &gateway, &configMap)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("ocid1.cabundle.oc1.." + fakeData.UUID().V4()),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleting,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, sha256Hex(configMap.Data["ca.crt"])),
		}
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)
		require.ErrorContains(t, err, "cannot be reused")
	})

	t.Run("resolves create conflict by reusing existing matching bundle", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		compartmentID := "compartment-" + fakeData.Lorem().Word()
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		name := defaultCABundleName(gateway)
		id := "ocid1.cabundle.oc1.." + fakeData.UUID().V4()
		certsClient.listEmptyResponses = 1
		certsClient.createErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+name+"' already exists."),
		)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             &id,
			Name:           &name,
			CompartmentId:  &compartmentID,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, sha256Hex(configMap.Data["ca.crt"])),
		}

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: compartmentID,
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)

		require.NoError(t, err)
		assert.Equal(t, []string{id}, got.TrustedCertificateAuthorityIds)
	})

	t.Run("surfaces create conflict lookup failures", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		name := defaultCABundleName(gateway)
		certsClient.listEmptyResponses = 2
		certsClient.createErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+name+"' already exists."),
		)

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)

		require.ErrorContains(t, err, "was not visible")

		model, certsClient = makeModel(t, &gateway, &configMap)
		certsClient.listEmptyResponses = 1
		certsClient.createErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+name+"' already exists."),
		)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("ocid1.cabundle.oc1.." + fakeData.UUID().V4()),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
		}
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)
		require.ErrorContains(t, err, "not owned")

		model, certsClient = makeModel(t, &gateway, &configMap)
		certsClient.listEmptyResponses = 1
		certsClient.createErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusBadRequest),
			ociapi.RandomServiceErrorWithCode("InvalidParameter"),
			ociapi.RandomServiceErrorWithMessage("A CA bundle with the name '"+name+"' already exists."),
		)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("ocid1.cabundle.oc1.." + fakeData.UUID().V4()),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "stale-hash"),
		}
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)
		require.ErrorContains(t, err, "stale CA data")
	})

	t.Run("surfaces CA bundle create and lookup failures", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		certsClient.listErr = errors.New("list failed")

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)
		require.ErrorContains(t, err, "failed to list OCI CA bundles")

		model, certsClient = makeModel(t, &gateway, &configMap)
		certsClient.createState = certificatesmanagement.CaBundleLifecycleStateCreating
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)
		require.ErrorContains(t, err, "is CREATING")

		model, certsClient = makeModel(t, &gateway, &configMap)
		certsClient.createErr = errors.New("create failed")
		_, err = model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)
		require.ErrorContains(t, err, "failed to create OCI CA bundle")
	})

	t.Run("ignores deleted named bundle before creating replacement", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		name := defaultCABundleName(gateway)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("deleted-id"),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleted,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, sha256Hex(configMap.Data["ca.crt"])),
		}

		got, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)

		require.NoError(t, err)
		assert.Contains(t, got.TrustedCertificateAuthorityIds[0], "ocid1.cabundle.oc1..created")
		require.Len(t, certsClient.createCalls, 1)
	})

	t.Run("surfaces CA bundle update failures", func(t *testing.T) {
		fakeData := faker.New()
		refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
		gateway := makeGateway(t, 443, &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{Group: "", Kind: "ConfigMap", Name: refName}},
		})
		configMap := makeConfigMap(t, gateway.Namespace, refName)
		model, certsClient := makeModel(t, &gateway, &configMap)
		name := defaultCABundleName(gateway)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("ocid1.cabundle.oc1.." + fakeData.UUID().V4()),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "stale-hash"),
		}
		certsClient.updateErr = errors.New("update failed")

		_, err := model.applyFrontendMTLS(t.Context(), reconcileHTTPListenerParams{
			loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			listenerSpec:              &gateway.Spec.Listeners[0],
			gateway:                   &gateway,
		}, nil)

		require.ErrorContains(t, err, "failed to update OCI CA bundle")
	})

	t.Run("cleans up across recorded compartments and ignores deleted bundle states", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 443, nil)
		gateway.Annotations[GatewayFrontendMTLSCABundleCompartmentsAnnotation] = "comp-a, ,comp-b,comp-a"
		model, certsClient := makeModel(t, &gateway)
		certsClient.bundles["deleting-"+fakeData.Lorem().Word()] = certificatesmanagement.CaBundleSummary{
			Id:             new("deleting-id"),
			Name:           new("deleting"),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleting,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}
		certsClient.bundles["deleted-"+fakeData.Lorem().Word()] = certificatesmanagement.CaBundleSummary{
			Id:             new("deleted-id"),
			Name:           new("deleted"),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleted,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}

		err := model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{
			gateway:            &gateway,
			compartmentID:      "compartment-" + fakeData.Lorem().Word(),
			desiredBundleNames: map[string]struct{}{},
		})

		require.NoError(t, err)
		assert.Empty(t, certsClient.deleteCalls)
		assert.Equal(t, []string{"comp-a", "comp-b"}, frontendMTLSCompartmentIDs(gateway))
	})

	t.Run("cleanup handles no-op and error cases", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 443, nil)
		var certsClient *stubCertificatesManagementClient
		model, _ := makeModel(t, &gateway)

		require.NoError(t, model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{}))
		model.certsClient = nil
		require.NoError(t, model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{
			gateway:       &gateway,
			compartmentID: "compartment-" + fakeData.Lorem().Word(),
		}))

		model, certsClient = makeModel(t, &gateway)
		certsClient.listErr = errors.New("list failed")
		err := model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{
			gateway:       &gateway,
			compartmentID: "compartment-" + fakeData.Lorem().Word(),
		})
		require.ErrorContains(t, err, "failed to list OCI CA bundles")

		model, certsClient = makeModel(t, &gateway)
		staleName := "stale-" + fakeData.Lorem().Word()
		certsClient.bundles[staleName] = certificatesmanagement.CaBundleSummary{
			Id:             new("stale-id"),
			Name:           new(staleName),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}
		certsClient.deleteErr = ociapi.NewRandomServiceError(
			ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound),
		)
		require.NoError(t, model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{
			gateway:       &gateway,
			compartmentID: "compartment-" + fakeData.Lorem().Word(),
		}))

		model, certsClient = makeModel(t, &gateway)
		certsClient.bundles[staleName] = certificatesmanagement.CaBundleSummary{
			Id:             new("stale-id"),
			Name:           new(staleName),
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateActive,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}
		certsClient.deleteErr = errors.New("delete failed")
		err = model.cleanupFrontendMTLSCABundles(t.Context(), cleanupFrontendMTLSCABundlesParams{
			gateway:       &gateway,
			compartmentID: "compartment-" + fakeData.Lorem().Word(),
		})
		require.ErrorContains(t, err, "failed to delete OCI CA bundle")
	})

	t.Run("direct CA bundle helpers cover fallback and error paths", func(t *testing.T) {
		fakeData := faker.New()
		port := gatewayv1.PortNumber(8443)
		defaultValidation := &gatewayv1.FrontendTLSValidation{
			CACertificateRefs: []gatewayv1.ObjectReference{{
				Group: "",
				Kind:  "ConfigMap",
				Name:  "ca-" + gatewayv1.ObjectName(fakeData.Lorem().Word()),
			}},
		}
		gateway := makeGateway(t, port, defaultValidation)
		gateway.Spec.TLS.Frontend.PerPort = []gatewayv1.TLSPortConfig{{
			Port: port + 1,
			TLS:  gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{}},
		}}
		assert.Same(t, defaultValidation, effectiveFrontendTLSValidation(gateway, port))
		assert.Nil(t, frontendMTLSOCICABundleIDs(gatewayv1.Gateway{}, port))

		model, _ := makeModel(t)
		err := model.ensureFrontendMTLSCompartment(
			t.Context(),
			gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: gateway.Namespace, Name: gateway.Name}},
			"compartment-"+fakeData.Lorem().Word(),
		)
		require.ErrorContains(t, err, "failed to record frontend mTLS CA bundle compartment")

		gateway.Annotations[GatewayFrontendMTLSCABundleCompartmentsAnnotation] = "existing"
		require.NoError(t, model.ensureFrontendMTLSCompartment(t.Context(), gateway, "existing"))
		require.NoError(t, model.ensureFrontendMTLSCompartment(t.Context(), gateway, ""))

		pendingName := "pending-" + fakeData.Lorem().Word()
		err = ensureFrontendMTLSCABundleUsable(certificatesmanagement.CaBundleSummary{
			Name:           &pendingName,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateFailed,
		})
		require.ErrorContains(t, err, "not ready")
	})

	t.Run("direct existing CA bundle resolver handles list and deleted-only failures", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 443, nil)
		model, certsClient := makeModel(t, &gateway)
		name := "ca-" + fakeData.Lorem().Word()
		compartmentID := "compartment-" + fakeData.Lorem().Word()
		certsClient.listErr = errors.New("list failed")

		_, err := model.resolveExistingFrontendMTLSCABundle(t.Context(), gateway, compartmentID, name, "hash")

		require.ErrorContains(t, err, "failed to re-list OCI CA bundle")

		model, certsClient = makeModel(t, &gateway)
		certsClient.bundles[name] = certificatesmanagement.CaBundleSummary{
			Id:             new("deleted-id"),
			Name:           &name,
			LifecycleState: certificatesmanagement.CaBundleLifecycleStateDeleted,
			FreeformTags:   frontendMTLSCABundleTags(gateway, 443, "hash"),
		}
		_, err = model.resolveExistingFrontendMTLSCABundle(t.Context(), gateway, compartmentID, name, "hash")
		require.ErrorContains(t, err, "was not visible")
	})

	t.Run("helper functions are deterministic", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 8443, nil)
		gateway.UID = apitypes.UID("uid-" + fakeData.UUID().V4())
		ref := gatewayv1.ObjectReference{
			Group: "",
			Kind:  "ConfigMap",
			Name:  "ca-" + gatewayv1.ObjectName(fakeData.Lorem().Word()),
		}

		name := frontendMTLSCABundleName(gateway, 8443, ref)

		assert.True(t, strings.HasPrefix(name, frontendMTLSCABundleNamePrefix))
		assert.Len(t, name, len(frontendMTLSCABundleNamePrefix)+24)
		assert.Equal(t, string(gateway.UID), frontendMTLSGatewayIdentity(gateway))
		assert.True(t, isOwnedFrontendMTLSCABundle(frontendMTLSCABundleTags(gateway, 8443, "hash"), gateway))
		assert.False(t, isOwnedFrontendMTLSCABundle(nil, gateway))
		assert.False(t, isFrontendMTLSCABundleAlreadyExists(errors.New("plain")))
		assert.False(t, isFrontendMTLSCABundleAlreadyDeleted(errors.New("plain")))

		noUIDGateway := gateway
		noUIDGateway.UID = ""
		noUIDGateway.CreationTimestamp = metav1.NewTime(time.Now())
		assert.NotEmpty(t, frontendMTLSGatewayIdentity(noUIDGateway))

		noTimestampGateway := noUIDGateway
		noTimestampGateway.CreationTimestamp = metav1.Time{}
		noTimestampGateway.Generation = 17
		assert.Equal(t, "17", frontendMTLSGatewayIdentity(noTimestampGateway))
		assert.Equal(t, defaultFrontendMTLSVerifyDepth, lo.Must(frontendMTLSVerifyDepth(gatewayv1.Gateway{}, 8443)))
	})

	t.Run("Gateway frontend mTLS configured helper handles spec and annotations", func(t *testing.T) {
		fakeData := faker.New()
		gateway := makeGateway(t, 443, nil)
		assert.True(t, gatewayFrontendMTLSConfigured(gateway))

		gateway.Spec.TLS = nil
		gateway.Annotations = nil
		assert.False(t, gatewayFrontendMTLSConfigured(gateway))

		gateway.Annotations = map[string]string{
			FrontendMTLSVerifyDepthAnnotation: " ",
		}
		assert.False(t, gatewayFrontendMTLSConfigured(gateway))

		gateway.Annotations[frontendMTLSPortTrustedCABundleOCIDsAnnotation(443)] =
			"ocid1.cabundle.oc1.." + fakeData.UUID().V4()
		assert.True(t, gatewayFrontendMTLSConfigured(gateway))
	})
}
