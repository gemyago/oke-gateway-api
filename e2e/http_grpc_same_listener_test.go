package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/probe"
)

func testHTTPGRPCSameListenerProtocol(t *testing.T, live *liveFixture) {
	logger := startTestLogger(t)
	ctx, cfg := newLiveHTTPContext(t)

	fake := faker.New()
	suffix := randomDNSLabel(fake)
	gatewayName := "gateway-grpc-" + suffix
	listenerName := gatewayv1.SectionName("grpc-" + suffix)
	listenerPort := gatewayv1.PortNumber(20000 + rand.IntN(10000))
	host := gatewayv1.Hostname("grpc-" + suffix + ".example.test")
	httpRouteName := "http-" + suffix
	grpcRouteName := "grpc-" + suffix
	backendName := "backend-" + suffix
	secretName := "tls-" + suffix
	responseText := "grpc same listener " + suffix

	logTestProgress(
		ctx,
		t,
		logger,
		"Creating isolated Gateway for mixed HTTPRoute and GRPCRoute listener protocol test",
		slog.String("listener", string(listenerName)),
		slog.Int("port", int(listenerPort)),
	)

	gatewayNamespace, err := createIsolatedGatewayNamespace(ctx, t, live, cfg, suffix)
	require.NoError(t, err)

	caBundle, err := newCertificateAuthority("oke gateway api grpc e2e root " + suffix)
	require.NoError(t, err)
	leaf, err := caBundle.newLeaf(certificateSpec{
		commonName: "oke gateway api grpc e2e leaf " + suffix,
		dnsNames:   []string{string(host)},
	})
	require.NoError(t, err)

	require.NoError(t, live.kubeClient.Create(ctx, e2ek8s.NewTLSSecret(e2ek8s.TLSSecretOptions{
		Namespace:   gatewayNamespace.namespaceName,
		Name:        secretName,
		Certificate: leaf.certPEM,
		PrivateKey:  leaf.keyPEM,
	})))

	require.NoError(t, live.kubeClient.Create(ctx, e2ek8s.NewEchoService(e2ek8s.EchoServiceOptions{
		Namespace: gatewayNamespace.namespaceName,
		Name:      backendName,
	})))
	require.NoError(t, live.kubeClient.Create(ctx, e2ek8s.NewStaticHTTPDeployment(
		e2ek8s.StaticHTTPDeploymentOptions{
			Namespace:    gatewayNamespace.namespaceName,
			Name:         backendName,
			ResponseText: responseText,
		},
	)))
	_, err = e2ek8s.WaitForDeploymentReady(ctx, live.kubeClient.Client, gatewayNamespace.namespaceName, backendName, nil)
	require.NoError(t, err)
	_, err = e2ek8s.WaitForServiceEndpointsReady(ctx, live.kubeClient.Client, gatewayNamespace.namespaceName, backendName, nil)
	require.NoError(t, err)

	gateway := e2ek8s.NewGateway(e2ek8s.GatewayOptions{
		Namespace:         gatewayNamespace.namespaceName,
		Name:              gatewayName,
		GatewayClassName:  gatewayNamespace.gatewayClassName,
		GatewayConfigName: gatewayNamespace.gatewayConfigName,
		Listeners: []gatewayv1.Listener{
			{
				Name:     listenerName,
				Port:     listenerPort,
				Protocol: gatewayv1.HTTPSProtocolType,
				TLS: &gatewayv1.ListenerTLSConfig{
					CertificateRefs: []gatewayv1.SecretObjectReference{
						{Name: gatewayv1.ObjectName(secretName)},
					},
				},
			},
		},
	})
	require.NoError(t, live.kubeClient.Create(ctx, gateway))
	_, err = e2ek8s.WaitForGatewayAccepted(
		ctx,
		live.kubeClient.Client,
		gatewayNamespace.namespaceName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)
	_, err = e2ek8s.WaitForGatewayProgrammed(
		ctx,
		live.kubeClient.Client,
		gatewayNamespace.namespaceName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	httpRoute := e2ek8s.NewHTTPRoute(e2ek8s.HTTPRouteOptions{
		Namespace:    gatewayNamespace.namespaceName,
		Name:         httpRouteName,
		GatewayName:  gatewayName,
		ListenerName: listenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		Hostnames:    []gatewayv1.Hostname{host},
	})
	require.NoError(t, live.kubeClient.Create(ctx, httpRoute))
	grpcRoute := e2ek8s.NewGRPCRoute(e2ek8s.GRPCRouteOptions{
		Namespace:    gatewayNamespace.namespaceName,
		Name:         grpcRouteName,
		GatewayName:  gatewayName,
		ListenerName: listenerName,
		ServiceName:  backendName,
		ServicePort:  e2ek8s.DefaultEchoPort,
		Hostnames:    []gatewayv1.Hostname{host},
	})
	require.NoError(t, live.kubeClient.Create(ctx, grpcRoute))

	_, err = e2ek8s.WaitForHTTPRouteResolvedRefs(
		ctx,
		live.kubeClient.Client,
		gatewayNamespace.namespaceName,
		httpRouteName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)
	_, err = e2ek8s.WaitForGRPCRouteResolvedRefs(
		ctx,
		live.kubeClient.Client,
		gatewayNamespace.namespaceName,
		grpcRouteName,
		gatewayName,
		nil,
	)
	require.NoError(t, err)

	require.NoError(t, waitForOCIListenerProtocol(
		ctx,
		live.ociClient,
		cfg.OCI.LoadBalancerID,
		string(listenerName),
		"GRPC",
	))

	rootCAs := x509.NewCertPool()
	require.True(t, rootCAs.AppendCertsFromPEM(caBundle.certPEM))
	probeClient, err := probe.NewClient(live.publicIP, int(listenerPort), &probe.ClientOptions{
		Scheme: "https",
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    rootCAs,
					ServerName: string(host),
				},
			},
		},
	})
	require.NoError(t, err)

	_, err = probe.WaitForResponse(
		ctx,
		probeClient,
		"/",
		&probe.RequestOptions{Host: string(host)},
		nil,
		"wait for HTTPRoute response on shared GRPC listener",
		func(response *probe.Response) (bool, string) {
			switch {
			case response == nil:
				return false, "no response received"
			case response.StatusCode != http.StatusOK:
				return false, fmt.Sprintf("received status %d", response.StatusCode)
			case response.BodyString() != responseText:
				return false, fmt.Sprintf("received body %q", response.BodyString())
			default:
				return true, ""
			}
		},
	)
	require.NoError(t, err)
}

func waitForOCIListenerProtocol(
	ctx context.Context,
	client *loadbalancer.LoadBalancerClient,
	loadBalancerID string,
	listenerName string,
	wantProtocol string,
) error {
	progressLogger := diag.NewWaitProgressLogger(nil, "wait for OCI listener protocol", 0)
	var lastMessage string

	for {
		response, err := client.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: &loadBalancerID,
		})
		if err != nil {
			return fmt.Errorf("get load balancer %q: %w", loadBalancerID, err)
		}

		listener, ok := response.LoadBalancer.Listeners[listenerName]
		if ok && listener.Protocol != nil {
			gotProtocol := strings.TrimSpace(*listener.Protocol)
			if gotProtocol == wantProtocol {
				return nil
			}

			lastMessage = fmt.Sprintf("listener %q protocol is %q, want %q", listenerName, gotProtocol, wantProtocol)
		} else if ok {
			lastMessage = fmt.Sprintf("listener %q protocol is empty, want %q", listenerName, wantProtocol)
		} else {
			lastMessage = fmt.Sprintf("listener %q not found", listenerName)
		}
		progressLogger.Log(ctx, lastMessage)

		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", lastMessage, ctx.Err())
		case <-timer.C:
		}
	}
}
