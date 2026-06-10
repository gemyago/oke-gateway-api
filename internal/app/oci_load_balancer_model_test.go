package app

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

func TestOciLoadBalancerModelImpl(t *testing.T) {
	makeMockDeps := func(t *testing.T) ociLoadBalancerModelDeps {
		return ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
	}
	makeMatchingRoutingPolicy := func(routingPolicyName, defaultBackendSetName string) loadbalancer.RoutingPolicy {
		return loadbalancer.RoutingPolicy{
			Name:                     new(routingPolicyName),
			ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
			Rules: []loadbalancer.RoutingRule{
				defaultCatchAllRoutingRule(defaultBackendSetName),
			},
		}
	}

	t.Run("helper functions", func(t *testing.T) {
		t.Run("compares health checkers", func(t *testing.T) {
			fake := faker.New()
			protocol := "TCP"
			port := rand.IntN(60000) + 1
			assert.False(t, loadBalancerHealthCheckerMatches(nil, loadbalancer.HealthCheckerDetails{
				Protocol: &protocol,
				Port:     &port,
			}))
			assert.False(t, loadBalancerHealthCheckerMatches(&loadbalancer.HealthChecker{
				Protocol: &protocol,
				Port:     new(port + 1),
			}, loadbalancer.HealthCheckerDetails{
				Protocol: &protocol,
				Port:     &port,
			}))
			assert.False(t, loadBalancerBackendSetMatches(
				loadbalancer.BackendSet{
					Policy: new("IP_HASH"),
					HealthChecker: &loadbalancer.HealthChecker{
						Protocol: &protocol,
						Port:     &port,
					},
				},
				"ROUND_ROBIN",
				loadbalancer.HealthCheckerDetails{Protocol: &protocol, Port: &port},
			))
			assert.NotEmpty(t, fake.Lorem().Word())
		})

		t.Run("copies backend and ssl details", func(t *testing.T) {
			fake := faker.New()
			backend := loadbalancer.Backend{
				Backup:         new(true),
				Drain:          new(false),
				IpAddress:      new(fake.Internet().Ipv4()),
				Offline:        new(false),
				Port:           new(rand.IntN(60000) + 1),
				Weight:         new(rand.IntN(100) + 1),
				MaxConnections: new(rand.IntN(1000) + 1),
			}

			assert.Equal(t, loadbalancer.BackendDetails{
				Backup:         backend.Backup,
				Drain:          backend.Drain,
				IpAddress:      backend.IpAddress,
				Offline:        backend.Offline,
				Port:           backend.Port,
				Weight:         backend.Weight,
				MaxConnections: backend.MaxConnections,
			}, ociBackendToDetails(backend, 0))
			assert.Nil(t, sslConfigurationDetailsFromBackendSet(nil))

			certificateID := fake.UUID().V4()
			config := loadbalancer.SslConfiguration{
				VerifyDepth:                    new(rand.IntN(5) + 1),
				VerifyPeerCertificate:          new(true),
				HasSessionResumption:           new(true),
				TrustedCertificateAuthorityIds: []string{fake.UUID().V4()},
				CertificateIds:                 []string{certificateID},
				CertificateName:                new("cert-" + fake.Lorem().Word()),
				Protocols:                      []string{"TLSv1.2"},
				CipherSuiteName:                new("cipher-" + fake.Lorem().Word()),
				ServerOrderPreference:          loadbalancer.SslConfigurationServerOrderPreferenceEnabled,
			}

			got := sslConfigurationDetailsFromBackendSet(&config)

			require.NotNil(t, got)
			assert.Equal(t, config.VerifyDepth, got.VerifyDepth)
			assert.Equal(t, config.VerifyPeerCertificate, got.VerifyPeerCertificate)
			assert.Equal(t, config.HasSessionResumption, got.HasSessionResumption)
			assert.Equal(t, config.TrustedCertificateAuthorityIds, got.TrustedCertificateAuthorityIds)
			assert.Equal(t, config.CertificateIds, got.CertificateIds)
			assert.Equal(t, config.CertificateName, got.CertificateName)
			assert.Equal(t, config.Protocols, got.Protocols)
			assert.Equal(t, config.CipherSuiteName, got.CipherSuiteName)
			assert.Equal(t, loadbalancer.SslConfigurationDetailsServerOrderPreferenceEnabled, got.ServerOrderPreference)
		})

		t.Run("detects routing default rule shape", func(t *testing.T) {
			fake := faker.New()
			defaultBackendSetName := "default-" + fake.Lorem().Word()
			assert.False(t, routingRuleForwardsToBackendSet(loadbalancer.RoutingRule{}, defaultBackendSetName))
			assert.False(t, routingRuleForwardsToBackendSet(loadbalancer.RoutingRule{
				Actions: []loadbalancer.Action{loadbalancer.RedirectRule{}},
			}, defaultBackendSetName))
			assert.True(t, routingPolicyDefaultRuleDrifted(loadbalancer.RoutingPolicy{
				Rules: []loadbalancer.RoutingRule{makeRandomOCIRoutingRule()},
			}, defaultBackendSetName))
			assert.False(t, routingPolicyDefaultRuleDrifted(loadbalancer.RoutingPolicy{
				Rules: []loadbalancer.RoutingRule{defaultCatchAllRoutingRule(defaultBackendSetName)},
			}, defaultBackendSetName))
		})

		t.Run("includes grpc rule name when present", func(t *testing.T) {
			fake := faker.New()
			ruleName := gatewayv1.SectionName("grpc-" + fake.Lorem().Word())
			route := makeRandomGRPCRoute(randomGRPCRouteWithRulesOpt(
				makeRandomGRPCRouteRule(func(rule *gatewayv1.GRPCRouteRule) {
					rule.Name = &ruleName
				}),
			))

			got := ociGRPCListenerPolicyRuleName(route, 0)

			assert.Equal(
				t,
				ociListenerPolicyRuleNameFromParts(0, "grpc", route.Namespace, route.Name, string(ruleName)),
				got,
			)
			assert.NotEqual(
				t,
				ociListenerPolicyRuleNameFromParts(0, "grpc", route.Namespace, route.Name),
				got,
			)
		})
	})

	t.Run("updateBackendSetConfig", func(t *testing.T) {
		t.Run("returns update errors", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().UpdateBackendSet(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateBackendSetResponse{}, wantErr).
				Once()

			err := model.updateBackendSetConfig(
				t.Context(),
				fake.UUID().V4(),
				"backend-"+fake.Lorem().Word(),
				makeRandomOCIBackendSet(),
				"ROUND_ROBIN",
				loadBalancerBackendSetHealthChecker(rand.IntN(60000)+1),
			)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns missing work request id errors", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			loadBalancerID := fake.UUID().V4()
			backendSetName := "backend-" + fake.Lorem().Word()

			ociLoadBalancerClient.EXPECT().UpdateBackendSet(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateBackendSetRequest) bool {
					return lo.FromPtr(req.LoadBalancerId) == loadBalancerID &&
						lo.FromPtr(req.BackendSetName) == backendSetName
				}),
			).Return(loadbalancer.UpdateBackendSetResponse{}, nil).Once()

			err := model.updateBackendSetConfig(
				t.Context(),
				loadBalancerID,
				backendSetName,
				makeRandomOCIBackendSet(),
				"ROUND_ROBIN",
				loadBalancerBackendSetHealthChecker(rand.IntN(60000)+1),
			)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("returns wait errors", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			loadBalancerID := fake.UUID().V4()
			backendSetName := "backend-" + fake.Lorem().Word()
			workRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().UpdateBackendSet(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateBackendSetResponse{OpcWorkRequestId: &workRequestID}, nil).
				Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			err := model.updateBackendSetConfig(
				t.Context(),
				loadBalancerID,
				backendSetName,
				makeRandomOCIBackendSet(),
				"ROUND_ROBIN",
				loadBalancerBackendSetHealthChecker(rand.IntN(60000)+1),
			)

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("reconcileDefaultBackendSet", func(t *testing.T) {
		t.Run("when backend set exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"
			existingBackendSet := makeRandomOCIBackendSet(
				randomOCIBackendSetWithNameOpt(wantBsName),
				func(bs *loadbalancer.BackendSet) {
					bs.Policy = new("ROUND_ROBIN")
					bs.HealthChecker = &loadbalancer.HealthChecker{
						Protocol: new("TCP"),
						Port:     new(defaultBackendSetPort),
					}
				},
			)

			knownBackendSets := map[string]loadbalancer.BackendSet{
				wantBsName:       existingBackendSet,
				fake.UUID().V4(): makeRandomOCIBackendSet(),
				fake.UUID().V4(): makeRandomOCIBackendSet(),
			}

			params := reconcileDefaultBackendParams{
				loadBalancerID:   fake.UUID().V4(),
				knownBackendSets: knownBackendSets,
				gateway:          gw,
			}
			actualBackendSet, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, existingBackendSet, actualBackendSet)
		})

		t.Run("updates existing backend set config drift", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()
			wantBsName := gw.Name + "-default"
			existingBackendSet := makeRandomOCIBackendSet(randomOCIBackendSetWithNameOpt(wantBsName))

			params := reconcileDefaultBackendParams{
				loadBalancerID: fake.UUID().V4(),
				knownBackendSets: map[string]loadbalancer.BackendSet{
					wantBsName: existingBackendSet,
				},
				gateway: gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().UpdateBackendSet(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateBackendSetRequest) bool {
					return assert.Equal(t, params.loadBalancerID, *req.LoadBalancerId) &&
						assert.Equal(t, wantBsName, *req.BackendSetName) &&
						assert.Equal(t, "ROUND_ROBIN", *req.Policy) &&
						assert.Equal(t, "TCP", *req.HealthChecker.Protocol) &&
						assert.Equal(t, defaultBackendSetPort, *req.HealthChecker.Port)
				}),
			).Return(loadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			actualBackendSet, err := model.reconcileDefaultBackendSet(t.Context(), params)

			require.NoError(t, err)
			assert.Equal(t, "ROUND_ROBIN", lo.FromPtr(actualBackendSet.Policy))
			require.NotNil(t, actualBackendSet.HealthChecker)
			assert.Equal(t, "TCP", lo.FromPtr(actualBackendSet.HealthChecker.Protocol))
			assert.Equal(t, defaultBackendSetPort, lo.FromPtr(actualBackendSet.HealthChecker.Port))
		})

		t.Run("when backend set does not exist", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"
			wantBs := makeRandomOCIBackendSet()

			params := reconcileDefaultBackendParams{
				loadBalancerID: fake.UUID().V4(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     new(int(80)),
						Protocol: new("TCP"),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: wantBs,
			}, nil)

			actualBackendSet, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, wantBs, actualBackendSet)
		})

		t.Run("when create backend set fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: fake.UUID().V4(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     new(int(80)),
						Protocol: new("TCP"),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{}, wantErr)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("when wait for backend set fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: fake.UUID().V4(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     new(int(80)),
						Protocol: new("TCP"),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("when create backend set has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()
			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: fake.UUID().V4(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     new(int(80)),
						Protocol: new("TCP"),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{}, nil)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("when final get backend set fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

			params := reconcileDefaultBackendParams{
				loadBalancerID: fake.UUID().V4(),
				gateway:        gw,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Port:     new(int(80)),
						Protocol: new("TCP"),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{}, wantErr)

			_, err := model.reconcileDefaultBackendSet(t.Context(), params)
			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("reconcileListenersCertificates", func(t *testing.T) {
		t.Run("all certificates exist", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			listeners := []gatewayv1.Listener{
				makeRandomListener(randomListenerWithHTTPSParamsOpt()),
				makeRandomListener(randomListenerWithHTTPSParamsOpt()),
				makeRandomListener(randomListenerWithHTTPSParamsOpt()),
				makeRandomListener(),
			}

			gateway := newRandomGateway(
				randomGatewayWithListenersOpt(listeners...),
			)

			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			knownCertificates := make(map[string]loadbalancer.Certificate)
			certificatesByListener := make(map[string][]loadbalancer.Certificate)
			for _, listener := range listeners {
				if listener.TLS != nil {
					for _, ref := range listener.TLS.CertificateRefs {
						secret := makeRandomSecret()
						setupClientGet(t, k8sClient, types.NamespacedName{
							Namespace: string(lo.FromPtr(ref.Namespace)),
							Name:      string(ref.Name),
						}, secret).Once()
						certName := ociCertificateNameFromSecret(secret)
						knownCertificates[certName] = makeRandomOCICertificate()
						certificatesByListener[string(listener.Name)] = append(
							certificatesByListener[string(listener.Name)],
							knownCertificates[certName],
						)
					}
				}
			}

			params := reconcileListenersCertificatesParams{
				loadBalancerID:    fake.UUID().V4(),
				gateway:           gateway,
				knownCertificates: knownCertificates,
			}

			gotResult, err := model.reconcileListenersCertificates(t.Context(), params)
			require.NoError(t, err)

			assert.Equal(t, knownCertificates, gotResult.reconciledCertificates, "knownCertificates should be equal")
			assert.Equal(
				t,
				certificatesByListener,
				gotResult.certificatesByListener,
				"listenerCertificates should be equal",
			)
		})

		t.Run("all certificates exist in gateway namespace", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			listeners := []gatewayv1.Listener{
				makeRandomListener(
					func(l *gatewayv1.Listener) {
						l.TLS = &gatewayv1.ListenerTLSConfig{
							CertificateRefs: []gatewayv1.SecretObjectReference{
								{
									Name: gatewayv1.ObjectName("cert1-" + fake.Internet().Domain()),
								},
								{
									Name: gatewayv1.ObjectName("cert2-" + fake.Internet().Domain()),
								},
							},
						}
					},
				),
			}

			gateway := newRandomGateway(
				randomGatewayWithListenersOpt(listeners...),
			)

			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			knownCertificates := make(map[string]loadbalancer.Certificate)
			certificatesByListener := make(map[string][]loadbalancer.Certificate)
			for _, listener := range listeners {
				if listener.TLS != nil {
					for _, ref := range listener.TLS.CertificateRefs {
						secret := makeRandomSecret()
						setupClientGet(t, k8sClient, types.NamespacedName{
							Namespace: gateway.Namespace,
							Name:      string(ref.Name),
						}, secret).Once()
						certName := ociCertificateNameFromSecret(secret)
						knownCertificates[certName] = makeRandomOCICertificate()
						certificatesByListener[string(listener.Name)] = append(
							certificatesByListener[string(listener.Name)],
							knownCertificates[certName],
						)
					}
				}
			}

			params := reconcileListenersCertificatesParams{
				loadBalancerID:    fake.UUID().V4(),
				gateway:           gateway,
				knownCertificates: knownCertificates,
			}

			gotResult, err := model.reconcileListenersCertificates(t.Context(), params)
			require.NoError(t, err)

			assert.Equal(t, knownCertificates, gotResult.reconciledCertificates, "knownCertificates should be equal")
			assert.Equal(
				t,
				certificatesByListener,
				gotResult.certificatesByListener,
				"listenerCertificates should be equal",
			)
		})

		t.Run("some certificates are missing", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			missingCertListeners := []gatewayv1.Listener{
				makeRandomListener(
					randomListenerWithNameOpt(gatewayv1.SectionName("missing-cert-1-"+fake.UUID().V4())),
					randomListenerWithHTTPSParamsOpt(),
				),
				makeRandomListener(
					randomListenerWithNameOpt(gatewayv1.SectionName("missing-cert-2-"+fake.UUID().V4())),
					randomListenerWithHTTPSParamsOpt(),
				),
				makeRandomListener(
					randomListenerWithNameOpt(gatewayv1.SectionName("missing-cert-3-"+fake.UUID().V4())),
					randomListenerWithHTTPSParamsOpt(),
				),
			}

			existingCertsListeners := []gatewayv1.Listener{
				makeRandomListener(randomListenerWithHTTPSParamsOpt()),
				makeRandomListener(randomListenerWithHTTPSParamsOpt()),
				makeRandomListener(randomListenerWithHTTPSParamsOpt()),
			}

			allListeners := make([]gatewayv1.Listener, 0, len(existingCertsListeners)+len(missingCertListeners))
			allListeners = append(allListeners, existingCertsListeners...)
			allListeners = append(allListeners, missingCertListeners...)

			gateway := newRandomGateway(
				randomGatewayWithListenersOpt(allListeners...),
			)

			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			loadBalancerID := fake.UUID().V4()
			knownCertificates := make(map[string]loadbalancer.Certificate)
			wantResultingCerts := make(map[string]loadbalancer.Certificate)
			wantResultingCertsByListener := make(map[string][]loadbalancer.Certificate)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			for _, listener := range existingCertsListeners {
				if listener.TLS != nil {
					for _, ref := range listener.TLS.CertificateRefs {
						secret := makeRandomSecret()
						setupClientGet(t, k8sClient, types.NamespacedName{
							Namespace: string(lo.FromPtr(ref.Namespace)),
							Name:      string(ref.Name),
						}, secret).Once()
						certName := ociCertificateNameFromSecret(secret)
						existingCert := makeRandomOCICertificate()
						knownCertificates[certName] = existingCert
						wantResultingCerts[certName] = existingCert
						wantResultingCertsByListener[string(listener.Name)] = append(
							wantResultingCertsByListener[string(listener.Name)],
							existingCert,
						)
					}
				}
			}

			for _, listener := range missingCertListeners {
				if listener.TLS != nil {
					for i, ref := range listener.TLS.CertificateRefs {
						secretName := fmt.Sprintf(
							"missing-cert-%d-%s-%s",
							i,
							fake.Internet().Domain(),
							fake.Lorem().Word(),
						)
						secret := makeRandomSecret(
							randomSecretWithNameOpt(secretName),
							randomSecretWithTLSDataOpt(),
						)
						setupClientGet(t, k8sClient, types.NamespacedName{
							Namespace: string(lo.FromPtr(ref.Namespace)),
							Name:      string(ref.Name),
						}, secret).Once()
						certName := ociCertificateNameFromSecret(secret)

						workRequestID := fake.UUID().V4()
						certCreateDetails := loadbalancer.CreateCertificateDetails{
							CertificateName:   &certName,
							PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
							PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
						}
						ociLoadBalancerClient.EXPECT().
							CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
								LoadBalancerId:           &loadBalancerID,
								CreateCertificateDetails: certCreateDetails,
							}).
							Return(loadbalancer.CreateCertificateResponse{
								OpcWorkRequestId: &workRequestID,
							}, nil)

						workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

						ociCert := loadbalancer.Certificate{
							CertificateName:   &certName,
							PublicCertificate: certCreateDetails.PublicCertificate,
						}
						wantResultingCerts[certName] = ociCert
						wantResultingCertsByListener[string(listener.Name)] = append(
							wantResultingCertsByListener[string(listener.Name)],
							ociCert,
						)
					}
				}
			}

			params := reconcileListenersCertificatesParams{
				loadBalancerID:    loadBalancerID,
				gateway:           gateway,
				knownCertificates: maps.Clone(knownCertificates),
			}

			gotResult, err := model.reconcileListenersCertificates(t.Context(), params)
			require.NoError(t, err)

			assert.Equal(t, wantResultingCerts, gotResult.reconciledCertificates, "knownCertificates should be equal")
			assert.Equal(t,
				wantResultingCertsByListener,
				gotResult.certificatesByListener,
				"listenerCertificates should be equal",
			)
		})

		t.Run("fails when secret get fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			listener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway(randomGatewayWithListenersOpt(listener))
			ref := listener.TLS.CertificateRefs[0]
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)

			k8sClient.EXPECT().Get(t.Context(), types.NamespacedName{
				Namespace: string(lo.FromPtr(ref.Namespace)),
				Name:      string(ref.Name),
			}, mock.Anything).Return(wantErr).Once()

			_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
				loadBalancerID: faker.New().UUID().V4(),
				gateway:        gateway,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when certificate creation fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			listener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway(randomGatewayWithListenersOpt(listener))
			ref := listener.TLS.CertificateRefs[0]
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			certName := ociCertificateNameFromSecret(secret)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			loadBalancerID := faker.New().UUID().V4()

			setupClientGet(t, k8sClient, types.NamespacedName{
				Namespace: string(lo.FromPtr(ref.Namespace)),
				Name:      string(ref.Name),
			}, secret).Once()

			ociLoadBalancerClient.EXPECT().CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
				},
			}).Return(loadbalancer.CreateCertificateResponse{}, wantErr).Once()

			_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
				loadBalancerID: loadBalancerID,
				gateway:        gateway,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when certificate creation wait fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			listener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway(randomGatewayWithListenersOpt(listener))
			ref := listener.TLS.CertificateRefs[0]
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			certName := ociCertificateNameFromSecret(secret)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			loadBalancerID := faker.New().UUID().V4()
			workRequestID := faker.New().UUID().V4()

			setupClientGet(t, k8sClient, types.NamespacedName{
				Namespace: string(lo.FromPtr(ref.Namespace)),
				Name:      string(ref.Name),
			}, secret).Once()

			ociLoadBalancerClient.EXPECT().CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
				},
			}).Return(loadbalancer.CreateCertificateResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
				loadBalancerID: loadBalancerID,
				gateway:        gateway,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("reconcileHTTPListener", func(t *testing.T) {
		t.Run("when regular http listener exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			lbListener := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.Name = new(string(gwListener.Name))
				},
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
					fake.UUID().V4():        makeRandomOCIListener(),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   new(string(gwListener.Name)),
				UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
					Port:                  new(int(gwListener.Port)),
					Protocol:              new(string(gwListener.Protocol)),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
				},
			}).Return(loadbalancer.UpdateListenerResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fails when existing listener update fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			lbListener := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.Name = new(string(gwListener.Name))
				},
			)
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			defaultBackendSetName := faker.New().UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.New().UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   new(string(gwListener.Name)),
				UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
					Port:                  new(int(gwListener.Port)),
					Protocol:              new(string(gwListener.Protocol)),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
				},
			}).Return(loadbalancer.UpdateListenerResponse{}, wantErr).Once()

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when existing listener update has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			lbListener := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.Name = new(string(gwListener.Name))
				},
			)
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().
				UpdateListener(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateListenerResponse{}, nil)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("when listener exists no changes", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			lbListener := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.Name = new(string(gwListener.Name))
					l.Port = new(int(gwListener.Port))
					l.Protocol = new(string(gwListener.Protocol))
					l.DefaultBackendSetName = new(defaultBackendSetName)
					l.RoutingPolicyName = new(routingPolicyName)
				},
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
					fake.UUID().V4():        makeRandomOCIListener(),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)

			ociLoadBalancerClient.AssertNotCalled(t, "UpdateListener")
		})

		t.Run("when https listener exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			lbListener := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.Name = new(string(gwListener.Name))
				},
			)

			ociListenerCert := makeRandomOCICertificate()
			listenerCertificates := []loadbalancer.Certificate{
				ociListenerCert,
				makeRandomOCICertificate(), // first one should be used
			}

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
					fake.UUID().V4():        makeRandomOCIListener(),
				},
				listenerCertificates:  listenerCertificates,
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   new(string(gwListener.Name)),
				UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
					Port:                  new(int(gwListener.Port)),
					Protocol:              new("HTTP"),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
					SslConfiguration: &loadbalancer.SslConfigurationDetails{
						CertificateName: ociListenerCert.CertificateName,
					},
				},
			}).Return(loadbalancer.UpdateListenerResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fails when https listener has no certificate source", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := faker.New().UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.New().UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			err := model.reconcileHTTPListener(t.Context(), params)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, string(gatewayv1.GatewayConditionAccepted), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
			assert.Contains(
				t,
				statusErr.message,
				"requires certificateRefs or oci.oraclecloud.com/certificate-ocid TLS option",
			)
		})

		t.Run("when listener does not exist", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// For routing policy creation
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			routingPolicyWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), loadbalancer.CreateRoutingPolicyRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateRoutingPolicyDetails: loadbalancer.CreateRoutingPolicyDetails{
					Name:                     &routingPolicyName,
					ConditionLanguageVersion: loadbalancer.CreateRoutingPolicyDetailsConditionLanguageVersionV1,
					Rules: []loadbalancer.RoutingRule{
						{
							Name:      new("default_catch_all"),
							Condition: new("any(http.request.url.path sw '/')"),
							Actions: []loadbalancer.Action{
								loadbalancer.ForwardToBackendSet{
									BackendSetName: new(params.defaultBackendSetName),
								},
							},
						},
					},
				},
			}).Return(loadbalancer.CreateRoutingPolicyResponse{
				OpcWorkRequestId: &routingPolicyWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			// For listener creation
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  new(string(gwListener.Name)),
					Port:                  new(int(gwListener.Port)),
					Protocol:              new(string(gwListener.Protocol)),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &listenerWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(nil)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("when https listener does not exist", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)

			ociListenerCert := makeRandomOCICertificate()
			listenerCertificates := []loadbalancer.Certificate{
				ociListenerCert,
				makeRandomOCICertificate(), // first one should be used
			}

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				listenerCertificates:  listenerCertificates,
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// For routing policy creation
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			routingPolicyWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), loadbalancer.CreateRoutingPolicyRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateRoutingPolicyDetails: loadbalancer.CreateRoutingPolicyDetails{
					Name:                     &routingPolicyName,
					ConditionLanguageVersion: loadbalancer.CreateRoutingPolicyDetailsConditionLanguageVersionV1,
					Rules: []loadbalancer.RoutingRule{
						{
							Name:      new("default_catch_all"),
							Condition: new("any(http.request.url.path sw '/')"),
							Actions: []loadbalancer.Action{
								loadbalancer.ForwardToBackendSet{
									BackendSetName: new(params.defaultBackendSetName),
								},
							},
						},
					},
				},
			}).Return(loadbalancer.CreateRoutingPolicyResponse{
				OpcWorkRequestId: &routingPolicyWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			// For listener creation
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  new(string(gwListener.Name)),
					Port:                  new(int(gwListener.Port)),
					Protocol:              new("HTTP"),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
					SslConfiguration: &loadbalancer.SslConfigurationDetails{
						CertificateName: ociListenerCert.CertificateName,
					},
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &listenerWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(nil)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fails when https listener has no certificate source", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := faker.New().UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: faker.New().UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			err := model.reconcileHTTPListener(t.Context(), params)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, string(gatewayv1.GatewayConditionAccepted), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
			assert.Contains(
				t,
				statusErr.message,
				"requires certificateRefs or oci.oraclecloud.com/certificate-ocid TLS option",
			)
		})

		t.Run("when routing policy exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4(): makeRandomOCIRoutingPolicy(),
					routingPolicyName: {
						Name:                     new(routingPolicyName),
						ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
						Rules: []loadbalancer.RoutingRule{
							defaultCatchAllRoutingRule(defaultBackendSetName),
						},
					},
					fake.UUID().V4(): makeRandomOCIRoutingPolicy(),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// For listener creation
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  new(string(gwListener.Name)),
					Port:                  new(int(gwListener.Port)),
					Protocol:              new("HTTP"),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &listenerWorkRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(nil)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.NoError(t, err)
			ociLoadBalancerClient.AssertNotCalled(t, "CreateRoutingPolicy")
			ociLoadBalancerClient.AssertNotCalled(t, "UpdateRoutingPolicy")
		})

		t.Run("fails when created listener has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()
			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().
				CreateListener(t.Context(), mock.Anything).
				Return(loadbalancer.CreateListenerResponse{}, nil)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("fails when created routing policy has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID:        fake.UUID().V4(),
				knownRoutingPolicies:  map[string]loadbalancer.RoutingPolicy{},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().
				CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{}, nil)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
			ociLoadBalancerClient.AssertNotCalled(t, "CreateListener")
		})

		t.Run("updates routing policy default rule drift", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()
			existingRule := defaultCatchAllRoutingRule("wrong-" + fake.UUID().V4())
			extraRule := loadbalancer.RoutingRule{
				Name:      new("r" + fake.Lorem().Word()),
				Condition: new("any(http.request.url.path sw '/api')"),
				Actions: []loadbalancer.Action{
					loadbalancer.ForwardToBackendSet{BackendSetName: new("api-" + fake.UUID().V4())},
				},
			}
			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     new(routingPolicyName),
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				Rules:                    []loadbalancer.RoutingRule{extraRule, existingRule},
			}

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: existingPolicy,
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			policyWorkRequestID := fake.UUID().V4()
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					return assert.Equal(t, params.loadBalancerID, *req.LoadBalancerId) &&
						assert.Equal(t, routingPolicyName, *req.RoutingPolicyName) &&
						assert.ElementsMatch(
							t,
							[]loadbalancer.RoutingRule{
								extraRule,
								defaultCatchAllRoutingRule(defaultBackendSetName),
							},
							req.UpdateRoutingPolicyDetails.Rules,
						)
				}),
			).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &policyWorkRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), policyWorkRequestID).Return(nil).Once()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  new(string(gwListener.Name)),
					Port:                  new(int(gwListener.Port)),
					Protocol:              new("HTTP"),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &listenerWorkRequestID,
			}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(nil).Once()

			err := model.reconcileHTTPListener(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("restores missing routing policy default rule", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()
			extraRule := loadbalancer.RoutingRule{
				Name:      new("r" + fake.Lorem().Word()),
				Condition: new("any(http.request.url.path sw '/api')"),
				Actions: []loadbalancer.Action{
					loadbalancer.ForwardToBackendSet{BackendSetName: new("api-" + fake.UUID().V4())},
				},
			}
			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     new(routingPolicyName),
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				Rules:                    []loadbalancer.RoutingRule{extraRule},
			}

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: existingPolicy,
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			policyWorkRequestID := fake.UUID().V4()
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					return assert.ElementsMatch(
						t,
						[]loadbalancer.RoutingRule{
							extraRule,
							defaultCatchAllRoutingRule(defaultBackendSetName),
						},
						req.UpdateRoutingPolicyDetails.Rules,
					)
				}),
			).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &policyWorkRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), policyWorkRequestID).Return(nil).Once()

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), loadbalancer.CreateListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateListenerDetails: loadbalancer.CreateListenerDetails{
					Name:                  new(string(gwListener.Name)),
					Port:                  new(int(gwListener.Port)),
					Protocol:              new("HTTP"),
					DefaultBackendSetName: new(params.defaultBackendSetName),
					RoutingPolicyName:     new(routingPolicyName),
				},
			}).Return(loadbalancer.CreateListenerResponse{
				OpcWorkRequestId: &listenerWorkRequestID,
			}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(nil).Once()

			err := model.reconcileHTTPListener(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("fails when routing policy default rule update has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     new(routingPolicyName),
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				Rules:                    []loadbalancer.RoutingRule{defaultCatchAllRoutingRule(fake.UUID().V4())},
			}
			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: existingPolicy,
				},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().
				UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{}, nil)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
			ociLoadBalancerClient.AssertNotCalled(t, "CreateListener")
		})

		t.Run("when create routing policy fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{}, wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when wait for routing policy fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			routingPolicyWorkRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{
					OpcWorkRequestId: &routingPolicyWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when create listener fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			routingPolicyWorkRequestID := fake.UUID().V4()

			// Expect routing policy creation to succeed
			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{
					OpcWorkRequestId: &routingPolicyWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), mock.Anything).
				Return(loadbalancer.CreateListenerResponse{}, wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("when wait for listener fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				defaultBackendSetName: fake.UUID().V4(),
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			routingPolicyWorkRequestID := fake.UUID().V4()

			// Expect routing policy creation to succeed
			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.CreateRoutingPolicyResponse{
					OpcWorkRequestId: &routingPolicyWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), routingPolicyWorkRequestID).Return(nil)

			listenerWorkRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().CreateListener(t.Context(), mock.Anything).
				Return(loadbalancer.CreateListenerResponse{
					OpcWorkRequestId: &listenerWorkRequestID,
				}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), listenerWorkRequestID).Return(wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("reconcileBackendSet", func(t *testing.T) {
		t.Run("create new backend set", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()

			params := reconcileBackendSetParams{
				loadBalancerID: fake.UUID().V4(),
				service:        service,
			}

			wantBsName := ociBackendSetNameFromService(service)

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: new("TCP"),
						Port:     new(service.Spec.Ports[0].TargetPort.IntValue()),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
		})
		t.Run("create new backend set with no target port", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService(
				func(s *corev1.Service) {
					s.Spec.Ports[0].TargetPort = intstr.FromInt(0)
				},
			)

			params := reconcileBackendSetParams{
				loadBalancerID: fake.UUID().V4(),
				service:        service,
			}

			wantBsName := ociBackendSetNameFromService(service)

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: new("TCP"),
						Port:     new(int(service.Spec.Ports[0].Port)),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("do nothing if backend set exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			exitingBs := makeRandomOCIBackendSet(func(bs *loadbalancer.BackendSet) {
				bs.Name = new(wantBsName)
				bs.Policy = new("ROUND_ROBIN")
				bs.HealthChecker = &loadbalancer.HealthChecker{
					Protocol: new("TCP"),
					Port:     new(service.Spec.Ports[0].TargetPort.IntValue()),
				}
			})

			params := reconcileBackendSetParams{
				loadBalancerID: fake.UUID().V4(),
				service:        service,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: exitingBs,
			}, nil)

			err := model.reconcileBackendSet(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("updates backend set config drift", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			existingBs := makeRandomOCIBackendSet(randomOCIBackendSetWithNameOpt(wantBsName))

			params := reconcileBackendSetParams{
				loadBalancerID: fake.UUID().V4(),
				service:        service,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{
				BackendSet: existingBs,
			}, nil).Once()

			ociLoadBalancerClient.EXPECT().UpdateBackendSet(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.UpdateBackendSetRequest) bool {
					return assert.Equal(t, params.loadBalancerID, *req.LoadBalancerId) &&
						assert.Equal(t, wantBsName, *req.BackendSetName) &&
						assert.Equal(t, "ROUND_ROBIN", *req.Policy) &&
						assert.Equal(t, "TCP", *req.HealthChecker.Protocol) &&
						assert.Equal(t, service.Spec.Ports[0].TargetPort.IntValue(), *req.HealthChecker.Port)
				}),
			).Return(loadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			err := model.reconcileBackendSet(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("fails when get backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			params := reconcileBackendSetParams{
				loadBalancerID: faker.New().UUID().V4(),
				service:        service,
			}
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{}, wantErr).Once()

			err := model.reconcileBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("returns non not found backend set lookup errors", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			wantErr := ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(500))
			params := reconcileBackendSetParams{
				loadBalancerID: faker.New().UUID().V4(),
				service:        service,
			}
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(loadbalancer.GetBackendSetResponse{}, wantErr).Once()

			err := model.reconcileBackendSet(t.Context(), params)

			require.ErrorIs(t, err, wantErr)
			ociLoadBalancerClient.AssertNotCalled(t, "CreateBackendSet")
		})

		t.Run("fails when create backend set fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			params := reconcileBackendSetParams{
				loadBalancerID: faker.New().UUID().V4(),
				service:        service,
			}
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: new("TCP"),
						Port:     new(service.Spec.Ports[0].TargetPort.IntValue()),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{}, wantErr).Once()

			err := model.reconcileBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when create backend set has no work request id", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			params := reconcileBackendSetParams{
				loadBalancerID: faker.New().UUID().V4(),
				service:        service,
			}
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: new("TCP"),
						Port:     new(service.Spec.Ports[0].TargetPort.IntValue()),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{}, nil).Once()

			err := model.reconcileBackendSet(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("fails when create backend set wait fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()
			wantBsName := ociBackendSetNameFromService(service)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			params := reconcileBackendSetParams{
				loadBalancerID: faker.New().UUID().V4(),
				service:        service,
			}
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestID := faker.New().UUID().V4()

			ociLoadBalancerClient.EXPECT().GetBackendSet(t.Context(), loadbalancer.GetBackendSetRequest{
				BackendSetName: &wantBsName,
				LoadBalancerId: &params.loadBalancerID,
			}).Return(
				loadbalancer.GetBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
			).Once()

			ociLoadBalancerClient.EXPECT().CreateBackendSet(t.Context(), loadbalancer.CreateBackendSetRequest{
				LoadBalancerId: &params.loadBalancerID,
				CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
					Name: &wantBsName,
					HealthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: new("TCP"),
						Port:     new(service.Spec.Ports[0].TargetPort.IntValue()),
					},
					Policy: new("ROUND_ROBIN"),
				},
			}).Return(loadbalancer.CreateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			err := model.reconcileBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("removeMissingListeners", func(t *testing.T) {
		t.Run("no listeners to remove", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			gwListener1 := makeRandomListener()
			gwListener2 := makeRandomListener()

			lbListener1 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(gwListener1.Name))
			})
			lbListener2 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(gwListener2.Name))
			})

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListener1.Name: lbListener1,
					*lbListener2.Name: lbListener2,
				},
				gatewayListeners: []gatewayv1.Listener{
					gwListener1,
					gwListener2,
				},
			}

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("some listeners to remove", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			gwListener1 := makeRandomListener()
			gwListener2 := makeRandomListener()
			lbListener1 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(gwListener1.Name))
			})
			lbListener2 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(gwListener2.Name))
			})
			lbListenerToRemove1 := makeRandomOCIListener()
			lbListenerToRemove2 := makeRandomOCIListener()

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListener1.Name:         lbListener1,
					*lbListener2.Name:         lbListener2,
					*lbListenerToRemove1.Name: lbListenerToRemove1,
					*lbListenerToRemove2.Name: lbListenerToRemove2,
				},
				gatewayListeners: []gatewayv1.Listener{
					gwListener1,
					gwListener2,
				},
			}

			// Expect deletion for both missing listeners
			workRequestID1 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove1.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID1}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID1).Return(nil).Once()

			workRequestID2 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove2.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID2}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("some listeners to remove with routing policy", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			gwListener1 := makeRandomListener()
			gwListener2 := makeRandomListener()
			lbListener1 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(gwListener1.Name))
			})
			lbListener2 := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(gwListener2.Name))
			})
			lbListenerToRemove1 := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.RoutingPolicyName = new("policy1" + fake.Internet().Domain())
				},
			)
			lbListenerToRemove2 := makeRandomOCIListener(
				func(l *loadbalancer.Listener) {
					l.RoutingPolicyName = new("policy2" + fake.Internet().Domain())
				},
			)

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListener1.Name:         lbListener1,
					*lbListener2.Name:         lbListener2,
					*lbListenerToRemove1.Name: lbListenerToRemove1,
					*lbListenerToRemove2.Name: lbListenerToRemove2,
				},
				gatewayListeners: []gatewayv1.Listener{
					gwListener1,
					gwListener2,
				},
			}

			deletePolicyRequestID1 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove1.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID1}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID1).Return(nil).Once()

			deletePolicyRequestID2 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove2.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID2}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID2).Return(nil).Once()

			// Expect deletion for both missing listeners
			deleteListenerRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove1.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteListenerRequestID).Return(nil).Once()

			workRequestID2 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove2.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID2}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fail when delete listener fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			lbListenerToRemove := makeRandomOCIListener()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{}, wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when wait for listener fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			lbListenerToRemove := makeRandomOCIListener()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when listener delete has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			lbListenerToRemove := makeRandomOCIListener()
			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{}, nil).Once()

			err := model.removeMissingListeners(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("continues deleting even if one fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			lbListenerToRemove1 := makeRandomOCIListener()
			lbListenerToRemove2 := makeRandomOCIListener() // This one succeeds
			lbListenerToRemove3 := makeRandomOCIListener() // This one fails during wait

			wantErr1 := errors.New(fake.Lorem().Sentence(10))
			wantErr3 := errors.New(fake.Lorem().Sentence(10))

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove1.Name: lbListenerToRemove1,
					*lbListenerToRemove2.Name: lbListenerToRemove2,
					*lbListenerToRemove3.Name: lbListenerToRemove3,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			// Expect deletion attempt for all three
			// 1. Fails on delete call
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove1.Name,
			}).Return(loadbalancer.DeleteListenerResponse{}, wantErr1).Once()

			// 2. Succeeds fully
			workRequestID2 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove2.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID2}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			// 3. Fails on wait
			workRequestID3 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove3.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID3}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID3).Return(wantErr3).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err) // Should return combined error
			require.ErrorIs(t, err, wantErr1)
			require.ErrorIs(t, err, wantErr3)
		})

		t.Run("fails when routing policy delete fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			lbListenerToRemove := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.RoutingPolicyName = new(listenerPolicyName(lo.FromPtr(l.Name)))
			})
			wantErr := errors.New(faker.New().Lorem().Sentence(10))

			params := removeMissingListenersParams{
				loadBalancerID: faker.New().UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			deleteListenerRequestID := faker.New().UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteListenerRequestID).Return(nil).Once()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{}, wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when routing policy delete wait fails", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			lbListenerToRemove := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.RoutingPolicyName = new(listenerPolicyName(lo.FromPtr(l.Name)))
			})
			wantErr := errors.New(faker.New().Lorem().Sentence(10))

			params := removeMissingListenersParams{
				loadBalancerID: faker.New().UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			deleteListenerRequestID := faker.New().UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteListenerRequestID).Return(nil).Once()
			deletePolicyRequestID := faker.New().UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID).Return(wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when routing policy delete has no work request id", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			lbListenerToRemove := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.RoutingPolicyName = new(listenerPolicyName(lo.FromPtr(l.Name)))
			})

			params := removeMissingListenersParams{
				loadBalancerID: faker.New().UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*lbListenerToRemove.Name: lbListenerToRemove,
				},
				gatewayListeners: []gatewayv1.Listener{},
			}

			deleteListenerRequestID := faker.New().UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   lbListenerToRemove.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &deleteListenerRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteListenerRequestID).Return(nil).Once()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: lbListenerToRemove.RoutingPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{}, nil).Once()

			err := model.removeMissingListeners(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})
	})

	t.Run("makeRoutingRule", func(t *testing.T) {
		t.Run("successfully create a routing rule", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			refs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(refs...),
					),
				),
			)
			ruleIndex := 0

			params := makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
			}

			expectedCondition := fake.Lorem().Sentence(10)
			routingRulesMapper.EXPECT().mapHTTPRouteHostnamesAndMatchesToCondition(
				httpRoute.Spec.Hostnames,
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return(expectedCondition, nil).Once()

			expectedRuleName := ociListerPolicyRuleName(httpRoute, ruleIndex)
			expectedBackendSets := lo.Map(refs, func(ref gatewayv1.HTTPBackendRef, _ int) string {
				return ociBackendSetNameFromBackendRef(httpRoute, ref)
			})

			expectedRule := loadbalancer.RoutingRule{
				Name:      new(expectedRuleName),
				Condition: new(expectedCondition),
				Actions: lo.Map(expectedBackendSets, func(backendSet string, _ int) loadbalancer.Action {
					return loadbalancer.ForwardToBackendSet{
						BackendSetName: new(backendSet),
					}
				}),
			}

			actualRule, err := model.makeRoutingRule(t.Context(), params)
			require.NoError(t, err)
			assert.Equal(t, expectedRule, actualRule)
		})

		t.Run("includes route hostname in routing rule condition", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			deps.RoutingRulesMapper = newOciLoadBalancerRoutingRulesMapper()
			model := newOciLoadBalancerModel(deps)

			hostname := gatewayv1.Hostname("auth-" + fake.Internet().Domain())
			pathValue := "/"
			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					gatewayv1.HTTPRouteRule{
						Matches: []gatewayv1.HTTPRouteMatch{
							{
								Path: &gatewayv1.HTTPPathMatch{
									Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
									Value: &pathValue,
								},
							},
						},
						BackendRefs: []gatewayv1.HTTPBackendRef{backendRef},
					},
				),
			)
			httpRoute.Spec.Hostnames = []gatewayv1.Hostname{hostname}

			actualRule, err := model.makeRoutingRule(t.Context(), makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: 0,
			})

			require.NoError(t, err)
			condition := lo.FromPtr(actualRule.Condition)
			assert.Contains(t, condition, "all(")
			assert.Contains(t, condition, "http.request.headers[(i 'host')]")
			assert.Contains(t, condition, fmt.Sprintf("eq (i '%s')", hostname))
			assert.Contains(t, condition, fmt.Sprintf("http.request.url.path sw '%s'", pathValue))
		})

		t.Run("fail when mapping matches to condition fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(makeRandomHTTPRouteRule()),
			)
			ruleIndex := 0

			params := makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
			}

			expectedErr := errors.New(fake.Lorem().Sentence(10))
			routingRulesMapper.EXPECT().mapHTTPRouteHostnamesAndMatchesToCondition(
				httpRoute.Spec.Hostnames,
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return("", expectedErr).Once()

			_, err := model.makeRoutingRule(t.Context(), params)
			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("makeGRPCRoutingRule", func(t *testing.T) {
		t.Run("uses a route-kind-specific rule name", func(t *testing.T) {
			namespace := "default"
			name := "shared-route-name"
			ruleIndex := 0

			httpRoute := gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      name,
				},
				Spec: gatewayv1.HTTPRouteSpec{
					Rules: []gatewayv1.HTTPRouteRule{{}},
				},
			}
			grpcRoute := gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      name,
				},
				Spec: gatewayv1.GRPCRouteSpec{
					Rules: []gatewayv1.GRPCRouteRule{{}},
				},
			}

			assert.NotEqual(
				t,
				ociListerPolicyRuleName(httpRoute, ruleIndex),
				ociGRPCListenerPolicyRuleName(grpcRoute, ruleIndex),
			)
		})

		t.Run("successfully creates a grpc routing rule", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)

			refs := []gatewayv1.GRPCBackendRef{
				{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName("svc-" + fake.Lorem().Word() + "-a"),
					Port: lo.ToPtr(gatewayv1.PortNumber(50051)),
				}}},
				{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName("svc-" + fake.Lorem().Word() + "-b"),
					Port: lo.ToPtr(gatewayv1.PortNumber(50052)),
				}}},
			}
			methodService := fmt.Sprintf("%s.%s", fake.Lorem().Word(), fake.Lorem().Word())
			methodName := fake.Lorem().Word()
			grpcRoute := gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "ns-" + fake.Lorem().Word(),
					Name:      "grpc-" + fake.Lorem().Word(),
				},
				Spec: gatewayv1.GRPCRouteSpec{
					Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname("grpc-" + fake.Internet().Domain())},
					Rules: []gatewayv1.GRPCRouteRule{
						{
							Matches: []gatewayv1.GRPCRouteMatch{
								{Method: &gatewayv1.GRPCMethodMatch{
									Service: &methodService,
									Method:  &methodName,
								}},
							},
							BackendRefs: refs,
						},
					},
				},
			}
			ruleIndex := 0

			expectedCondition := fake.Lorem().Sentence(10)
			routingRulesMapper.EXPECT().mapGRPCRouteHostnamesAndMatchesToCondition(
				grpcRoute.Spec.Hostnames,
				grpcRoute.Spec.Rules[ruleIndex].Matches,
			).Return(expectedCondition, nil).Once()

			expectedRuleName := ociGRPCListenerPolicyRuleName(grpcRoute, ruleIndex)
			expectedBackendSets := lo.Map(refs, func(ref gatewayv1.GRPCBackendRef, _ int) string {
				return ociBackendSetNameFromGRPCBackendRef(grpcRoute, ref)
			})
			expectedRule := loadbalancer.RoutingRule{
				Name:      new(expectedRuleName),
				Condition: new(expectedCondition),
				Actions: lo.Map(expectedBackendSets, func(backendSet string, _ int) loadbalancer.Action {
					return loadbalancer.ForwardToBackendSet{
						BackendSetName: new(backendSet),
					}
				}),
			}

			actualRule, err := model.makeGRPCRoutingRule(t.Context(), makeGRPCRoutingRuleParams{
				grpcRoute:          grpcRoute,
				grpcRouteRuleIndex: ruleIndex,
			})

			require.NoError(t, err)
			assert.Equal(t, expectedRule, actualRule)
		})

		t.Run("fails when grpc match mapping fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			routingRulesMapper, _ := deps.RoutingRulesMapper.(*MockociLoadBalancerRoutingRulesMapper)
			grpcRoute := gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "ns-" + fake.Lorem().Word(),
					Name:      "grpc-" + fake.Lorem().Word(),
				},
				Spec: gatewayv1.GRPCRouteSpec{
					Rules: []gatewayv1.GRPCRouteRule{{}},
				},
			}
			wantErr := errors.New(fake.Lorem().Sentence(10))
			routingRulesMapper.EXPECT().mapGRPCRouteHostnamesAndMatchesToCondition(
				grpcRoute.Spec.Hostnames,
				grpcRoute.Spec.Rules[0].Matches,
			).Return("", wantErr).Once()

			_, err := model.makeGRPCRoutingRule(t.Context(), makeGRPCRoutingRuleParams{
				grpcRoute:          grpcRoute,
				grpcRouteRuleIndex: 0,
			})

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("commitRoutingPolicy", func(t *testing.T) {
		t.Run("successfully merge and update routing policy", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)

			existingRulePrefixes := []string{
				"routes-1",
				"routes-2",
				"routes-3",
				"routes-4",
			}

			existingRules := lo.Map(existingRulePrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      new(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: new(fake.Lorem().Sentence(10)),
				}
			})

			existingRules = append(existingRules, loadbalancer.RoutingRule{
				Name:      new(string(defaultCatchAllRuleName)),
				Condition: new(fake.Lorem().Sentence(10)),
			})

			newRulesPrefixes := []string{
				"new-routes-1",
				"new-routes-2",
				"new-routes-3",
			}
			newRules := lo.Map(newRulesPrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      new(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: new(fake.Lorem().Sentence(10)),
				}
			})

			replacedRuleIndex := rand.IntN(len(existingRulePrefixes))
			replacedRule := loadbalancer.RoutingRule{
				Name:      new(fmt.Sprintf("%s%04d", existingRulePrefixes[replacedRuleIndex], replacedRuleIndex)),
				Condition: new(fake.Lorem().Sentence(10)),
			}

			rulesToCommit := make([]loadbalancer.RoutingRule, 0, len(existingRules)+len(newRules))
			rulesToCommit = append(rulesToCommit, existingRules...)
			rulesToCommit[replacedRuleIndex] = replacedRule
			rulesToCommit = append(rulesToCommit, newRules...)

			wantMergedRules := make([]loadbalancer.RoutingRule, 0, len(rulesToCommit))
			wantMergedRules = append(wantMergedRules, rulesToCommit...)

			// Sort the expected rules
			sort.Slice(wantMergedRules, func(i, j int) bool {
				ruleI := lo.FromPtr(wantMergedRules[i].Name)
				ruleJ := lo.FromPtr(wantMergedRules[j].Name)
				if ruleI == defaultCatchAllRuleName {
					return false
				}
				if ruleJ == defaultCatchAllRuleName {
					return true
				}
				return ruleI < ruleJ
			})

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    rulesToCommit,
			}

			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     new(policyName),
				Rules:                    existingRules,
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
			}

			// Expect to get the current routing policy
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			// Expect to update the policy with merged rules
			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), loadbalancer.UpdateRoutingPolicyRequest{
				LoadBalancerId:    &loadBalancerID,
				RoutingPolicyName: new(policyName),
				UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
					ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
						existingPolicy.ConditionLanguageVersion,
					),
					Rules: wantMergedRules,
				},
			}).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("delete previously programmed rules that are not in the new policy", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)

			existingRulePrefixes := []string{
				"routes-1",
				"routes-2",
				"routes-3",
				"routes-4",
			}

			deletedRulePrefixes := []string{
				"deleted-routes-1",
				"deleted-routes-2",
				"deleted-routes-3",
			}

			deletedRules := lo.Map(deletedRulePrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      new(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: new(fake.Lorem().Sentence(10)),
				}
			})

			existingRules := lo.Map(existingRulePrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      new(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: new(fake.Lorem().Sentence(10)),
				}
			})

			existingRules = append(existingRules, loadbalancer.RoutingRule{
				Name:      new(string(defaultCatchAllRuleName)),
				Condition: new(fake.Lorem().Sentence(10)),
			})

			newRulesPrefixes := []string{
				"new-routes-1",
				"new-routes-2",
				"new-routes-3",
			}
			newRules := lo.Map(newRulesPrefixes, func(prefix string, i int) loadbalancer.RoutingRule {
				return loadbalancer.RoutingRule{
					Name:      new(fmt.Sprintf("%s%04d", prefix, i)),
					Condition: new(fake.Lorem().Sentence(10)),
				}
			})

			replacedRuleIndex := rand.IntN(len(existingRulePrefixes))
			replacedRule := loadbalancer.RoutingRule{
				Name:      new(fmt.Sprintf("%s%04d", existingRulePrefixes[replacedRuleIndex], replacedRuleIndex)),
				Condition: new(fake.Lorem().Sentence(10)),
			}

			rulesToCommit := make([]loadbalancer.RoutingRule, 0, len(existingRules)+len(newRules))
			rulesToCommit = append(rulesToCommit, existingRules...)
			rulesToCommit[replacedRuleIndex] = replacedRule
			rulesToCommit = append(rulesToCommit, newRules...)

			wantMergedRules := make([]loadbalancer.RoutingRule, 0, len(rulesToCommit))
			wantMergedRules = append(wantMergedRules, rulesToCommit...)

			// Sort the expected rules
			sort.Slice(wantMergedRules, func(i, j int) bool {
				ruleI := lo.FromPtr(wantMergedRules[i].Name)
				ruleJ := lo.FromPtr(wantMergedRules[j].Name)
				if ruleI == defaultCatchAllRuleName {
					return false
				}
				if ruleJ == defaultCatchAllRuleName {
					return true
				}
				return ruleI < ruleJ
			})

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    rulesToCommit,
				prevPolicyRules: lo.Map(deletedRules, func(rule loadbalancer.RoutingRule, _ int) string {
					return lo.FromPtr(rule.Name)
				}),
			}

			allExistingRules := make([]loadbalancer.RoutingRule, 0, len(existingRules)+len(deletedRules))
			allExistingRules = append(allExistingRules, existingRules...)
			allExistingRules = append(allExistingRules, deletedRules...)

			existingPolicy := loadbalancer.RoutingPolicy{
				Name:                     new(policyName),
				Rules:                    allExistingRules,
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
			}

			// Expect to get the current routing policy
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			// Expect to update the policy with merged rules
			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.MatchedBy(
				func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					assert.Equal(t, wantMergedRules, req.UpdateRoutingPolicyDetails.Rules)
					return true
				},
			)).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("skips update when routing policy already matches desired rules", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			defaultRule := defaultCatchAllRoutingRule("default-" + fake.Lorem().Word())
			firstRule := makeRandomOCIRoutingRule()
			firstRule.Name = new("first-" + fake.Lorem().Word())
			secondRule := makeRandomOCIRoutingRule()
			secondRule.Name = new("second-" + fake.Lorem().Word())
			existingRules := []loadbalancer.RoutingRule{defaultRule, secondRule, firstRule}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: loadbalancer.RoutingPolicy{
					Name:                     new(policyName),
					Rules:                    existingRules,
					ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				},
			}, nil)

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    []loadbalancer.RoutingRule{secondRule, firstRule},
			})
			require.NoError(t, err)
			ociLoadBalancerClient.AssertNotCalled(t, "UpdateRoutingPolicy")
		})

		t.Run("fail when get routing policy fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)

			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			}

			wantErr := errors.New(fake.Lorem().Sentence(10))
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{}, wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when update routing policy fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)

			existingPolicy := makeRandomOCIRoutingPolicy(
				func(policy *loadbalancer.RoutingPolicy) {
					policy.Name = new(policyName)
				},
			)

			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			wantErr := errors.New(fake.Lorem().Sentence(10))
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{}, wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fail when update routing policy has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			existingPolicy := makeRandomOCIRoutingPolicy(
				func(policy *loadbalancer.RoutingPolicy) {
					policy.Name = new(policyName)
				},
			)

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{}, nil)

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    []loadbalancer.RoutingRule{makeRandomOCIRoutingRule()},
			})

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("fail when wait for update fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)

			existingPolicy := makeRandomOCIRoutingPolicy(
				func(policy *loadbalancer.RoutingPolicy) {
					policy.Name = new(policyName)
				},
			)

			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
			}

			params := commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{
					OpcWorkRequestId: &workRequestID,
				}, nil)

			wantErr := errors.New(fake.Lorem().Sentence(10))
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

			err := model.commitRoutingPolicy(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("deprovisionBackendSet", func(t *testing.T) {
		t.Run("successfully deprovisions an existing backend set", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)
			workRequestID := fake.UUID().V4()

			params := deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				httpRoute:      httpRoute,
				backendRef:     backendRef,
			}

			ociLoadBalancerClient.EXPECT().DeleteBackendSet(t.Context(), loadbalancer.DeleteBackendSetRequest{
				LoadBalancerId: &loadBalancerID,
				BackendSetName: &backendSetName,
			}).Return(loadbalancer.DeleteBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			err := model.deprovisionBackendSet(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("returns error if delete backend set fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)
			wantErr := errors.New(fake.Lorem().Sentence(10))

			params := deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				httpRoute:      httpRoute,
				backendRef:     backendRef,
			}

			ociLoadBalancerClient.EXPECT().DeleteBackendSet(t.Context(), loadbalancer.DeleteBackendSetRequest{
				LoadBalancerId: &loadBalancerID,
				BackendSetName: &backendSetName,
			}).Return(loadbalancer.DeleteBackendSetResponse{}, wantErr).Once()

			err := model.deprovisionBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if waiting for deletion fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)
			workRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			params := deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				httpRoute:      httpRoute,
				backendRef:     backendRef,
			}

			ociLoadBalancerClient.EXPECT().DeleteBackendSet(t.Context(), loadbalancer.DeleteBackendSetRequest{
				LoadBalancerId: &loadBalancerID,
				BackendSetName: &backendSetName,
			}).Return(loadbalancer.DeleteBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			err := model.deprovisionBackendSet(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error if delete backend set has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)

			params := deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				httpRoute:      httpRoute,
				backendRef:     backendRef,
			}

			ociLoadBalancerClient.EXPECT().DeleteBackendSet(t.Context(), loadbalancer.DeleteBackendSetRequest{
				LoadBalancerId: &loadBalancerID,
				BackendSetName: &backendSetName,
			}).Return(loadbalancer.DeleteBackendSetResponse{}, nil).Once()

			err := model.deprovisionBackendSet(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("succeeds if backend set does not exist (404 on delete)", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)

			params := deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				httpRoute:      httpRoute,
				backendRef:     backendRef,
			}

			ociLoadBalancerClient.EXPECT().DeleteBackendSet(t.Context(), loadbalancer.DeleteBackendSetRequest{
				LoadBalancerId: &loadBalancerID,
				BackendSetName: &backendSetName,
			}).Return(
				loadbalancer.DeleteBackendSetResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404))).Once()

			err := model.deprovisionBackendSet(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("succeeds if backend set is used in routing policy (400 InvalidParameter)", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef()
			backendSetName := ociBackendSetNameFromBackendRef(httpRoute, backendRef)

			params := deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				httpRoute:      httpRoute,
				backendRef:     backendRef,
			}

			serviceErr := ociapi.NewRandomServiceError(
				ociapi.RandomServiceErrorWithStatusCode(400),
				ociapi.RandomServiceErrorWithCode("InvalidParameter"),
				ociapi.RandomServiceErrorWithMessage("Backend set is used in routing policy"),
			)

			ociLoadBalancerClient.EXPECT().DeleteBackendSet(t.Context(), loadbalancer.DeleteBackendSetRequest{
				LoadBalancerId: &loadBalancerID,
				BackendSetName: &backendSetName,
			}).Return(loadbalancer.DeleteBackendSetResponse{}, serviceErr).Once()

			err := model.deprovisionBackendSet(t.Context(), params)
			require.NoError(t, err)
		})
	})

	t.Run("removeUnusedCertificates", func(t *testing.T) {
		makeManagedCertificate := func(namespace, name, resourceVersion string) loadbalancer.Certificate {
			certName := fmt.Sprintf("%s-%s-rev-%s", namespace, name, resourceVersion)
			cert := makeRandomOCICertificate()
			cert.CertificateName = new(certName)
			return cert
		}

		t.Run("no certificates to remove", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			// Create some certificates that are used by listeners
			cert1 := makeRandomOCICertificate()
			cert2 := makeRandomOCICertificate()
			cert3 := makeRandomOCICertificate()

			knownCertificates := map[string]loadbalancer.Certificate{
				*cert1.CertificateName: cert1,
				*cert2.CertificateName: cert2,
				*cert3.CertificateName: cert3,
			}

			// Create listener certificates map showing all certificates are in use
			listenerCertificates := map[string][]loadbalancer.Certificate{
				"listener1": {cert1, cert2},
				"listener2": {cert3},
			}

			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(cert1.CertificateName),
					lo.FromPtr(cert2.CertificateName),
					lo.FromPtr(cert3.CertificateName),
				},
				desiredCertificates: certificateNamesFromListenerCertificates(listenerCertificates),
				knownCertificates:   knownCertificates,
			}

			err := model.removeUnusedCertificates(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("removes unused certificates", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// Create certificates, some used and some unused
			usedCert1 := makeRandomOCICertificate()
			usedCert2 := makeRandomOCICertificate()
			unusedCert1 := makeManagedCertificate("default", "unused-one", fake.UUID().V4())
			unusedCert2 := makeManagedCertificate("default", "unused-two", fake.UUID().V4())

			knownCertificates := map[string]loadbalancer.Certificate{
				*usedCert1.CertificateName:   usedCert1,
				*usedCert2.CertificateName:   usedCert2,
				*unusedCert1.CertificateName: unusedCert1,
				*unusedCert2.CertificateName: unusedCert2,
			}

			// Only used certificates are referenced by listeners
			listenerCertificates := map[string][]loadbalancer.Certificate{
				"listener1": {usedCert1},
				"listener2": {usedCert2},
			}

			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(usedCert1.CertificateName),
					lo.FromPtr(usedCert2.CertificateName),
					lo.FromPtr(unusedCert1.CertificateName),
					lo.FromPtr(unusedCert2.CertificateName),
				},
				desiredCertificates: certificateNamesFromListenerCertificates(listenerCertificates),
				knownCertificates:   knownCertificates,
			}

			// Expect deletion of unused certificates
			workRequestID1 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert1.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{
				OpcWorkRequestId: &workRequestID1,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID1).Return(nil).Once()

			workRequestID2 := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert2.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{
				OpcWorkRequestId: &workRequestID2,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID2).Return(nil).Once()

			err := model.removeUnusedCertificates(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("preserves unused certificates not previously programmed by the controller", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			usedCert := makeRandomOCICertificate()
			externalUnusedCert := makeRandomOCICertificate()
			externalUnusedCertWithRev := makeRandomOCICertificate()
			externalUnusedCertWithRev.CertificateName = new("external-rev-certificate")

			err := model.removeUnusedCertificates(t.Context(), removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				desiredCertificates: certificateNamesFromListenerCertificates(map[string][]loadbalancer.Certificate{
					"listener1": {usedCert},
				}),
				knownCertificates: map[string]loadbalancer.Certificate{
					lo.FromPtr(usedCert.CertificateName):                  usedCert,
					lo.FromPtr(externalUnusedCert.CertificateName):        externalUnusedCert,
					lo.FromPtr(externalUnusedCertWithRev.CertificateName): externalUnusedCertWithRev,
				},
			})

			require.NoError(t, err)
			ociLoadBalancerClient.AssertNotCalled(t, "DeleteCertificate")
		})

		t.Run("continues when certificate delete has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			usedCert := makeRandomOCICertificate()
			unusedCert := makeManagedCertificate("default", "unused", fake.UUID().V4())

			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(usedCert.CertificateName),
					lo.FromPtr(unusedCert.CertificateName),
				},
				desiredCertificates: certificateNamesFromListenerCertificates(map[string][]loadbalancer.Certificate{
					"listener1": {usedCert},
				}),
				knownCertificates: map[string]loadbalancer.Certificate{
					*usedCert.CertificateName:   usedCert,
					*unusedCert.CertificateName: unusedCert,
				},
			}

			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{}, nil).Once()

			err := model.removeUnusedCertificates(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("continues deletion even if one fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// Create certificates, some used and some unused
			usedCert := makeRandomOCICertificate()
			unusedCert1 := makeManagedCertificate("default", "unused-one", fake.UUID().V4()) // This one will fail
			unusedCert2 := makeManagedCertificate("default", "unused-two", fake.UUID().V4()) // This one will succeed

			knownCertificates := map[string]loadbalancer.Certificate{
				*usedCert.CertificateName:    usedCert,
				*unusedCert1.CertificateName: unusedCert1,
				*unusedCert2.CertificateName: unusedCert2,
			}

			listenerCertificates := map[string][]loadbalancer.Certificate{
				"listener1": {usedCert},
			}

			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(usedCert.CertificateName),
					lo.FromPtr(unusedCert1.CertificateName),
					lo.FromPtr(unusedCert2.CertificateName),
				},
				desiredCertificates: certificateNamesFromListenerCertificates(listenerCertificates),
				knownCertificates:   knownCertificates,
			}

			// First certificate deletion fails
			wantErr := errors.New(fake.Lorem().Sentence(10))
			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.DeleteCertificateRequest) bool {
					return assert.Equal(t, params.loadBalancerID, *req.LoadBalancerId) &&
						(lo.FromPtr(req.CertificateName) == lo.FromPtr(unusedCert1.CertificateName) ||
							lo.FromPtr(req.CertificateName) == lo.FromPtr(unusedCert2.CertificateName))
				}),
			).RunAndReturn(
				func(
					_ context.Context,
					req loadbalancer.DeleteCertificateRequest,
				) (loadbalancer.DeleteCertificateResponse, error) {
					if lo.FromPtr(req.CertificateName) == lo.FromPtr(unusedCert1.CertificateName) {
						return loadbalancer.DeleteCertificateResponse{}, wantErr
					}
					return loadbalancer.DeleteCertificateResponse{
						OpcWorkRequestId: &workRequestID,
					}, nil
				},
			).Twice()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			err := model.removeUnusedCertificates(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("handles wait failure", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// Create certificates, some used and some unused
			usedCert := makeRandomOCICertificate()
			unusedCert := makeManagedCertificate("default", "unused", fake.UUID().V4())

			knownCertificates := map[string]loadbalancer.Certificate{
				*usedCert.CertificateName:   usedCert,
				*unusedCert.CertificateName: unusedCert,
			}

			listenerCertificates := map[string][]loadbalancer.Certificate{
				"listener1": {usedCert},
			}

			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(usedCert.CertificateName),
					lo.FromPtr(unusedCert.CertificateName),
				},
				desiredCertificates: certificateNamesFromListenerCertificates(listenerCertificates),
				knownCertificates:   knownCertificates,
			}

			workRequestID := fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()

			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

			err := model.removeUnusedCertificates(t.Context(), params)
			require.NoError(t, err)
		})
	})
}

