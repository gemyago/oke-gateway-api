package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

const certificateLifecycleSecretNamePrefix = "https-cert"

func testHTTPCertificateLifecycle(t *testing.T, fixture *httpRoutingFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	gatewayName := "gateway-" + suffix
	routeName := "https-route-" + suffix
	secretName := certificateLifecycleSecretNamePrefix + "-" + suffix
	routePath := "/"
	backend := fixture.staticBackends[0]
	serialHostV1 := "v1-" + suffix + ".example.test"
	serialHostV2 := "v2-" + suffix + ".example.test"

	logger.InfoContext(ctx, "Loaded live HTTPS certificate lifecycle configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("loadBalancerID", cfg.OCI.LoadBalancerID),
	)

	publicIP := net.ParseIP(fixture.publicIP)
	require.NotNil(t, publicIP, "expected a parseable load balancer public IP")

	caBundle, err := newCertificateAuthority("oke-gateway-api e2e root " + suffix)
	require.NoError(t, err)

	leafV1, err := caBundle.newLeaf(certificateSpec{
		commonName: "oke-gateway-api e2e leaf v1 " + suffix,
		dnsNames:   []string{serialHostV1},
		ipSANs:     []net.IP{publicIP},
	})
	require.NoError(t, err)

	rootCAs := x509.NewCertPool()
	require.True(t, rootCAs.AppendCertsFromPEM(caBundle.certPEM))

	probeClient, err := probe.NewClient(fixture.publicIP, int(e2ek8s.DefaultHTTPSPort), &probe.ClientOptions{
		Scheme: "https",
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    rootCAs,
				},
			},
		},
	})
	require.NoError(t, err)

	logTestProgress(
		ctx,
		t,
		logger,
		"Using shared routing fixture namespace for HTTPS Gateway",
		slog.String("namespace", fixture.namespaceName),
		slog.String("gatewayClass", fixture.gatewayClassName),
		slog.String("sharedBackend", backend.Name),
	)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		logTestProgress(
			cleanupCtx,
			t,
			logger,
			"Deleting HTTPS Gateway",
			slog.String("namespace", fixture.namespaceName),
			slog.String("gateway", gatewayName),
		)
		if deleteErr := deleteObject(
			cleanupCtx,
			fixture.kubeClient.Client,
			ctrlclient.ObjectKey{Namespace: fixture.namespaceName, Name: gatewayName},
			&gatewayv1.Gateway{},
		); deleteErr != nil {
			t.Errorf("delete Gateway %s/%s: %v", fixture.namespaceName, gatewayName, deleteErr)
		}

		if deleteErr := deleteObject(
			cleanupCtx,
			fixture.kubeClient.Client,
			ctrlclient.ObjectKey{Namespace: fixture.namespaceName, Name: secretName},
			&corev1.Secret{},
		); deleteErr != nil {
			t.Errorf("delete Secret %s/%s: %v", fixture.namespaceName, secretName, deleteErr)
		}
	})
	registerHTTPRouteCleanup(t, fixture.kubeClient.WithWatch, fixture.namespaceName, routeName)

	tlsSecret := e2ek8s.NewTLSSecret(e2ek8s.TLSSecretOptions{
		Namespace:   fixture.namespaceName,
		Name:        secretName,
		Certificate: leafV1.certPEM,
		PrivateKey:  leafV1.keyPEM,
	})
	logger.InfoContext(
		ctx,
		"Creating initial TLS secret",
		slog.String("namespace", fixture.namespaceName),
		slog.String("secret", tlsSecret.Name),
		slog.String("serial", leafV1.cert.SerialNumber.String()),
		slog.String("fingerprint", certificateFingerprint(leafV1.cert)),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, tlsSecret))

	gateway := e2ek8s.NewGateway(e2ek8s.GatewayOptions{
		Namespace:         fixture.namespaceName,
		Name:              gatewayName,
		GatewayClassName:  fixture.gatewayClassName,
		GatewayConfigName: fixture.gatewayConfigName,
		Listeners: []gatewayv1.Listener{
			{
				Name:     e2ek8s.DefaultHTTPSListenerName,
				Port:     e2ek8s.DefaultHTTPSPort,
				Protocol: gatewayv1.HTTPSProtocolType,
				TLS: &gatewayv1.GatewayTLSConfig{
					CertificateRefs: []gatewayv1.SecretObjectReference{
						{Name: gatewayv1.ObjectName(secretName)},
					},
				},
			},
		},
	})
	logger.InfoContext(
		ctx,
		"Creating HTTPS Gateway",
		slog.String("namespace", fixture.namespaceName),
		slog.String("gateway", gatewayName),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, gateway))

	_, err = e2ek8s.WaitForGatewayAccepted(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	logTestProgress(
		ctx,
		t,
		logger,
		"Waiting for HTTPS Gateway programmed",
		slog.String("namespace", fixture.namespaceName),
		slog.String("gateway", gatewayName),
	)

	_, err = e2ek8s.WaitForGatewayProgrammed(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)
	logTestProgress(
		ctx,
		t,
		logger,
		"HTTPS Gateway accepted and programmed",
		slog.String("namespace", fixture.namespaceName),
		slog.String("gateway", gatewayName),
	)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    fixture.namespaceName,
		Name:         routeName,
		GatewayName:  gatewayName,
		ListenerName: e2ek8s.DefaultHTTPSListenerName,
		ServiceName:  backend.Name,
		ServicePort:  e2ek8s.DefaultEchoPort,
		PathPrefix:   routePath,
	})
	logTestProgress(
		ctx,
		t,
		logger,
		"Creating HTTPS HTTPRoute",
		slog.String("namespace", fixture.namespaceName),
		slog.String("httpRoute", routeName),
		slog.String("backend", backend.Name),
	)
	require.NoError(t, fixture.kubeClient.Create(ctx, httpRoute))

	_, err = e2ek8s.WaitForHTTPRouteAccepted(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		routeName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)
	_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		fixture.kubeClient.Client,
		fixture.namespaceName,
		routeName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	logTestProgress(
		ctx,
		t,
		logger,
		"Waiting for initial HTTPS certificate",
		slog.String("namespace", fixture.namespaceName),
		slog.String("secret", secretName),
		slog.String("serial", leafV1.cert.SerialNumber.String()),
	)
	responseV1, err := waitForServedCertificate(
		ctx,
		probeClient,
		routePath,
		backend.Response,
		leafV1.cert,
		[]string{serialHostV1},
	)
	require.NoError(t, err)
	logger.InfoContext(
		ctx,
		"Observed initial HTTPS certificate",
		slog.String("serial", responseV1.TLSPeerCertificates[0].SerialNumber.String()),
		slog.String("fingerprint", certificateFingerprint(responseV1.TLSPeerCertificates[0])),
	)

	leafV2, err := caBundle.newLeaf(certificateSpec{
		commonName: "oke-gateway-api e2e leaf v2 " + suffix,
		dnsNames:   []string{serialHostV1, serialHostV2},
		ipSANs:     []net.IP{publicIP},
	})
	require.NoError(t, err)

	logger.InfoContext(
		ctx,
		"Updating TLS secret with rotated certificate",
		slog.String("namespace", fixture.namespaceName),
		slog.String("secret", secretName),
		slog.String("serial", leafV2.cert.SerialNumber.String()),
		slog.String("fingerprint", certificateFingerprint(leafV2.cert)),
	)
	require.NoError(
		t,
		updateTLSSecret(ctx, fixture.kubeClient.Client, ctrlclient.ObjectKey{
			Namespace: fixture.namespaceName,
			Name:      secretName,
		}, leafV2.certPEM, leafV2.keyPEM),
	)

	responseV2, err := waitForServedCertificate(
		ctx,
		probeClient,
		routePath,
		backend.Response,
		leafV2.cert,
		[]string{serialHostV1, serialHostV2},
	)
	require.NoError(t, err)
	require.NotEqual(
		t,
		certificateFingerprint(responseV1.TLSPeerCertificates[0]),
		certificateFingerprint(responseV2.TLSPeerCertificates[0]),
	)
	logTestProgress(
		ctx,
		t,
		logger,
		"HTTPS certificate lifecycle completed",
		slog.String("namespace", fixture.namespaceName),
		slog.String("secret", secretName),
		slog.String("newSerial", responseV2.TLSPeerCertificates[0].SerialNumber.String()),
	)
}