func TestOciLoadBalancerModelOCICertificateIDs(t *testing.T) {
	withOCICertificateOption := func(listener gatewayv1.Listener, certificateID string) gatewayv1.Listener {
		listener.TLS = &gatewayv1.ListenerTLSConfig{
			Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
				gatewayv1.AnnotationKey(ListenerTLSOptionOCICertificateOCID): gatewayv1.AnnotationValue(certificateID),
			},
		}
		return listener
	}

	t.Run("reconcileListenersCertificates returns OCI certificate IDs without reading secrets", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		gateway := gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{withOCICertificateOption(gatewayv1.Listener{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
				}, "ocid1.certificate.oc1..test")},
			},
		}

		result, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID:    faker.New().UUID().V4(),
			gateway:           &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{},
		})

		require.NoError(t, err)
		assert.Empty(t, result.certificatesByListener)
		assert.Empty(t, result.reconciledCertificates)
		assert.Equal(t, "ocid1.certificate.oc1..test", result.certificateIDsByListener["https"])
	})

	t.Run("reconcileListenersCertificates supports mixed OCI IDs and Kubernetes Secrets", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		secretListener := gatewayv1.Listener{
			Name:     "secret-https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     8443,
			TLS: &gatewayv1.ListenerTLSConfig{
				CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "secret-cert"}},
			},
		}
		ociListener := gatewayv1.Listener{
			Name:     "oci-https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     443,
		}
		ociListener = withOCICertificateOption(ociListener, "ocid1.certificate.oc1..test")
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{secretListener, ociListener},
			},
		}
		secret := makeRandomSecret()
		secret.Namespace = gateway.Namespace
		secret.Name = "secret-cert"
		certName := ociCertificateNameFromSecret(secret)
		knownCertificate := makeRandomOCICertificate()

		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		setupClientGet(t, k8sClient, types.NamespacedName{
			Namespace: gateway.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		result, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID: faker.New().UUID().V4(),
			gateway:        &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{
				certName: knownCertificate,
			},
		})

		require.NoError(t, err)
		assert.Equal(t, "ocid1.certificate.oc1..test", result.certificateIDsByListener["oci-https"])
		assert.Equal(t, []loadbalancer.Certificate{knownCertificate}, result.certificatesByListener["secret-https"])
		assert.Equal(t, map[string]loadbalancer.Certificate{certName: knownCertificate}, result.reconciledCertificates)
	})

	t.Run("reconcileListenersCertificates deduplicates listener certificateRefs", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{
							{Name: "tls-secret"},
							{Name: "tls-secret"},
						},
					},
				}},
			},
		}
		secret := makeRandomSecret(
			randomSecretWithNameOpt("tls-secret"),
			randomSecretWithTLSDataOpt(),
		)
		secret.Namespace = gateway.Namespace
		certName := ociCertificateNameFromSecret(secret)
		knownCertificate := makeRandomOCICertificate()

		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		setupClientGet(t, k8sClient, types.NamespacedName{
			Namespace: gateway.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		result, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID: faker.New().UUID().V4(),
			gateway:        &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{
				certName: knownCertificate,
			},
		})

		require.NoError(t, err)
		assert.Equal(t, []loadbalancer.Certificate{knownCertificate}, result.certificatesByListener["https"])
	})

	t.Run("reconcileListenersCertificates returns Kubernetes Secret lookup errors", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-secret"}},
					},
				}},
			},
		}
		wantErr := errors.New("k8s unavailable")
		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		k8sClient.EXPECT().
			Get(t.Context(), types.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      "tls-secret",
			}, mock.Anything).
			Return(wantErr).
			Once()

		_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID:    faker.New().UUID().V4(),
			gateway:           &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{},
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get secret tls-secret")
	})

	t.Run("reconcileListenersCertificates returns OCI certificate create errors", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-secret"}},
					},
				}},
			},
		}
		secret := makeRandomSecret(
			randomSecretWithNameOpt("tls-secret"),
			randomSecretWithTLSDataOpt(),
		)
		secret.Namespace = gateway.Namespace
		loadBalancerID := faker.New().UUID().V4()
		certName := ociCertificateNameFromSecret(secret)
		wantErr := errors.New("create failed")

		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		setupClientGet(t, k8sClient, types.NamespacedName{
			Namespace: gateway.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
		ociLoadBalancerClient.EXPECT().
			CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
				},
			}).
			Return(loadbalancer.CreateCertificateResponse{}, wantErr).
			Once()

		_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID:    loadBalancerID,
			gateway:           &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{},
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to create certificate")
	})

	t.Run("reconcileListenersCertificates returns OCI work request wait errors", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-secret"}},
					},
				}},
			},
		}
		secret := makeRandomSecret(
			randomSecretWithNameOpt("tls-secret"),
			randomSecretWithTLSDataOpt(),
		)
		secret.Namespace = gateway.Namespace
		loadBalancerID := faker.New().UUID().V4()
		workRequestID := faker.New().UUID().V4()
		certName := ociCertificateNameFromSecret(secret)
		wantErr := errors.New("wait failed")

		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		setupClientGet(t, k8sClient, types.NamespacedName{
			Namespace: gateway.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
		ociLoadBalancerClient.EXPECT().
			CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
				},
			}).
			Return(loadbalancer.CreateCertificateResponse{OpcWorkRequestId: &workRequestID}, nil).
			Once()
		workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

		_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID:    loadBalancerID,
			gateway:           &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{},
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to wait for certificate")
	})

	t.Run("reconcileListenersCertificates returns missing OCI work request id errors", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-ns"},
			Spec: gatewayv1.GatewaySpec{
				Listeners: []gatewayv1.Listener{{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					Port:     443,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-secret"}},
					},
				}},
			},
		}
		secret := makeRandomSecret(
			randomSecretWithNameOpt("tls-secret"),
			randomSecretWithTLSDataOpt(),
		)
		secret.Namespace = gateway.Namespace
		loadBalancerID := faker.New().UUID().V4()
		certName := ociCertificateNameFromSecret(secret)

		k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
		setupClientGet(t, k8sClient, types.NamespacedName{
			Namespace: gateway.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
		ociLoadBalancerClient.EXPECT().
			CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
				},
			}).
			Return(loadbalancer.CreateCertificateResponse{}, nil).
			Once()

		_, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
			loadBalancerID:    loadBalancerID,
			gateway:           &gateway,
			knownCertificates: map[string]loadbalancer.Certificate{},
		})

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("make listener update details detects OCI certificate IDs", func(t *testing.T) {
		listener := gatewayv1.Listener{
			Name:     "https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     443,
		}
		sslConfig := &loadbalancer.SslConfigurationDetails{
			CertificateIds: []string{"ocid1.certificate.oc1..test"},
		}

		details, changed := makeOciListenerUpdateDetails(makeOciListenerUpdateDetailsParams{
			existingListenerData: loadbalancer.Listener{
				Protocol:              new("HTTP"),
				Port:                  new(443),
				DefaultBackendSetName: new("default"),
				RoutingPolicyName:     new("https_policy"),
				SslConfiguration: &loadbalancer.SslConfiguration{
					CertificateIds: []string{"ocid1.certificate.oc1..old"},
				},
			},
			listenerName:          "https",
			listenerSpec:          &listener,
			defaultBackendSetName: "default",
			sslConfig:             sslConfig,
		})

		assert.True(t, changed)
		assert.Equal(t, []string{"ocid1.certificate.oc1..test"}, details.SslConfiguration.CertificateIds)
	})

	t.Run("reconcileHTTPListener configures OCI certificate IDs", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		listener := gatewayv1.Listener{
			Name:     "https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     443,
		}
		params := reconcileHTTPListenerParams{
			loadBalancerID: faker.New().UUID().V4(),
			knownListeners: map[string]loadbalancer.Listener{
				"https": {
					Name:                  new("https"),
					Protocol:              new("HTTP"),
					Port:                  new(443),
					DefaultBackendSetName: new("default"),
					RoutingPolicyName:     new("https_policy"),
				},
			},
			knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
				"https_policy": {
					Name:                     new("https_policy"),
					ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
					Rules: []loadbalancer.RoutingRule{
						defaultCatchAllRoutingRule("default"),
					},
				},
			},
			listenerCertificateID: "ocid1.certificate.oc1..test",
			defaultBackendSetName: "default",
			listenerSpec:          &listener,
		}
		ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
		watcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
		workRequestID := faker.New().UUID().V4()

		ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
			LoadBalancerId: new(params.loadBalancerID),
			ListenerName:   new("https"),
			UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
				Protocol:              new("HTTP"),
				Port:                  new(443),
				DefaultBackendSetName: new("default"),
				RoutingPolicyName:     new("https_policy"),
				SslConfiguration: &loadbalancer.SslConfigurationDetails{
					CertificateIds: []string{"ocid1.certificate.oc1..test"},
				},
			},
		}).Return(loadbalancer.UpdateListenerResponse{OpcWorkRequestId: new(workRequestID)}, nil)
		watcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

		err := model.reconcileHTTPListener(t.Context(), params)

		require.NoError(t, err)
	})

	t.Run("reconcileHTTPListener returns OCI certificate ID update errors", func(t *testing.T) {
		deps := ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           NewMockociLoadBalancerClient(t),
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		}
		model := newOciLoadBalancerModel(deps)
		listener := gatewayv1.Listener{
			Name:     "https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     443,
		}
		params := reconcileHTTPListenerParams{
			loadBalancerID: faker.New().UUID().V4(),
			knownListeners: map[string]loadbalancer.Listener{
				"https": {
					Name:                  new("https"),
					Protocol:              new("HTTP"),
					Port:                  new(443),
					DefaultBackendSetName: new("default"),
					RoutingPolicyName:     new("https_policy"),
				},
			},
			knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
				"https_policy": {
					Name:                     new("https_policy"),
					ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
					Rules: []loadbalancer.RoutingRule{
						defaultCatchAllRoutingRule("default"),
					},
				},
			},
			listenerCertificateID: "ocid1.certificate.oc1..test",
			defaultBackendSetName: "default",
			listenerSpec:          &listener,
		}
		ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))

		ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
			LoadBalancerId: new(params.loadBalancerID),
			ListenerName:   new("https"),
			UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
				Protocol:              new("HTTP"),
				Port:                  new(443),
				DefaultBackendSetName: new("default"),
				RoutingPolicyName:     new("https_policy"),
				SslConfiguration: &loadbalancer.SslConfigurationDetails{
					CertificateIds: []string{"ocid1.certificate.oc1..test"},
				},
			},
		}).Return(loadbalancer.UpdateListenerResponse{}, wantErr)

		err := model.reconcileHTTPListener(t.Context(), params)

		require.Error(t, err)
		assert.ErrorIs(t, err, wantErr)
	})
}

func Test_ociListerPolicyRuleName(t *testing.T) {
	makeExpectedName := func(ruleIndex int, nameParts ...string) string {
		unsanitizedInput := fmt.Sprintf(
			"p%04d_%08x_%s",
			ruleIndex,
			crc32.ChecksumIEEE([]byte(ociListenerPolicyRuleIdentity(ruleIndex, nameParts...))),
			strings.Join(nameParts, "_"),
		)
		return ociapi.ConstructOCIResourceName(unsanitizedInput, ociapi.OCIResourceNameConfig{
			MaxLength:           maxListenerPolicyNameLength,
			InvalidCharsPattern: invalidCharsForPolicyNamePattern,
		})
	}

	type testCase struct {
		name      string
		route     gatewayv1.HTTPRoute
		ruleIndex int

		want string
	}

	tests := []func() testCase{
		func() testCase {
			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),

				makeRandomHTTPRouteRule(),

				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithNamespaceOpt(fmt.Sprintf("ns_%d", rand.IntN(1000))),
				randomHTTPRouteWithNameOpt(fmt.Sprintf("rt_%d", rand.IntN(1000))),
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "unnamed rule",
				route:     route,
				ruleIndex: index,
				want:      makeExpectedName(index, route.Namespace, route.Name),
			}
		},
		func() testCase {
			rule := makeRandomHTTPRouteRule()
			index := 0

			unsanitizedNamespace := fmt.Sprintf("ns-%d!", rand.IntN(1000))
			unsanitizedParentName := fmt.Sprintf("rt-%d!", rand.IntN(1000))
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(rule),
				randomHTTPRouteWithNamespaceOpt(unsanitizedNamespace),
				randomHTTPRouteWithNameOpt(unsanitizedParentName),
			)
			return testCase{
				name:      "sanitized unnamed rule",
				route:     route,
				ruleIndex: index,
				want:      makeExpectedName(index, unsanitizedNamespace, unsanitizedParentName),
			}
		},
		func() testCase {
			ruleName := fmt.Sprintf("rl_%d", rand.IntN(1000))
			fewRules := []gatewayv1.HTTPRouteRule{
				makeRandomHTTPRouteRule(),

				makeRandomHTTPRouteRule(),

				makeRandomHTTPRouteRule(),
			}
			index := rand.IntN(len(fewRules))
			fewRules[index].Name = new(gatewayv1.SectionName(ruleName))

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithNamespaceOpt(fmt.Sprintf("ns_%d", rand.IntN(1000))),
				randomHTTPRouteWithNameOpt(fmt.Sprintf("rt_%d", rand.IntN(1000))),
				randomHTTPRouteWithRulesOpt(fewRules...),
			)
			return testCase{
				name:      "named rule",
				route:     route,
				ruleIndex: index,
				want:      makeExpectedName(index, route.Namespace, route.Name, ruleName),
			}
		},
		func() testCase {
			unsanitizedRuleName := fmt.Sprintf("rule-%d-!#:-rule", rand.IntN(1000))

			rule := makeRandomHTTPRouteRule()
			rule.Name = new(gatewayv1.SectionName(unsanitizedRuleName))
			index := 0

			unsanitizedNamespace := fmt.Sprintf("ns-%d!", rand.IntN(1000))
			unsanitizedParentName := fmt.Sprintf("rt-%d!", rand.IntN(1000))
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(rule),
				randomHTTPRouteWithNamespaceOpt(unsanitizedNamespace),
				randomHTTPRouteWithNameOpt(unsanitizedParentName),
			)
			return testCase{
				name:      "sanitized named rule",
				route:     route,
				ruleIndex: index,
				want:      makeExpectedName(index, unsanitizedNamespace, unsanitizedParentName, unsanitizedRuleName),
			}
		},
	}

	for _, tc := range tests {
		tc := tc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociListerPolicyRuleName(tc.route, tc.ruleIndex)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("same route name in different namespace", func(t *testing.T) {
		fake := faker.New()
		rule := makeRandomHTTPRouteRule()
		index := 0
		routeName := "route_" + fake.Lorem().Word()
		namespace := "ns_" + fake.Lorem().Word()
		otherNamespace := "other_ns_" + fake.Lorem().Word()
		for otherNamespace == namespace {
			otherNamespace = "other_ns_" + fake.Lorem().Word()
		}

		route := makeRandomHTTPRoute(
			randomHTTPRouteWithNamespaceOpt(namespace),
			randomHTTPRouteWithNameOpt(routeName),
			randomHTTPRouteWithRulesOpt(rule),
		)
		otherRoute := makeRandomHTTPRoute(
			randomHTTPRouteWithNamespaceOpt(otherNamespace),
			randomHTTPRouteWithNameOpt(routeName),
			randomHTTPRouteWithRulesOpt(rule),
		)

		assert.NotEqual(t,
			ociListerPolicyRuleName(route, index),
			ociListerPolicyRuleName(otherRoute, index),
		)
	})

	t.Run("sanitized namespace and route name boundaries remain unique", func(t *testing.T) {
		fake := faker.New()
		rule := makeRandomHTTPRouteRule()
		index := 0
		namePartA := fake.Lorem().Word()
		namePartB := fake.Lorem().Word()
		namePartC := fake.Lorem().Word()

		route := makeRandomHTTPRoute(
			randomHTTPRouteWithNamespaceOpt(fmt.Sprintf("%s-%s", namePartA, namePartB)),
			randomHTTPRouteWithNameOpt(namePartC),
			randomHTTPRouteWithRulesOpt(rule),
		)
		otherRoute := makeRandomHTTPRoute(
			randomHTTPRouteWithNamespaceOpt(namePartA),
			randomHTTPRouteWithNameOpt(fmt.Sprintf("%s-%s", namePartB, namePartC)),
			randomHTTPRouteWithRulesOpt(rule),
		)

		assert.NotEqual(t,
			ociListerPolicyRuleName(route, index),
			ociListerPolicyRuleName(otherRoute, index),
		)
	})

	t.Run("truncates generated name at crash boundary", func(t *testing.T) {
		fake := faker.New()
		rule := makeRandomHTTPRouteRule()
		index := 0
		namespace := fake.Numerify("#################")
		routeName := fake.Numerify("############################")
		route := makeRandomHTTPRoute(
			randomHTTPRouteWithNamespaceOpt(namespace),
			randomHTTPRouteWithNameOpt(routeName),
			randomHTTPRouteWithRulesOpt(rule),
		)

		unsanitizedInput := fmt.Sprintf(
			"p%04d_%08x_%s",
			index,
			crc32.ChecksumIEEE([]byte(ociListenerPolicyRuleIdentity(index, namespace, routeName))),
			strings.Join([]string{namespace, routeName}, "_"),
		)
		require.Len(t, unsanitizedInput, 61)

		require.NotPanics(t, func() {
			got := ociListerPolicyRuleName(route, index)
			assert.Len(t, got, maxListenerPolicyNameLength)
			assert.False(t, invalidCharsForPolicyNamePattern.MatchString(got))
		})
	})
}