type certificateAuthority struct {
	cert    *x509.Certificate
	key     *rsa.PrivateKey
	certPEM []byte
}

type issuedLeafCertificate struct {
	cert    *x509.Certificate
	keyPEM  []byte
	certPEM []byte
}

type certificateSpec struct {
	commonName string
	dnsNames   []string
	ipSANs     []net.IP
}

func newCertificateAuthority(commonName string) (*certificateAuthority, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate CA private key: %w", err)
	}

	serialNumber, err := randomSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate CA serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	return &certificateAuthority{
		cert: cert,
		key:  privateKey,
		certPEM: pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certDER,
		}),
	}, nil
}

func (ca *certificateAuthority) newLeaf(spec certificateSpec) (*issuedLeafCertificate, error) {
	if ca == nil || ca.cert == nil || ca.key == nil {
		return nil, errors.New("certificate authority is not initialized")
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate leaf private key: %w", err)
	}

	serialNumber, err := randomSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate leaf serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: spec.commonName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(12 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              append([]string(nil), spec.dnsNames...),
		IPAddresses:           append([]net.IP(nil), spec.ipSANs...),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &privateKey.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("create leaf certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate: %w", err)
	}

	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal leaf private key: %w", err)
	}

	certificatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
	certificatePEM = append(certificatePEM, ca.certPEM...)

	return &issuedLeafCertificate{
		cert:    cert,
		certPEM: certificatePEM,
		keyPEM: pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: privateKeyDER,
		}),
	}, nil
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}

	return serialNumber, nil
}