func Test_listenerPolicyName(t *testing.T) {
	t.Run("preserves existing valid listener policy names", func(t *testing.T) {
		fake := faker.New()
		listenerName := "listener_" + fake.Lorem().Word()

		got := listenerPolicyName(listenerName)

		assert.Equal(t, listenerName+"_policy", got)
		assert.True(t, isValidOCIRoutingPolicyName(got))
	})

	t.Run("sanitizes gateway listener names that are invalid for OCI policy names", func(t *testing.T) {
		listenerName := "cert-reconcile"

		got := listenerPolicyName(listenerName)

		assert.NotEqual(t, "cert-reconcile_policy", got)
		assert.True(t, strings.HasPrefix(got, "p_"+listenerPolicyNameHash(listenerName)+"_"))
		assert.True(t, isValidOCIRoutingPolicyName(got))
		assert.False(t, invalidCharsForPolicyNamePattern.MatchString(got))
	})

	t.Run("keeps sanitized listener names unique", func(t *testing.T) {
		got := listenerPolicyName("route-name")
		other := listenerPolicyName("route.name")

		assert.True(t, isValidOCIRoutingPolicyName(got))
		assert.True(t, isValidOCIRoutingPolicyName(other))
		assert.NotEqual(t, got, other)
	})

	t.Run("handles listener names that would start OCI policy names with a digit", func(t *testing.T) {
		got := listenerPolicyName("9listener")

		assert.NotEqual(t, "9listener_policy", got)
		assert.True(t, isValidOCIRoutingPolicyName(got))
	})

	t.Run("truncates long listener policy names", func(t *testing.T) {
		fake := faker.New()
		listenerName := "listener" + fake.Numerify("########################################")

		got := listenerPolicyName(listenerName)

		assert.True(t, strings.HasPrefix(got, "p_"+listenerPolicyNameHash(listenerName)+"_"))
		assert.True(t, isValidOCIRoutingPolicyName(got))
		assert.LessOrEqual(t, len(got), maxListenerPolicyNameLength)
	})

	t.Run("keeps unique hash prefix when long names share a readable prefix", func(t *testing.T) {
		fake := faker.New()
		listenerName := "listener-" + fake.Numerify("################################")
		otherListenerName := listenerName + "-other"

		got := listenerPolicyName(listenerName)
		other := listenerPolicyName(otherListenerName)

		assert.True(t, strings.HasPrefix(got, "p_"+listenerPolicyNameHash(listenerName)+"_"))
		assert.True(t, strings.HasPrefix(other, "p_"+listenerPolicyNameHash(otherListenerName)+"_"))
		assert.True(t, isValidOCIRoutingPolicyName(got))
		assert.True(t, isValidOCIRoutingPolicyName(other))
		assert.NotEqual(t, got, other)
	})
}

func Test_ociBackendSetNameFromBackendRef(t *testing.T) {
	type testCase struct {
		name       string
		httpRoute  gatewayv1.HTTPRoute
		backendRef gatewayv1.HTTPBackendRef
		want       string
	}

	tests := []func() testCase{
		func() testCase {
			fake := faker.New()
			refName := fake.Internet().Slug()
			refNamespace := fake.Lorem().Word() + "-ns"
			httpRouteNs := fake.Lorem().Word() + "-route-ns"

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNameOpt(fake.Internet().Slug()+"-route"),
				func(hr *gatewayv1.HTTPRoute) {
					hr.Namespace = httpRouteNs
				},
			)
			backendRef := makeRandomBackendRef(
				func(br *gatewayv1.HTTPBackendRef) {
					br.Name = gatewayv1.ObjectName(refName)
					br.Namespace = new(gatewayv1.Namespace(refNamespace))
				},
			)
			wantName := fmt.Sprintf("%s-%s", refNamespace, refName)
			return testCase{
				name:       "with namespace in backendRef",
				httpRoute:  httpRoute,
				backendRef: backendRef,
				want: ociapi.ConstructOCIResourceName(wantName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
		func() testCase {
			fake := faker.New()
			routeNs := fake.Lorem().Word() + "-route-namespace"
			refName := fake.Internet().Slug() + "-svc"

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNameOpt(fake.Internet().Slug()+"-route"),
				func(hr *gatewayv1.HTTPRoute) {
					hr.Namespace = routeNs
				},
			)
			backendRef := makeRandomBackendRef(
				func(br *gatewayv1.HTTPBackendRef) {
					br.Name = gatewayv1.ObjectName(refName)
					br.Namespace = nil // No namespace in ref
				},
			)
			wantName := fmt.Sprintf("%s-%s", routeNs, refName)
			return testCase{
				name:       "without namespace in backendRef, uses route namespace",
				httpRoute:  httpRoute,
				backendRef: backendRef,
				want: ociapi.ConstructOCIResourceName(wantName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
		func() testCase {
			fake := faker.New()
			longRefNs := fake.Numerify("################################")[0:16]
			longRefName := fake.Numerify("################################")[0:16]

			httpRoute := makeRandomHTTPRoute()
			backendRef := makeRandomBackendRef(
				func(br *gatewayv1.HTTPBackendRef) {
					br.Name = gatewayv1.ObjectName(longRefName)
					br.Namespace = new(gatewayv1.Namespace(longRefNs))
				},
			)
			originalName := fmt.Sprintf("%s-%s", longRefNs, longRefName)
			assert.Greater(t, len(originalName), maxBackendSetNameLength)
			return testCase{
				name:       "long name truncated",
				httpRoute:  httpRoute,
				backendRef: backendRef,
				want: ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
	}

	for _, tcFunc := range tests {
		tc := tcFunc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociBackendSetNameFromBackendRef(tc.httpRoute, tc.backendRef)
			assert.Equal(t, tc.want, got)
		})
	}
}

func Test_ociBackendSetNameFromService(t *testing.T) {
	type testCase struct {
		name    string
		service corev1.Service
		want    string
	}

	tests := []func() testCase{
		func() testCase {
			fake := faker.New()
			svcNs := fake.Lorem().Word() + "-ns"
			svcName := fake.Internet().Slug() + "-svc"
			service := makeRandomService(
				func(s *corev1.Service) {
					s.Name = svcName
					s.Namespace = svcNs
				},
			)
			wantName := fmt.Sprintf("%s-%s", svcNs, svcName)
			return testCase{
				name:    "standard name",
				service: service,
				want: ociapi.ConstructOCIResourceName(wantName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
		func() testCase {
			fake := faker.New()
			longSvcNs := fake.Numerify("################################")
			longSvcName := fake.Numerify("################################")
			longSvcNs = longSvcNs[0:20]
			longSvcName = longSvcName[0:20]
			service := makeRandomService(
				func(s *corev1.Service) {
					s.Name = longSvcName
					s.Namespace = longSvcNs
				},
			)
			originalName := fmt.Sprintf("%s-%s", longSvcNs, longSvcName)
			assert.Greater(t, len(originalName), maxBackendSetNameLength)
			return testCase{
				name:    "long name truncated",
				service: service,
				want: ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
					MaxLength: maxBackendSetNameLength,
				}),
			}
		},
	}

	for _, tcFunc := range tests {
		tc := tcFunc()
		t.Run(tc.name, func(t *testing.T) {
			got := ociBackendSetNameFromService(tc.service)
			assert.Equal(t, tc.want, got)
		})
	}
}

func Test_ociCertificateNameFromSecret(t *testing.T) {
	secret := makeRandomSecret()
	got := ociCertificateNameFromSecret(secret)
	assert.Equal(t, secret.Namespace+"-"+secret.Name+"-rev-"+secret.ResourceVersion, got)
}

func Test_makeOciListenerUpdateDetails(t *testing.T) {
	type testCase struct {
		name   string
		params makeOciListenerUpdateDetailsParams
		want   loadbalancer.UpdateListenerDetails
		wantOk bool
	}

	makeSslConfigFromDetails := func(details *loadbalancer.SslConfigurationDetails) *loadbalancer.SslConfiguration {
		return &loadbalancer.SslConfiguration{
			CertificateName: details.CertificateName,
			CertificateIds:  details.CertificateIds,
		}
	}

	tests := []func() testCase{
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()

			return testCase{
				name: "no changes needed",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
				},
				want:   loadbalancer.UpdateListenerDetails{},
				wantOk: false,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			newPort := listenerSpec.Port + 1

			return testCase{
				name: "port change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(newPort)), // Set to a different port
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(defaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()

			return testCase{
				name: "protocol change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("TCP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(defaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			newDefaultBackendSetName := fake.UUID().V4()

			return testCase{
				name: "default backend set change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: newDefaultBackendSetName,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(newDefaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()

			return testCase{
				name: "routing policy name change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new("wrong-" + listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(defaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			certName := fake.UUID().V4()
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
			}

			return testCase{
				name: "ssl config change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
					sslConfig:             sslConfig,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(defaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					SslConfiguration:      sslConfig,
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			oldCertName := fake.UUID().V4()
			newCertName := fake.UUID().V4()
			oldSslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &oldCertName,
			}
			newSslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &newCertName,
			}

			return testCase{
				name: "ssl config certificate change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration:      makeSslConfigFromDetails(oldSslConfig),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
					sslConfig:             newSslConfig,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(defaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					SslConfiguration:      newSslConfig,
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			certName := fake.UUID().V4()
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
			}

			return testCase{
				name: "ssl config removed",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration:      makeSslConfigFromDetails(sslConfig),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
					sslConfig:             nil,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new("HTTP"),
					Port:                  new(int(listenerSpec.Port)),
					DefaultBackendSetName: new(defaultBackendSetName),
					RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					SslConfiguration:      nil,
				},
				wantOk: true,
			}
		},
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateIds: []string{},
			}

			return testCase{
				name: "empty certificate IDs match nil certificate IDs",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration: &loadbalancer.SslConfiguration{
							CertificateIds: nil,
						},
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
					sslConfig:             sslConfig,
				},
				want:   loadbalancer.UpdateListenerDetails{},
				wantOk: false,
			}
		},
	}

	for _, tc := range tests {
		tc := tc()
		t.Run(tc.name, func(t *testing.T) {
			got, gotOk := makeOciListenerUpdateDetails(tc.params)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantOk, gotOk)
		})
	}
}