func waitForServedCertificate(
	ctx context.Context,
	client *probe.Client,
	path string,
	expectedBody string,
	expectedCertificate *x509.Certificate,
	expectedDNSNames []string,
) (*probe.Response, error) {
	return probe.WaitForResponse(
		ctx,
		client,
		path,
		nil,
		nil,
		fmt.Sprintf(
			"wait for HTTPS echo and certificate serial %s",
			expectedCertificate.SerialNumber.String(),
		),
		func(response *probe.Response) (bool, string) {
			if response == nil {
				return false, "no response received"
			}

			if response.StatusCode != http.StatusOK {
				return false, fmt.Sprintf("received status %d", response.StatusCode)
			}

			if response.BodyString() != expectedBody {
				return false, fmt.Sprintf(
					"received body %q, expected %q",
					response.BodyString(),
					expectedBody,
				)
			}

			if len(response.TLSPeerCertificates) == 0 {
				return false, "TLS peer certificate is missing"
			}

			peerCertificate := response.TLSPeerCertificates[0]
			if peerCertificate.SerialNumber.Cmp(expectedCertificate.SerialNumber) != 0 {
				return false, fmt.Sprintf(
					"served serial %s did not match expected %s",
					peerCertificate.SerialNumber.String(),
					expectedCertificate.SerialNumber.String(),
				)
			}

			if certificateFingerprint(peerCertificate) != certificateFingerprint(expectedCertificate) {
				return false, "served certificate fingerprint did not match"
			}

			if !sameStrings(peerCertificate.DNSNames, expectedDNSNames) {
				return false, fmt.Sprintf(
					"served DNS names %v did not match expected %v",
					peerCertificate.DNSNames,
					expectedDNSNames,
				)
			}

			return true, ""
		},
	)
}

func updateTLSSecret(
	ctx context.Context,
	kubeClient ctrlclient.Client,
	key ctrlclient.ObjectKey,
	certificate []byte,
	privateKey []byte,
) error {
	secret := &corev1.Secret{}
	if err := kubeClient.Get(ctx, key, secret); err != nil {
		return fmt.Errorf("get TLS secret %s/%s: %w", key.Namespace, key.Name, err)
	}

	secret.Type = corev1.SecretTypeTLS
	secret.Data = map[string][]byte{
		corev1.TLSCertKey:       append([]byte(nil), certificate...),
		corev1.TLSPrivateKeyKey: append([]byte(nil), privateKey...),
	}

	if err := kubeClient.Update(ctx, secret); err != nil {
		return fmt.Errorf("update TLS secret %s/%s: %w", key.Namespace, key.Name, err)
	}

	return nil
}

func certificateFingerprint(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}

	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:])
}

func sameStrings(left []string, right []string) bool {
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	slices.Sort(leftCopy)
	slices.Sort(rightCopy)
	return slices.Equal(leftCopy, rightCopy)
}
