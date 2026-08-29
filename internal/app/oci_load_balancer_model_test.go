package app

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
			assert.False(t, loadBalancerHealthCheckerMatches(&loadbalancer.HealthChecker{
				Protocol: new("HTTP"),
				Port:     &port,
			}, loadbalancer.HealthCheckerDetails{
				Protocol: &protocol,
				Port:     &port,
			}))
			assert.True(t, loadBalancerHealthCheckerMatches(&loadbalancer.HealthChecker{
				Protocol: &protocol,
				Port:     &port,
			}, loadbalancer.HealthCheckerDetails{
				Protocol: &protocol,
				Port:     &port,
			}))
			assert.True(t, loadBalancerBackendSetMatches(
				loadbalancer.BackendSet{
					Policy: new("ROUND_ROBIN"),
					HealthChecker: &loadbalancer.HealthChecker{
						Protocol: &protocol,
						Port:     &port,
					},
				},
				"ROUND_ROBIN",
				loadbalancer.HealthCheckerDetails{Protocol: &protocol, Port: &port},
			))
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
			assert.Nil(t, firstSSLConfig(nil))
			assert.Equal(t, got, firstSSLConfig([]*loadbalancer.SslConfigurationDetails{got}))
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

		t.Run("compares listener ssl config drift with optional desired fields", func(t *testing.T) {
			fake := faker.New()
			base := &loadbalancer.SslConfigurationDetails{
				CertificateName: new("cert-" + fake.Lorem().Word()),
				CertificateIds: []string{
					"ocid1.certificate.oc1.." + fake.UUID().V4(),
					"ocid1.certificate.oc1.." + fake.UUID().V4(),
				},
				CipherSuiteName:       new("cipher-" + fake.Lorem().Word()),
				Protocols:             []string{"TLSv1.2", "TLSv1.3"},
				VerifyPeerCertificate: new(true),
				VerifyDepth:           new(4),
				TrustedCertificateAuthorityIds: []string{
					"ocid1.cabundle.oc1.." + fake.UUID().V4(),
					"ocid1.cabundle.oc1.." + fake.UUID().V4(),
				},
			}
			clone := func() *loadbalancer.SslConfigurationDetails {
				return &loadbalancer.SslConfigurationDetails{
					CertificateName:                base.CertificateName,
					CertificateIds:                 slices.Clone(base.CertificateIds),
					CipherSuiteName:                base.CipherSuiteName,
					Protocols:                      slices.Clone(base.Protocols),
					VerifyPeerCertificate:          base.VerifyPeerCertificate,
					VerifyDepth:                    base.VerifyDepth,
					TrustedCertificateAuthorityIds: slices.Clone(base.TrustedCertificateAuthorityIds),
				}
			}

			assert.True(t, loadBalancerListenerSSLConfigurationsEqual(clone(), clone()))
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(
				clone(),
				&loadbalancer.SslConfigurationDetails{
					CertificateName:       base.CertificateName,
					CertificateIds:        slices.Clone(base.CertificateIds),
					VerifyPeerCertificate: base.VerifyPeerCertificate,
				},
			))
			assert.True(t, loadBalancerListenerSSLConfigurationsEqual(
				&loadbalancer.SslConfigurationDetails{
					CertificateName:                base.CertificateName,
					CertificateIds:                 []string{base.CertificateIds[0]},
					TrustedCertificateAuthorityIds: []string{base.TrustedCertificateAuthorityIds[0]},
					VerifyPeerCertificate:          base.VerifyPeerCertificate,
				},
				&loadbalancer.SslConfigurationDetails{
					CertificateName:                base.CertificateName,
					CertificateIds:                 []string{base.CertificateIds[0]},
					TrustedCertificateAuthorityIds: []string{base.TrustedCertificateAuthorityIds[0]},
					VerifyPeerCertificate:          base.VerifyPeerCertificate,
				},
			))
			reorderedCurrent := clone()
			slices.Reverse(reorderedCurrent.CertificateIds)
			slices.Reverse(reorderedCurrent.Protocols)
			slices.Reverse(reorderedCurrent.TrustedCertificateAuthorityIds)
			assert.True(t, loadBalancerListenerSSLConfigurationsEqual(reorderedCurrent, clone()))
			assert.True(t, loadBalancerSSLConfigurationsEqual(reorderedCurrent, clone()))
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(nil, clone()))
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(clone(), nil))
			assert.False(t, loadBalancerSSLConfigurationsEqual(nil, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(clone(), nil))

			changedCertName := clone()
			changedCertName.CertificateName = new("cert-other-" + fake.Lorem().Word())
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedCertName, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedCertName, clone()))

			changedCertID := clone()
			changedCertID.CertificateIds = []string{"ocid1.certificate.oc1.." + fake.UUID().V4()}
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedCertID, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedCertID, clone()))

			changedCipher := clone()
			changedCipher.CipherSuiteName = new("cipher-other-" + fake.Lorem().Word())
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedCipher, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedCipher, clone()))

			changedProtocols := clone()
			changedProtocols.Protocols = []string{"TLSv1.1"}
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedProtocols, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedProtocols, clone()))

			changedPeerVerification := clone()
			changedPeerVerification.VerifyPeerCertificate = new(false)
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedPeerVerification, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedPeerVerification, clone()))

			changedVerifyDepth := clone()
			changedVerifyDepth.VerifyDepth = new(2)
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedVerifyDepth, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedVerifyDepth, clone()))

			changedSessionResumption := clone()
			changedHasSessionResumption := !lo.FromPtr(changedSessionResumption.HasSessionResumption)
			changedSessionResumption.HasSessionResumption = &changedHasSessionResumption
			assert.True(t, loadBalancerListenerSSLConfigurationsEqual(changedSessionResumption, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedSessionResumption, clone()))

			changedServerOrder := clone()
			changedServerOrder.ServerOrderPreference = loadbalancer.SslConfigurationDetailsServerOrderPreferenceDisabled
			if clone().ServerOrderPreference == loadbalancer.SslConfigurationDetailsServerOrderPreferenceDisabled {
				changedServerOrder.ServerOrderPreference = loadbalancer.SslConfigurationDetailsServerOrderPreferenceEnabled
			}
			assert.True(t, loadBalancerListenerSSLConfigurationsEqual(changedServerOrder, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedServerOrder, clone()))

			changedTrustedCA := clone()
			changedTrustedCA.TrustedCertificateAuthorityIds = []string{"ocid1.cabundle.oc1.." + fake.UUID().V4()}
			assert.False(t, loadBalancerListenerSSLConfigurationsEqual(changedTrustedCA, clone()))
			assert.False(t, loadBalancerSSLConfigurationsEqual(changedTrustedCA, clone()))
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
			driftedDefaultRule := defaultCatchAllRoutingRule(defaultBackendSetName)
			driftedDefaultRule.Actions = append(driftedDefaultRule.Actions, loadbalancer.ForwardToBackendSet{
				BackendSetName: new("other-" + fake.Lorem().Word()),
			})
			assert.True(t, routingPolicyDefaultRuleDrifted(loadbalancer.RoutingPolicy{
				Rules: []loadbalancer.RoutingRule{driftedDefaultRule},
			}, defaultBackendSetName))
			wrongBackendDefaultRule := defaultCatchAllRoutingRule("other-" + fake.Lorem().Word())
			assert.True(t, routingPolicyDefaultRuleDrifted(loadbalancer.RoutingPolicy{
				Rules: []loadbalancer.RoutingRule{wrongBackendDefaultRule},
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

		t.Run("keeps http and grpc policy rule names distinct for same route identity", func(t *testing.T) {
			fake := faker.New()
			namespace := "routes-" + fake.Lorem().Word()
			routeName := "api-" + fake.Lorem().Word()
			ruleName := gatewayv1.SectionName("root-" + fake.Lorem().Word())
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithNamespaceOpt(namespace),
				randomHTTPRouteWithNameOpt(routeName),
				randomHTTPRouteWithRulesOpt(makeRandomHTTPRouteRule(func(rule *gatewayv1.HTTPRouteRule) {
					rule.Name = &ruleName
				})),
			)
			grpcRoute := makeRandomGRPCRoute(randomGRPCRouteWithRulesOpt(
				makeRandomGRPCRouteRule(func(rule *gatewayv1.GRPCRouteRule) {
					rule.Name = &ruleName
				}),
			))
			grpcRoute.Namespace = namespace
			grpcRoute.Name = routeName

			httpRuleName := ociListerPolicyRuleName(httpRoute, 0)
			grpcRuleName := ociGRPCListenerPolicyRuleName(grpcRoute, 0)

			assert.NotEqual(t, httpRuleName, grpcRuleName)
			assert.True(t, isValidOCIRoutingPolicyName(httpRuleName))
			assert.True(t, isValidOCIRoutingPolicyName(grpcRuleName))
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

		t.Run("creates frontend mTLS certificate aliases with CA certificate", func(t *testing.T) {
			fakeData := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			certRefName := gatewayv1.ObjectName("server-cert-" + fakeData.Lorem().Word())
			certRefNamespace := gatewayv1.Namespace("server-ns-" + fakeData.Lorem().Word())
			listener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
				func(l *gatewayv1.Listener) {
					l.TLS.CertificateRefs = []gatewayv1.SecretObjectReference{{
						Name:      certRefName,
						Namespace: &certRefNamespace,
					}}
				},
			)
			gateway := newRandomGateway(randomGatewayWithListenersOpt(listener))
			caRefName := gatewayv1.ObjectName("client-ca-" + fakeData.Lorem().Word())
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{
						CACertificateRefs: []gatewayv1.ObjectReference{{
							Group: "",
							Kind:  "ConfigMap",
							Name:  caRefName,
						}},
					}},
				},
			}
			ref := listener.TLS.CertificateRefs[0]
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			caPEM := testCAPEM(t)
			configMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: gateway.Namespace,
					Name:      string(caRefName),
				},
				Data: map[string]string{"ca.crt": caPEM},
			}
			trimmedCAPEM := strings.TrimSpace(caPEM)
			certName := frontendMTLSListenerCertificateName(secret, listener.Port, trimmedCAPEM)
			loadBalancerID := fakeData.UUID().V4()
			workRequestID := fakeData.UUID().V4()
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			setupClientGet(t, k8sClient, types.NamespacedName{
				Namespace: string(lo.FromPtr(ref.Namespace)),
				Name:      string(ref.Name),
			}, secret).Once()
			setupClientGet(t, k8sClient, types.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      string(caRefName),
			}, configMap).Once()
			ociLoadBalancerClient.EXPECT().CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
					CaCertificate:     &trimmedCAPEM,
				},
			}).Return(loadbalancer.CreateCertificateResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			gotResult, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
				loadBalancerID: loadBalancerID,
				gateway:        gateway,
			})

			require.NoError(t, err)
			gotCerts := gotResult.certificatesByListener[string(listener.Name)]
			require.Len(t, gotCerts, 1)
			assert.Equal(t, certName, lo.FromPtr(gotCerts[0].CertificateName))
			assert.Contains(t, gotResult.reconciledCertificates, certName)
		})

		t.Run("serializes concurrent missing certificate creation", func(t *testing.T) {
			fakeData := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			secretName := gatewayv1.ObjectName("tls-secret-" + fakeData.Lorem().Word())
			secretNamespace := gatewayv1.Namespace("tls-ns-" + fakeData.Lorem().Word())
			listener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
				func(l *gatewayv1.Listener) {
					l.TLS.CertificateRefs = []gatewayv1.SecretObjectReference{{
						Name:      secretName,
						Namespace: &secretNamespace,
					}}
				},
			)
			gateway := newRandomGateway(randomGatewayWithListenersOpt(listener))
			ref := listener.TLS.CertificateRefs[0]
			secret := makeRandomSecret(randomSecretWithTLSDataOpt())
			certName := ociCertificateNameFromSecret(secret)
			loadBalancerID := fakeData.UUID().V4()
			workRequestID := fakeData.UUID().V4()

			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			setupClientGet(t, k8sClient, types.NamespacedName{
				Namespace: string(lo.FromPtr(ref.Namespace)),
				Name:      string(ref.Name),
			}, secret).Twice()

			createStarted := make(chan struct{})
			releaseCreate := make(chan struct{})
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			ociLoadBalancerClient.EXPECT().CreateCertificate(t.Context(), loadbalancer.CreateCertificateRequest{
				LoadBalancerId: &loadBalancerID,
				CreateCertificateDetails: loadbalancer.CreateCertificateDetails{
					CertificateName:   &certName,
					PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
					PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
				},
			}).RunAndReturn(func(
				context.Context,
				loadbalancer.CreateCertificateRequest,
			) (loadbalancer.CreateCertificateResponse, error) {
				close(createStarted)
				<-releaseCreate
				return loadbalancer.CreateCertificateResponse{OpcWorkRequestId: &workRequestID}, nil
			}).Once()

			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			reconcile := func() error {
				result, err := model.reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
					loadBalancerID:    loadBalancerID,
					gateway:           gateway,
					knownCertificates: map[string]loadbalancer.Certificate{},
				})
				if err != nil {
					return err
				}
				gotCerts := result.certificatesByListener[string(listener.Name)]
				if len(gotCerts) != 1 || lo.FromPtr(gotCerts[0].CertificateName) != certName {
					return fmt.Errorf("unexpected listener certificates: %#v", gotCerts)
				}
				return nil
			}

			errs := make(chan error, 2)
			go func() {
				errs <- reconcile()
			}()
			select {
			case <-createStarted:
			case <-time.After(time.Second):
				require.Fail(t, "timed out waiting for first certificate create")
			}
			go func() {
				errs <- reconcile()
			}()

			lockKey := loadBalancerCertificateLockKey(loadBalancerID, certName)
			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				model.certificateLocks.mu.Lock()
				defer model.certificateLocks.mu.Unlock()

				lock := model.certificateLocks.locks[lockKey]
				if assert.NotNil(collect, lock) {
					assert.Equal(collect, 2, lock.refs)
				}
			}, time.Second, 10*time.Millisecond)

			close(releaseCreate)

			for range 2 {
				select {
				case err := <-errs:
					require.NoError(t, err)
				case <-time.After(time.Second):
					require.Fail(t, "timed out waiting for certificate reconciliation")
				}
			}

			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				model.certificateLocks.mu.Lock()
				defer model.certificateLocks.mu.Unlock()
				assert.Empty(collect, model.certificateLocks.locks)
			}, time.Second, 10*time.Millisecond)
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
			cipherSuiteName := "oci-tls-12-13-ssl-cipher-suite-v3"
			gwListener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
				func(listener *gatewayv1.Listener) {
					listener.TLS.Options = map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
						ListenerTLSOptionCipherSuiteName: gatewayv1.AnnotationValue(cipherSuiteName),
						ListenerTLSOptionProtocols:       "TLSv1.2,TLSv1.3",
					}
				},
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
						CipherSuiteName: &cipherSuiteName,
						Protocols:       []string{"TLSv1.2", "TLSv1.3"},
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

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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
			cipherSuiteName := "oci-tls-13-ssl-cipher-suite-v3"
			gwListener := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
				func(listener *gatewayv1.Listener) {
					listener.TLS.Options = map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
						ListenerTLSOptionCipherSuiteName: gatewayv1.AnnotationValue(cipherSuiteName),
						ListenerTLSOptionProtocols:       "TLSv1.3",
					}
				},
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

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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
						CipherSuiteName: &cipherSuiteName,
						Protocols:       []string{"TLSv1.3"},
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

		t.Run("uses routing policy created after load balancer snapshot", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			defaultBackendSetName := fake.UUID().V4()
			params := reconcileHTTPListenerParams{
				loadBalancerID:        fake.UUID().V4(),
				knownListeners:        map[string]loadbalancer.Listener{},
				knownRoutingPolicies:  map[string]loadbalancer.RoutingPolicy{},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
			}, nil)
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

		t.Run("uses refreshed routing policy when snapshot default rule is stale", func(t *testing.T) {
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
				},
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					routingPolicyName: makeMatchingRoutingPolicy(routingPolicyName, "wrong-"+fake.UUID().V4()),
				},
				defaultBackendSetName: defaultBackendSetName,
				listenerSpec:          &gwListener,
			}

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			listenerWorkRequestID := fake.UUID().V4()

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: makeMatchingRoutingPolicy(routingPolicyName, defaultBackendSetName),
			}, nil)
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

		t.Run("does not create routing policy when frontend mTLS validation fails", func(t *testing.T) {
			fakeData := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway(randomGatewayWithListenersOpt(gwListener))
			gateway.Annotations = map[string]string{}
			gateway.Annotations[FrontendMTLSTrustedCABundleOCIDsAnnotation] =
				"ocid1.cabundle.oc1.." + fakeData.UUID().V4()
			certName := "cert-" + fakeData.Lorem().Word()
			params := reconcileHTTPListenerParams{
				loadBalancerID:            fakeData.UUID().V4(),
				defaultBackendSetName:     fakeData.UUID().V4(),
				listenerSpec:              &gwListener,
				gateway:                   gateway,
				listenerCertificates:      []loadbalancer.Certificate{{CertificateName: &certName}},
				knownRoutingPolicies:      map[string]loadbalancer.RoutingPolicy{},
				knownListeners:            map[string]loadbalancer.Listener{},
				listenerCertificateID:     "",
				loadBalancerCompartmentID: "compartment-" + fakeData.Lorem().Word(),
			}
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorContains(t, err, "OCI CA bundle OCID annotations require listener")
			ociLoadBalancerClient.AssertNotCalled(t, "CreateRoutingPolicy")
			ociLoadBalancerClient.AssertNotCalled(t, "UpdateRoutingPolicy")
			ociLoadBalancerClient.AssertNotCalled(t, "CreateListener")
			ociLoadBalancerClient.AssertNotCalled(t, "UpdateListener")
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
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)
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

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)
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
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)
			ociLoadBalancerClient.EXPECT().
				UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{}, nil)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
			ociLoadBalancerClient.AssertNotCalled(t, "CreateListener")
		})

		t.Run("fails when routing policy default rule update wait fails", func(t *testing.T) {
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
			workRequestID := fake.UUID().V4()
			wantErr := errors.New("wait failed")

			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: existingPolicy,
			}, nil)
			ociLoadBalancerClient.EXPECT().
				UpdateRoutingPolicy(t.Context(), mock.Anything).
				Return(loadbalancer.UpdateRoutingPolicyResponse{OpcWorkRequestId: &workRequestID}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)

			err := model.reconcileHTTPListener(t.Context(), params)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to wait for routing policy")
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

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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

			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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
			routingPolicyName := listenerPolicyName(string(gwListener.Name))
			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &routingPolicyName,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
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
		t.Run("resolves health checker port from backend target port", func(t *testing.T) {
			service := makeRandomService()
			servicePort := service.Spec.Ports[0].Port
			targetPort := servicePort%65535 + 1
			service.Spec.Ports[0].TargetPort = intstr.FromInt(int(targetPort))
			backendRef := gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName(service.Name),
				Port: &servicePort,
			}}

			assert.Equal(t, int(targetPort), healthCheckerPortForBackendRef(service, backendRef))

			service.Spec.Ports[0].TargetPort = intstr.FromInt(0)
			assert.Equal(t, int(servicePort), healthCheckerPortForBackendRef(service, backendRef))

			backendRef.Port = nil
			service.Spec.Ports[0].TargetPort = intstr.FromInt(int(targetPort))
			assert.Equal(t, int(targetPort), healthCheckerPortForBackendRef(service, backendRef))
			service.Spec.Ports[0].TargetPort = intstr.FromInt(0)
			assert.Equal(t, int(servicePort), healthCheckerPortForBackendRef(service, backendRef))
			assert.Equal(t, 0, healthCheckerPortForBackendRef(corev1.Service{}, gatewayv1.BackendRef{}))
		})

		makeParams := func(service corev1.Service, loadBalancerID string) reconcileBackendSetParams {
			service.Spec.Ports[0].TargetPort = intstr.FromInt(int(service.Spec.Ports[0].Port))
			namespace := gatewayv1.Namespace(service.Namespace)
			return reconcileBackendSetParams{
				loadBalancerID: loadBalancerID,
				service:        service,
				routeNS:        service.Namespace,
				backendRef: gatewayv1.BackendRef{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name:      gatewayv1.ObjectName(service.Name),
						Namespace: &namespace,
						Port:      &service.Spec.Ports[0].Port,
					},
				},
			}
		}
		backendSetNameFromParams := func(params reconcileBackendSetParams) string {
			return ociBackendSetNameFromBackendObjectRef(
				params.routeNS,
				params.backendRef.BackendObjectReference,
			)
		}

		t.Run("create new backend set uses service target port", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			service := makeRandomService()

			params := makeParams(service, fake.UUID().V4())
			params.service.Spec.Ports[0].TargetPort = intstr.FromInt(
				int(params.service.Spec.Ports[0].Port%65535 + 1),
			)

			wantBsName := backendSetNameFromParams(params)

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
						Port:     new(healthCheckerPortForBackendRef(params.service, params.backendRef)),
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

			params := makeParams(service, fake.UUID().V4())
			params.backendRef.Port = nil

			wantBsName := backendSetNameFromParams(params)

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
			params := makeParams(service, fake.UUID().V4())
			wantBsName := backendSetNameFromParams(params)
			exitingBs := makeRandomOCIBackendSet(func(bs *loadbalancer.BackendSet) {
				bs.Name = new(wantBsName)
				bs.Policy = new("ROUND_ROBIN")
				bs.HealthChecker = &loadbalancer.HealthChecker{
					Protocol: new("TCP"),
					Port:     new(healthCheckerPortForBackendRef(params.service, params.backendRef)),
				}
			})

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
			params := makeParams(service, fake.UUID().V4())
			wantBsName := backendSetNameFromParams(params)
			existingBs := makeRandomOCIBackendSet(randomOCIBackendSetWithNameOpt(wantBsName))

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
						assert.Equal(
							t,
							healthCheckerPortForBackendRef(params.service, params.backendRef),
							*req.HealthChecker.Port,
						)
				}),
			).Return(loadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			err := model.reconcileBackendSet(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("keeps backend health check when adding backend TLS SSL config", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			service := makeRandomService()
			params := makeParams(service, fake.UUID().V4())
			params.manageSSLConfig = true
			verifyDepth := 2
			params.sslConfig = &loadbalancer.SslConfigurationDetails{
				VerifyDepth: &verifyDepth,
			}
			wantBsName := backendSetNameFromParams(params)
			existingBs := makeRandomOCIBackendSet(func(bs *loadbalancer.BackendSet) {
				bs.Name = new(wantBsName)
				bs.Policy = new("ROUND_ROBIN")
				bs.HealthChecker = &loadbalancer.HealthChecker{
					Protocol: new("TCP"),
					Port:     new(healthCheckerPortForBackendRef(params.service, params.backendRef)),
				}
				bs.SslConfiguration = nil
			})

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
					return assert.Equal(t, "TCP", lo.FromPtr(req.HealthChecker.Protocol)) &&
						assert.Equal(
							t,
							healthCheckerPortForBackendRef(params.service, params.backendRef),
							lo.FromPtr(req.HealthChecker.Port),
						) &&
						assert.Equal(t, verifyDepth, lo.FromPtr(req.SslConfiguration.VerifyDepth))
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
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			params := makeParams(service, faker.New().UUID().V4())
			wantBsName := backendSetNameFromParams(params)
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
			wantErr := ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(500))
			params := makeParams(service, faker.New().UUID().V4())
			wantBsName := backendSetNameFromParams(params)
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
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			params := makeParams(service, faker.New().UUID().V4())
			wantBsName := backendSetNameFromParams(params)
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
						Port:     new(healthCheckerPortForBackendRef(params.service, params.backendRef)),
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
			params := makeParams(service, faker.New().UUID().V4())
			wantBsName := backendSetNameFromParams(params)
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
						Port:     new(healthCheckerPortForBackendRef(params.service, params.backendRef)),
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
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			params := makeParams(service, faker.New().UUID().V4())
			wantBsName := backendSetNameFromParams(params)
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
						Port:     new(healthCheckerPortForBackendRef(params.service, params.backendRef)),
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

		t.Run("preserves listeners that are outside cleanup scope", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			currentListener := makeRandomListener()
			currentOCIListener := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(currentListener.Name))
			})
			scopedStaleListener := makeRandomOCIListener()
			otherGatewayListener := makeRandomOCIListener()

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					lo.FromPtr(currentOCIListener.Name):   currentOCIListener,
					lo.FromPtr(scopedStaleListener.Name):  scopedStaleListener,
					lo.FromPtr(otherGatewayListener.Name): otherGatewayListener,
				},
				cleanupListenerNames: map[string]struct{}{
					lo.FromPtr(currentOCIListener.Name):  {},
					lo.FromPtr(scopedStaleListener.Name): {},
				},
				gatewayListeners: []gatewayv1.Listener{
					currentListener,
				},
			}

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   scopedStaleListener.Name,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("preserves all listeners when cleanup scope is empty", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			currentOCIListener := makeRandomOCIListener()
			otherGatewayListener := makeRandomOCIListener()

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					lo.FromPtr(currentOCIListener.Name):   currentOCIListener,
					lo.FromPtr(otherGatewayListener.Name): otherGatewayListener,
				},
				cleanupListenerNames: map[string]struct{}{},
				gatewayListeners:     nil,
			}

			err := model.removeMissingListeners(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("removes stale ListenerSet derived listeners", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)
			gateway := gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "infra-" + fake.Lorem().Word(),
					Name:      "edge-" + fake.Lorem().Word(),
				},
			}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + fake.Lorem().Word(),
					Name:      "media-" + fake.Lorem().Word(),
				},
			}
			staleListenerName := listenerSetOCIListenerName(
				gateway,
				listenerSet,
				gatewayv1.Listener{Name: "https", Protocol: gatewayv1.HTTPSProtocolType, Port: 443},
			)
			staleListener := makeRandomOCIListener(func(listener *loadbalancer.Listener) {
				listener.Name = &staleListenerName
			})
			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					staleListenerName: staleListener,
				},
			}
			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteListener(t.Context(), loadbalancer.DeleteListenerRequest{
				LoadBalancerId: &params.loadBalancerID,
				ListenerName:   &staleListenerName,
			}).Return(loadbalancer.DeleteListenerResponse{OpcWorkRequestId: &workRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

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

		t.Run("removes orphaned listener routing policies when listeners are already gone", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			currentListener := makeRandomListener()
			currentOCIListener := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(currentListener.Name))
				l.RoutingPolicyName = new(listenerPolicyName(string(currentListener.Name)))
			})
			tlsListener := gatewayv1.Listener{
				Name:     "rtmps",
				Protocol: gatewayv1.TLSProtocolType,
				Port:     1936,
			}
			removedListenerName := "removed-" + fake.UUID().V4()
			orphanPolicyName := listenerPolicyName(removedListenerName)
			rtmpsPolicyName := listenerPolicyName(string(tlsListener.Name))
			userPolicyName := "custom_policy"
			defaultRule := loadbalancer.RoutingRule{
				Name:      new(defaultCatchAllRuleName),
				Condition: new("any(http.request.url.path sw '/')"),
			}

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*currentOCIListener.Name: currentOCIListener,
				},
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					*currentOCIListener.RoutingPolicyName: makeRandomOCIRoutingPolicy(
						func(p *loadbalancer.RoutingPolicy) {
							p.Name = currentOCIListener.RoutingPolicyName
						},
					),
					orphanPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &orphanPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
					rtmpsPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &rtmpsPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
					userPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &userPolicyName
					}),
				},
				gatewayListeners: []gatewayv1.Listener{
					currentListener,
					tlsListener,
				},
			}

			deletePolicyRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &orphanPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID).Return(nil).Once()
			deleteRTMPSPolicyRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &rtmpsPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deleteRTMPSPolicyRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deleteRTMPSPolicyRequestID).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("preserves orphaned routing policies outside cleanup scope", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			scopedListenerName := "scoped-" + fake.Lorem().Word()
			otherGatewayListenerName := "other-" + fake.Lorem().Word()
			scopedPolicyName := listenerPolicyName(scopedListenerName)
			otherGatewayPolicyName := listenerPolicyName(otherGatewayListenerName)
			defaultRule := loadbalancer.RoutingRule{
				Name:      new(defaultCatchAllRuleName),
				Condition: new("any(http.request.url.path sw '/')"),
			}
			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					scopedPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &scopedPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
					otherGatewayPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &otherGatewayPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
				},
				cleanupListenerNames: map[string]struct{}{
					scopedListenerName: {},
				},
			}

			deletePolicyRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &scopedPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{OpcWorkRequestId: &deletePolicyRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), deletePolicyRequestID).Return(nil).Once()

			err := model.removeMissingListeners(t.Context(), params)

			require.NoError(t, err)
		})

		t.Run("skips protected orphaned listener routing policy candidates", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)

			desiredListener := makeRandomListener(func(l *gatewayv1.Listener) {
				l.Protocol = gatewayv1.HTTPSProtocolType
			})
			attachedGatewayListener := makeRandomListener()
			desiredPolicyName := listenerPolicyName(string(desiredListener.Name))
			attachedPolicyName := listenerPolicyName("attached-" + fake.UUID().V4())
			nonDefaultPolicyName := listenerPolicyName("non-default-" + fake.UUID().V4())
			userPolicyName := "manual." + fake.Internet().Domain()
			attachedListener := makeRandomOCIListener(func(l *loadbalancer.Listener) {
				l.Name = new(string(attachedGatewayListener.Name))
				l.RoutingPolicyName = &attachedPolicyName
			})
			defaultRule := loadbalancer.RoutingRule{
				Name:      new(defaultCatchAllRuleName),
				Condition: new("any(http.request.url.path sw '/')"),
			}

			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					*attachedListener.Name: attachedListener,
				},
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					desiredPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &desiredPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
					attachedPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &attachedPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
					nonDefaultPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &nonDefaultPolicyName
					}),
					userPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &userPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
				},
				gatewayListeners: []gatewayv1.Listener{
					desiredListener,
					attachedGatewayListener,
				},
			}

			err := model.removeMissingListeners(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("returns orphaned routing policy delete errors", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			orphanPolicyName := listenerPolicyName("orphan-" + fake.UUID().V4())
			wantErr := errors.New(fake.Lorem().Sentence(10))
			defaultRule := loadbalancer.RoutingRule{
				Name:      new(defaultCatchAllRuleName),
				Condition: new("any(http.request.url.path sw '/')"),
			}
			params := removeMissingListenersParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					orphanPolicyName: makeRandomOCIRoutingPolicy(func(p *loadbalancer.RoutingPolicy) {
						p.Name = &orphanPolicyName
						p.Rules = []loadbalancer.RoutingRule{defaultRule}
					}),
				},
			}

			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &params.loadBalancerID,
				RoutingPolicyName: &orphanPolicyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{}, wantErr).Once()

			err := model.removeMissingListeners(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
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
			listenerPort := 8000 + fake.Int32Between(1, 1000)

			params := makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
				listenerPort:       listenerPort,
			}

			expectedCondition := fake.Lorem().Sentence(10)
			routingRulesMapper.EXPECT().mapHTTPRouteHostnamesAndMatchesToCondition(
				httpRoute.Spec.Hostnames,
				listenerPort,
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
			listenerPort := 8000 + fake.Int32Between(1, 1000)
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
				listenerPort:       listenerPort,
			})

			require.NoError(t, err)
			condition := lo.FromPtr(actualRule.Condition)
			assert.Contains(t, condition, "all(")
			assert.Contains(t, condition, "http.request.headers[(i 'host')]")
			assert.Contains(t, condition, fmt.Sprintf("eq (i '%s')", hostname))
			assert.Contains(t, condition, fmt.Sprintf("eq (i '%s:%d')", hostname, listenerPort))
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
			listenerPort := 8000 + fake.Int32Between(1, 1000)

			params := makeRoutingRuleParams{
				httpRoute:          httpRoute,
				httpRouteRuleIndex: ruleIndex,
				listenerPort:       listenerPort,
			}

			expectedErr := errors.New(fake.Lorem().Sentence(10))
			routingRulesMapper.EXPECT().mapHTTPRouteHostnamesAndMatchesToCondition(
				httpRoute.Spec.Hostnames,
				listenerPort,
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
			listenerPort := 8000 + fake.Int32Between(1, 1000)

			expectedCondition := fake.Lorem().Sentence(10)
			routingRulesMapper.EXPECT().mapGRPCRouteHostnamesAndMatchesToCondition(
				grpcRoute.Spec.Hostnames,
				listenerPort,
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
				listenerPort:       listenerPort,
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
			listenerPort := 8000 + fake.Int32Between(1, 1000)
			routingRulesMapper.EXPECT().mapGRPCRouteHostnamesAndMatchesToCondition(
				grpcRoute.Spec.Hostnames,
				listenerPort,
				grpcRoute.Spec.Rules[0].Matches,
			).Return("", wantErr).Once()

			_, err := model.makeGRPCRoutingRule(t.Context(), makeGRPCRoutingRuleParams{
				grpcRoute:          grpcRoute,
				grpcRouteRuleIndex: 0,
				listenerPort:       listenerPort,
			})

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("commitRoutingPolicy", func(t *testing.T) {
		t.Run("creates missing routing policy with desired rules", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			workRequestID := fake.UUID().V4()
			newRules := []loadbalancer.RoutingRule{
				makeRandomOCIRoutingRule(),
				makeRandomOCIRoutingRule(),
			}
			wantRules := slices.Clone(newRules)
			sortRoutingRules(wantRules)

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
			ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(
				t.Context(),
				mock.MatchedBy(func(req loadbalancer.CreateRoutingPolicyRequest) bool {
					require.Equal(t, loadBalancerID, lo.FromPtr(req.LoadBalancerId))
					require.Equal(t, policyName, lo.FromPtr(req.CreateRoutingPolicyDetails.Name))
					require.Equal(
						t,
						loadbalancer.CreateRoutingPolicyDetailsConditionLanguageVersionV1,
						req.CreateRoutingPolicyDetails.ConditionLanguageVersion,
					)
					require.Equal(t, wantRules, req.CreateRoutingPolicyDetails.Rules)
					return true
				}),
			).Return(loadbalancer.CreateRoutingPolicyResponse{OpcWorkRequestId: &workRequestID}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    newRules,
			})

			require.NoError(t, err)
		})

		t.Run("ignores missing routing policy with no desired rules", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{},
				ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
			})

			require.NoError(t, err)
			ociLoadBalancerClient.AssertNotCalled(t, "CreateRoutingPolicy")
			ociLoadBalancerClient.AssertNotCalled(t, "UpdateRoutingPolicy")
		})

		t.Run("returns missing routing policy create errors", func(t *testing.T) {
			fake := faker.New()
			for name, tc := range map[string]struct {
				setup func(
					*testing.T,
					*MockociLoadBalancerClient,
					*MockworkRequestsWatcher,
					string,
					string,
					error,
				)
				wantContains string
			}{
				"create fails": {
					setup: func(
						t *testing.T,
						ociLoadBalancerClient *MockociLoadBalancerClient,
						_ *MockworkRequestsWatcher,
						loadBalancerID string,
						policyName string,
						wantErr error,
					) {
						ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(
							t.Context(),
							mock.MatchedBy(func(req loadbalancer.CreateRoutingPolicyRequest) bool {
								return lo.FromPtr(req.LoadBalancerId) == loadBalancerID &&
									lo.FromPtr(req.CreateRoutingPolicyDetails.Name) == policyName
							}),
						).Return(loadbalancer.CreateRoutingPolicyResponse{}, wantErr)
					},
					wantContains: "failed to create routing policy",
				},
				"create returns no work request": {
					setup: func(
						t *testing.T,
						ociLoadBalancerClient *MockociLoadBalancerClient,
						_ *MockworkRequestsWatcher,
						loadBalancerID string,
						policyName string,
						_ error,
					) {
						ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(
							t.Context(),
							mock.MatchedBy(func(req loadbalancer.CreateRoutingPolicyRequest) bool {
								return lo.FromPtr(req.LoadBalancerId) == loadBalancerID &&
									lo.FromPtr(req.CreateRoutingPolicyDetails.Name) == policyName
							}),
						).Return(loadbalancer.CreateRoutingPolicyResponse{}, nil)
					},
					wantContains: "missing work request id",
				},
				"wait fails": {
					setup: func(
						t *testing.T,
						ociLoadBalancerClient *MockociLoadBalancerClient,
						workRequestsWatcher *MockworkRequestsWatcher,
						loadBalancerID string,
						policyName string,
						wantErr error,
					) {
						workRequestID := fake.UUID().V4()
						ociLoadBalancerClient.EXPECT().CreateRoutingPolicy(
							t.Context(),
							mock.MatchedBy(func(req loadbalancer.CreateRoutingPolicyRequest) bool {
								return lo.FromPtr(req.LoadBalancerId) == loadBalancerID &&
									lo.FromPtr(req.CreateRoutingPolicyDetails.Name) == policyName
							}),
						).Return(loadbalancer.CreateRoutingPolicyResponse{OpcWorkRequestId: &workRequestID}, nil)
						workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr)
					},
					wantContains: "failed to wait for routing policy",
				},
			} {
				t.Run(name, func(t *testing.T) {
					deps := makeMockDeps(t)
					model := newOciLoadBalancerModel(deps)
					ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
					workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

					loadBalancerID := fake.UUID().V4()
					listenerName := fake.UUID().V4()
					policyName := listenerPolicyName(listenerName)
					wantErr := errors.New(fake.Lorem().Sentence(10))

					ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
						RoutingPolicyName: new(policyName),
						LoadBalancerId:    &loadBalancerID,
					}).Return(loadbalancer.GetRoutingPolicyResponse{},
						ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
					tc.setup(t, ociLoadBalancerClient, workRequestsWatcher, loadBalancerID, policyName, wantErr)

					err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
						loadBalancerID: loadBalancerID,
						listenerName:   listenerName,
						policyRules:    []loadbalancer.RoutingRule{makeRandomOCIRoutingRule()},
					})

					require.Error(t, err)
					require.ErrorContains(t, err, tc.wantContains)
					if name != "create returns no work request" {
						require.ErrorIs(t, err, wantErr)
					}
				})
			}
		})

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

		t.Run("deletes routing policy when removing previous rules leaves it empty", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			previousRule := loadbalancer.RoutingRule{
				Name:      new("route-" + fake.Lorem().Word()),
				Condition: new("any(http.request.url.path sw '/')"),
			}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: loadbalancer.RoutingPolicy{
					Name:                     new(policyName),
					Rules:                    []loadbalancer.RoutingRule{previousRule},
					ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				},
			}, nil)

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteRoutingPolicy(t.Context(), loadbalancer.DeleteRoutingPolicyRequest{
				LoadBalancerId:    &loadBalancerID,
				RoutingPolicyName: &policyName,
			}).Return(loadbalancer.DeleteRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    listenerName,
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{lo.FromPtr(previousRule.Name)},
			})
			require.NoError(t, err)
			ociLoadBalancerClient.AssertNotCalled(t, "UpdateRoutingPolicy")
		})

		t.Run("orders grpc rules before http rules and default catch all", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			httpRule := loadbalancer.RoutingRule{
				Name:      new("http-api"),
				Condition: new("http.request.url.path sw '/'"),
			}
			grpcRule := loadbalancer.RoutingRule{
				Name:      new("grpc-api"),
				Condition: new(grpcContentTypeCondition()),
			}
			defaultRule := defaultCatchAllRoutingRule("default-backend")

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: loadbalancer.RoutingPolicy{
					Name:                     new(policyName),
					Rules:                    []loadbalancer.RoutingRule{httpRule, defaultRule},
					ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				},
			}, nil)

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.MatchedBy(
				func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					assert.Equal(
						t,
						[]loadbalancer.RoutingRule{grpcRule, httpRule, defaultRule},
						req.UpdateRoutingPolicyDetails.Rules,
					)
					return true
				},
			)).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    []loadbalancer.RoutingRule{httpRule, grpcRule},
			})
			require.NoError(t, err)
		})

		t.Run("serializes concurrent commits for the same routing policy", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			defaultRule := defaultCatchAllRoutingRule("default-" + fake.Lorem().Word())
			httpRule := loadbalancer.RoutingRule{
				Name:      new("http-" + fake.Lorem().Word()),
				Condition: new("http.request.url.path sw '/'"),
			}
			grpcRule := loadbalancer.RoutingRule{
				Name:      new("grpc-" + fake.Lorem().Word()),
				Condition: new(grpcContentTypeCondition()),
			}

			var policyMu sync.Mutex
			currentPolicy := loadbalancer.RoutingPolicy{
				Name:                     new(policyName),
				Rules:                    []loadbalancer.RoutingRule{defaultRule},
				ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
			}
			getCount := 0
			secondGet := make(chan struct{})

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).RunAndReturn(func(context.Context, loadbalancer.GetRoutingPolicyRequest) (loadbalancer.GetRoutingPolicyResponse, error) {
				policyMu.Lock()
				getCount++
				currentGet := getCount
				if currentGet == 2 {
					close(secondGet)
				}
				snapshot := currentPolicy
				snapshot.Rules = slices.Clone(currentPolicy.Rules)
				policyMu.Unlock()

				if currentGet == 1 {
					select {
					case <-secondGet:
					case <-time.After(50 * time.Millisecond):
					}
				}

				return loadbalancer.GetRoutingPolicyResponse{RoutingPolicy: snapshot}, nil
			}).Twice()

			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.MatchedBy(
				func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					policyMu.Lock()
					currentPolicy.Rules = slices.Clone(req.UpdateRoutingPolicyDetails.Rules)
					policyMu.Unlock()
					return true
				},
			)).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: new("wr-" + fake.UUID().V4()),
			}, nil).Twice()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), mock.Anything).Return(nil).Twice()

			var wg sync.WaitGroup
			wg.Add(2)
			errs := make(chan error, 2)
			go func() {
				defer wg.Done()
				errs <- model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID: loadBalancerID,
					listenerName:   listenerName,
					policyRules:    []loadbalancer.RoutingRule{httpRule},
				})
			}()
			go func() {
				defer wg.Done()
				errs <- model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID: loadBalancerID,
					listenerName:   listenerName,
					policyRules:    []loadbalancer.RoutingRule{grpcRule},
				})
			}()
			wg.Wait()
			close(errs)

			for err := range errs {
				require.NoError(t, err)
			}
			policyMu.Lock()
			gotRules := slices.Clone(currentPolicy.Rules)
			policyMu.Unlock()

			assert.ElementsMatch(t, []loadbalancer.RoutingRule{grpcRule, httpRule, defaultRule}, gotRules)
			assert.Empty(t, model.routingPolicyLocks.locks)
		})

		t.Run("routing policy locks clean up after waiting operations finish", func(t *testing.T) {
			fake := faker.New()
			locks := routingPolicyLocks{}
			lockKey := routingPolicyLockKey(fake.UUID().V4(), listenerPolicyName(fake.UUID().V4()))
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			firstDone := make(chan error, 1)
			secondDone := make(chan error, 1)

			go func() {
				firstDone <- locks.withLock(lockKey, func() error {
					close(firstStarted)
					<-releaseFirst
					return nil
				})
			}()

			select {
			case <-firstStarted:
			case <-time.After(time.Second):
				require.Fail(t, "timed out waiting for first operation")
			}

			go func() {
				secondDone <- locks.withLock(lockKey, func() error {
					return nil
				})
			}()

			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				locks.mu.Lock()
				defer locks.mu.Unlock()

				assert.Len(collect, locks.locks, 1)
				if lock := locks.locks[lockKey]; assert.NotNil(collect, lock) {
					assert.Equal(collect, 2, lock.refs)
				}
			}, time.Second, 10*time.Millisecond)

			close(releaseFirst)

			select {
			case err := <-firstDone:
				require.NoError(t, err)
			case <-time.After(time.Second):
				require.Fail(t, "timed out waiting for first operation")
			}

			select {
			case err := <-secondDone:
				require.NoError(t, err)
			case <-time.After(time.Second):
				require.Fail(t, "timed out waiting for second operation")
			}

			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				locks.mu.Lock()
				defer locks.mu.Unlock()
				assert.Empty(collect, locks.locks)
			}, time.Second, 10*time.Millisecond)
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

		t.Run("updates routing policy when rule condition changes", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			loadBalancerID := fake.UUID().V4()
			listenerName := fake.UUID().V4()
			policyName := listenerPolicyName(listenerName)
			ruleName := "http-" + fake.Lorem().Word()
			existingRule := loadbalancer.RoutingRule{
				Name:      new(ruleName),
				Condition: new(`http.request.headers[(i 'Host')][0] sw (i 'old-prefix.')`),
			}
			desiredRule := loadbalancer.RoutingRule{
				Name:      new(ruleName),
				Condition: new(`http.request.headers[(i 'Host')][0] sw (i 'new-prefix.')`),
			}

			ociLoadBalancerClient.EXPECT().GetRoutingPolicy(t.Context(), loadbalancer.GetRoutingPolicyRequest{
				RoutingPolicyName: new(policyName),
				LoadBalancerId:    &loadBalancerID,
			}).Return(loadbalancer.GetRoutingPolicyResponse{
				RoutingPolicy: loadbalancer.RoutingPolicy{
					Name:                     new(policyName),
					Rules:                    []loadbalancer.RoutingRule{existingRule},
					ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
				},
			}, nil)

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().UpdateRoutingPolicy(t.Context(), mock.MatchedBy(
				func(req loadbalancer.UpdateRoutingPolicyRequest) bool {
					assert.Equal(t, []loadbalancer.RoutingRule{desiredRule}, req.UpdateRoutingPolicyDetails.Rules)
					return true
				},
			)).Return(loadbalancer.UpdateRoutingPolicyResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil)
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil)

			err := model.commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID: loadBalancerID,
				listenerName:   listenerName,
				policyRules:    []loadbalancer.RoutingRule{desiredRule},
			})
			require.NoError(t, err)
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
				routeNamespace: httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
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
				routeNamespace: httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
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
				routeNamespace: httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
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
				routeNamespace: httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
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
				routeNamespace: httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
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
				routeNamespace: httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
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

	t.Run("backendSetReferenced", func(t *testing.T) {
		t.Run("returns true when routing policy forwards to backend set", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			backendSetName := "backend-" + fake.Lorem().Word()
			policyName := "policy-" + fake.Lorem().Word()
			loadBalancer := makeRandomOCILoadBalancer()
			loadBalancer.RoutingPolicies = map[string]loadbalancer.RoutingPolicy{
				policyName: {
					Name: new(policyName),
					Rules: []loadbalancer.RoutingRule{
						defaultCatchAllRoutingRule(backendSetName),
					},
				},
			}

			ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
				LoadBalancerId: &loadBalancerID,
			}).Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadBalancer,
			}, nil).Once()

			referenced, err := model.backendSetReferenced(t.Context(), loadBalancerID, backendSetName)

			require.NoError(t, err)
			assert.True(t, referenced)
		})

		t.Run("returns true when any routing rule action forwards to backend set", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			backendSetName := "backend-" + fake.Lorem().Word()
			otherBackendSetName := "other-backend-" + fake.Lorem().Word()
			policyName := "policy-" + fake.Lorem().Word()
			ruleName := "rule-" + fake.Lorem().Word()
			condition := "any(http.request.url.path sw '/')"
			loadBalancer := makeRandomOCILoadBalancer()
			loadBalancer.RoutingPolicies = map[string]loadbalancer.RoutingPolicy{
				policyName: {
					Name: new(policyName),
					Rules: []loadbalancer.RoutingRule{
						{
							Name:      new(ruleName),
							Condition: new(condition),
							Actions: []loadbalancer.Action{
								loadbalancer.ForwardToBackendSet{BackendSetName: new(otherBackendSetName)},
								loadbalancer.ForwardToBackendSet{BackendSetName: new(backendSetName)},
							},
						},
					},
				},
			}

			ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
				LoadBalancerId: &loadBalancerID,
			}).Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadBalancer,
			}, nil).Once()

			referenced, err := model.backendSetReferenced(t.Context(), loadBalancerID, backendSetName)

			require.NoError(t, err)
			assert.True(t, referenced)
		})

		t.Run("returns false when no routing policy forwards to backend set", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			backendSetName := "backend-" + fake.Lorem().Word()
			otherBackendSetName := "other-backend-" + fake.Lorem().Word()
			policyName := "policy-" + fake.Lorem().Word()
			loadBalancer := makeRandomOCILoadBalancer()
			loadBalancer.RoutingPolicies = map[string]loadbalancer.RoutingPolicy{
				policyName: {
					Name: new(policyName),
					Rules: []loadbalancer.RoutingRule{
						defaultCatchAllRoutingRule(otherBackendSetName),
					},
				},
			}

			ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
				LoadBalancerId: &loadBalancerID,
			}).Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadBalancer,
			}, nil).Once()

			referenced, err := model.backendSetReferenced(t.Context(), loadBalancerID, backendSetName)

			require.NoError(t, err)
			assert.False(t, referenced)
		})

		t.Run("returns error when load balancer lookup fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			loadBalancerID := fake.UUID().V4()
			backendSetName := "backend-" + fake.Lorem().Word()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
				LoadBalancerId: &loadBalancerID,
			}).Return(loadbalancer.GetLoadBalancerResponse{}, wantErr).Once()

			referenced, err := model.backendSetReferenced(t.Context(), loadBalancerID, backendSetName)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.False(t, referenced)
		})
	})

	t.Run("removeUnusedCertificates", func(t *testing.T) {
		makeManagedCertificate := func(namespace, name, resourceVersion string) loadbalancer.Certificate {
			certName := fmt.Sprintf("%s-%s-rev-%s", namespace, name, resourceVersion)
			cert := makeRandomOCICertificate()
			cert.CertificateName = new(certName)
			return cert
		}

		t.Run("extracts unique listener certificate names", func(t *testing.T) {
			firstCert := makeRandomOCICertificate()
			secondCert := makeRandomOCICertificate()

			got := certificateNamesFromListenerCertificates(map[string][]loadbalancer.Certificate{
				"listener-a": {firstCert, {CertificateName: nil}, secondCert},
				"listener-b": {firstCert},
			})

			assert.ElementsMatch(t, []string{
				lo.FromPtr(firstCert.CertificateName),
				lo.FromPtr(secondCert.CertificateName),
			}, got)
		})

		t.Run("extracts certificate names deterministically across listener order", func(t *testing.T) {
			fake := faker.New()
			firstCertName := "cert-a-" + fake.UUID().V4()
			secondCertName := "cert-b-" + fake.UUID().V4()
			thirdCertName := "cert-c-" + fake.UUID().V4()
			firstCert := loadbalancer.Certificate{CertificateName: new(firstCertName)}
			secondCert := loadbalancer.Certificate{CertificateName: new(secondCertName)}
			thirdCert := loadbalancer.Certificate{CertificateName: new(thirdCertName)}

			got := certificateNamesFromListenerCertificates(map[string][]loadbalancer.Certificate{
				"listener-b": {thirdCert, firstCert},
				"listener-a": {secondCert, firstCert},
			})

			assert.Equal(t, []string{firstCertName, secondCertName, thirdCertName}, got)
			assert.Equal(t, got, certificateNamesFromListenerCertificates(map[string][]loadbalancer.Certificate{
				"listener-a": {firstCert, secondCert},
				"listener-b": {firstCert, thirdCert},
			}))
		})

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

		t.Run("removes stale frontend mTLS certificate aliases", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			usedCert := makeManagedCertificate("default", "gateway-tls", fake.UUID().V4())
			staleFrontendMTLSCert := makeRandomOCICertificate()
			staleFrontendMTLSCertName := lo.FromPtr(usedCert.CertificateName) +
				"-fmtls-19443-" + fake.RandomStringWithLength(8)
			staleFrontendMTLSCert.CertificateName = &staleFrontendMTLSCertName
			externalFrontendMTLSLikeCert := makeRandomOCICertificate()
			externalFrontendMTLSLikeCertName := "external-gateway-tls-rev-" + fake.UUID().V4() +
				"-fmtls-19443-" + fake.RandomStringWithLength(8)
			externalFrontendMTLSLikeCert.CertificateName = &externalFrontendMTLSLikeCertName

			params := removeUnusedCertificatesParams{
				loadBalancerID:      fake.UUID().V4(),
				desiredCertificates: []string{lo.FromPtr(usedCert.CertificateName)},
				knownCertificates: map[string]loadbalancer.Certificate{
					lo.FromPtr(usedCert.CertificateName):                     usedCert,
					lo.FromPtr(staleFrontendMTLSCert.CertificateName):        staleFrontendMTLSCert,
					lo.FromPtr(externalFrontendMTLSLikeCert.CertificateName): externalFrontendMTLSLikeCert,
				},
			}

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: staleFrontendMTLSCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{OpcWorkRequestId: &workRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

			err := model.removeUnusedCertificates(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("removes frontend mTLS certificate aliases for previously programmed certificates", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			previousCert := makeManagedCertificate("default", "gateway-tls", fake.UUID().V4())
			previousCertName := lo.FromPtr(previousCert.CertificateName)
			frontendMTLSCert := makeRandomOCICertificate()
			frontendMTLSCertName := previousCertName + "-fmtls-19443-" + fake.RandomStringWithLength(8)
			frontendMTLSCert.CertificateName = &frontendMTLSCertName

			params := removeUnusedCertificatesParams{
				loadBalancerID:                   fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{previousCertName},
				knownCertificates: map[string]loadbalancer.Certificate{
					previousCertName:       previousCert,
					frontendMTLSCertName:   frontendMTLSCert,
					fake.UUID().V4() + "x": makeRandomOCICertificate(),
				},
			}

			previousWorkRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: previousCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{OpcWorkRequestId: &previousWorkRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), previousWorkRequestID).Return(nil).Once()

			frontendMTLSWorkRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: frontendMTLSCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{OpcWorkRequestId: &frontendMTLSWorkRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), frontendMTLSWorkRequestID).Return(nil).Once()

			err := model.removeUnusedCertificates(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("removes stale frontend mTLS certificate aliases after CA rotation", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			baseCert := makeManagedCertificate("default", "gateway-tls", fake.UUID().V4())
			currentFrontendMTLSCert := makeRandomOCICertificate()
			currentFrontendMTLSCertName := lo.FromPtr(baseCert.CertificateName) +
				"-fmtls-19443-current-" + fake.RandomStringWithLength(8)
			currentFrontendMTLSCert.CertificateName = &currentFrontendMTLSCertName
			staleFrontendMTLSCert := makeRandomOCICertificate()
			staleFrontendMTLSCertName := lo.FromPtr(baseCert.CertificateName) +
				"-fmtls-19443-stale-" + fake.RandomStringWithLength(8)
			staleFrontendMTLSCert.CertificateName = &staleFrontendMTLSCertName

			params := removeUnusedCertificatesParams{
				loadBalancerID:      fake.UUID().V4(),
				desiredCertificates: []string{lo.FromPtr(currentFrontendMTLSCert.CertificateName)},
				knownCertificates: map[string]loadbalancer.Certificate{
					lo.FromPtr(currentFrontendMTLSCert.CertificateName): currentFrontendMTLSCert,
					lo.FromPtr(staleFrontendMTLSCert.CertificateName):   staleFrontendMTLSCert,
				},
			}

			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: staleFrontendMTLSCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{OpcWorkRequestId: &workRequestID}, nil).Once()
			workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

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

		t.Run("returns error when only certificate delete has no work request id", func(t *testing.T) {
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

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("returns error when certificate delete has no work request id", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			unusedCert := makeManagedCertificate("default", "unused", fake.UUID().V4())
			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(unusedCert.CertificateName),
				},
				knownCertificates: map[string]loadbalancer.Certificate{
					lo.FromPtr(unusedCert.CertificateName): unusedCert,
				},
			}

			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{}, nil).Once()

			err := model.removeUnusedCertificates(t.Context(), params)

			require.ErrorContains(t, err, "missing work request id")
		})

		t.Run("continues deletion even if one fails and returns error", func(t *testing.T) {
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
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error when certificate delete fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)

			unusedCert := makeManagedCertificate("default", "unused", fake.UUID().V4())
			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(unusedCert.CertificateName),
				},
				knownCertificates: map[string]loadbalancer.Certificate{
					lo.FromPtr(unusedCert.CertificateName): unusedCert,
				},
			}

			wantErr := errors.New(fake.Lorem().Sentence(10))
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{}, wantErr).Once()

			err := model.removeUnusedCertificates(t.Context(), params)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error when certificate delete wait fails", func(t *testing.T) {
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
			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns error when only certificate delete wait fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			unusedCert := makeManagedCertificate("default", "unused", fake.UUID().V4())
			params := removeUnusedCertificatesParams{
				loadBalancerID: fake.UUID().V4(),
				previouslyProgrammedCertificates: []string{
					lo.FromPtr(unusedCert.CertificateName),
				},
				knownCertificates: map[string]loadbalancer.Certificate{
					lo.FromPtr(unusedCert.CertificateName): unusedCert,
				},
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

			require.ErrorIs(t, err, wantErr)
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

func TestOciLoadBalancerModelImpl_ensureGRPCListenerProtocol(t *testing.T) {
	t.Run("updates listener protocol to GRPC and preserves existing listener settings", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: workRequestsWatcher,
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()
		workRequestID := fake.UUID().V4()
		backendSetName := "backend-" + fake.Lorem().Word()
		port := rand.IntN(60000) + 1
		routingPolicyName := listenerPolicyName(listenerName)
		pathRouteSetName := "path-" + fake.Lorem().Word()
		ruleSetName := "rule-" + fake.Lorem().Word()
		hostnameName := "host-" + fake.Lorem().Word()
		protocol := ociListenerProtocolHTTP
		sslConfig := &loadbalancer.SslConfiguration{
			CertificateIds:        []string{fake.UUID().V4()},
			CertificateName:       new("cert-" + fake.Lorem().Word()),
			HasSessionResumption:  new(true),
			ServerOrderPreference: loadbalancer.SslConfigurationServerOrderPreferenceEnabled,
		}
		connectionConfig := &loadbalancer.ConnectionConfiguration{
			IdleTimeout: new(int64(rand.IntN(300) + 1)),
		}

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: new(loadBalancerID),
		}).Return(loadbalancer.GetLoadBalancerResponse{
			LoadBalancer: loadbalancer.LoadBalancer{
				Listeners: map[string]loadbalancer.Listener{
					listenerName: {
						Name:                    new(listenerName),
						DefaultBackendSetName:   new(backendSetName),
						Port:                    new(port),
						Protocol:                new(protocol),
						HostnameNames:           []string{hostnameName},
						PathRouteSetName:        new(pathRouteSetName),
						SslConfiguration:        sslConfig,
						ConnectionConfiguration: connectionConfig,
						RuleSetNames:            []string{ruleSetName},
						RoutingPolicyName:       new(routingPolicyName),
					},
				},
			},
		}, nil).Once()
		ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
			LoadBalancerId: new(loadBalancerID),
			ListenerName:   new(listenerName),
			UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
				DefaultBackendSetName:   new(backendSetName),
				Port:                    new(port),
				Protocol:                new(ociListenerProtocolGRPC),
				HostnameNames:           []string{hostnameName},
				PathRouteSetName:        new(pathRouteSetName),
				RoutingPolicyName:       new(routingPolicyName),
				SslConfiguration:        sslConfigurationDetailsFromBackendSet(sslConfig),
				ConnectionConfiguration: connectionConfig,
				RuleSetNames:            []string{ruleSetName},
			},
		}).Return(loadbalancer.UpdateListenerResponse{
			OpcWorkRequestId: new(workRequestID),
		}, nil).Once()
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.NoError(t, err)
	})

	t.Run("updates HTTP2 listener protocol to GRPC and preserves existing listener settings", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: workRequestsWatcher,
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()
		workRequestID := fake.UUID().V4()
		backendSetName := "backend-" + fake.Lorem().Word()
		port := rand.IntN(60000) + 1
		routingPolicyName := listenerPolicyName(listenerName)
		pathRouteSetName := "path-" + fake.Lorem().Word()
		ruleSetName := "rule-" + fake.Lorem().Word()
		hostnameName := "host-" + fake.Lorem().Word()
		protocol := ociListenerProtocolHTTP2
		sslConfig := &loadbalancer.SslConfiguration{
			CertificateIds:        []string{fake.UUID().V4()},
			CertificateName:       new("cert-" + fake.Lorem().Word()),
			HasSessionResumption:  new(true),
			ServerOrderPreference: loadbalancer.SslConfigurationServerOrderPreferenceEnabled,
		}
		connectionConfig := &loadbalancer.ConnectionConfiguration{
			IdleTimeout: new(int64(rand.IntN(300) + 1)),
		}

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: new(loadBalancerID),
		}).Return(loadbalancer.GetLoadBalancerResponse{
			LoadBalancer: loadbalancer.LoadBalancer{
				Listeners: map[string]loadbalancer.Listener{
					listenerName: {
						Name:                    new(listenerName),
						DefaultBackendSetName:   new(backendSetName),
						Port:                    new(port),
						Protocol:                new(protocol),
						HostnameNames:           []string{hostnameName},
						PathRouteSetName:        new(pathRouteSetName),
						SslConfiguration:        sslConfig,
						ConnectionConfiguration: connectionConfig,
						RuleSetNames:            []string{ruleSetName},
						RoutingPolicyName:       new(routingPolicyName),
					},
				},
			},
		}, nil).Once()
		ociLoadBalancerClient.EXPECT().UpdateListener(t.Context(), loadbalancer.UpdateListenerRequest{
			LoadBalancerId: new(loadBalancerID),
			ListenerName:   new(listenerName),
			UpdateListenerDetails: loadbalancer.UpdateListenerDetails{
				DefaultBackendSetName:   new(backendSetName),
				Port:                    new(port),
				Protocol:                new(ociListenerProtocolGRPC),
				HostnameNames:           []string{hostnameName},
				PathRouteSetName:        new(pathRouteSetName),
				RoutingPolicyName:       new(routingPolicyName),
				SslConfiguration:        sslConfigurationDetailsFromBackendSet(sslConfig),
				ConnectionConfiguration: connectionConfig,
				RuleSetNames:            []string{ruleSetName},
			},
		}).Return(loadbalancer.UpdateListenerResponse{
			OpcWorkRequestId: new(workRequestID),
		}, nil).Once()
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(nil).Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.NoError(t, err)
	})

	t.Run("skips update when listener already uses GRPC", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: new(loadBalancerID),
		}).Return(loadbalancer.GetLoadBalancerResponse{
			LoadBalancer: loadbalancer.LoadBalancer{
				Listeners: map[string]loadbalancer.Listener{
					listenerName: {
						Name:     new(listenerName),
						Protocol: new(ociListenerProtocolGRPC),
					},
				},
			},
		}, nil).Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.NoError(t, err)
		ociLoadBalancerClient.AssertNotCalled(t, "UpdateListener")
	})

	t.Run("returns load balancer lookup errors", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		wantErr := errors.New(fake.Lorem().Sentence(10))

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: new(loadBalancerID),
		}).Return(loadbalancer.GetLoadBalancerResponse{}, wantErr).Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   "grpc-" + fake.Lorem().Word(),
		})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns listener not found errors", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: new(loadBalancerID),
		}).Return(loadbalancer.GetLoadBalancerResponse{
			LoadBalancer: loadbalancer.LoadBalancer{
				Listeners: map[string]loadbalancer.Listener{
					"other-" + fake.Lorem().Word(): makeRandomOCIListener(),
				},
			},
		}, nil).Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.ErrorContains(t, err, "listener "+listenerName+" not found")
	})

	t.Run("returns update listener errors", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()
		wantErr := errors.New(fake.Lorem().Sentence(10))

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
			LoadBalancerId: new(loadBalancerID),
		}).Return(loadbalancer.GetLoadBalancerResponse{
			LoadBalancer: loadbalancer.LoadBalancer{
				Listeners: map[string]loadbalancer.Listener{
					listenerName: makeRandomOCIListener(func(listener *loadbalancer.Listener) {
						listener.Protocol = new(ociListenerProtocolHTTP)
					}),
				},
			},
		}, nil).Once()
		ociLoadBalancerClient.EXPECT().
			UpdateListener(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateListenerResponse{}, wantErr).
			Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns missing work request id errors", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: NewMockworkRequestsWatcher(t),
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{
					Listeners: map[string]loadbalancer.Listener{
						listenerName: makeRandomOCIListener(func(listener *loadbalancer.Listener) {
							listener.Protocol = new(ociListenerProtocolHTTP)
						}),
					},
				},
			}, nil).
			Once()
		ociLoadBalancerClient.EXPECT().
			UpdateListener(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateListenerResponse{}, nil).
			Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.ErrorContains(t, err, "missing work request id")
	})

	t.Run("returns work request wait errors", func(t *testing.T) {
		fake := faker.New()
		ociLoadBalancerClient := NewMockociLoadBalancerClient(t)
		workRequestsWatcher := NewMockworkRequestsWatcher(t)
		model := newOciLoadBalancerModel(ociLoadBalancerModelDeps{
			RootLogger:          diag.RootTestLogger(),
			OciClient:           ociLoadBalancerClient,
			K8sClient:           NewMockk8sClient(t),
			WorkRequestsWatcher: workRequestsWatcher,
			RoutingRulesMapper:  NewMockociLoadBalancerRoutingRulesMapper(t),
		})
		loadBalancerID := fake.UUID().V4()
		listenerName := "grpc-" + fake.Lorem().Word()
		workRequestID := fake.UUID().V4()
		wantErr := errors.New(fake.Lorem().Sentence(10))

		ociLoadBalancerClient.EXPECT().GetLoadBalancer(t.Context(), mock.Anything).
			Return(loadbalancer.GetLoadBalancerResponse{
				LoadBalancer: loadbalancer.LoadBalancer{
					Listeners: map[string]loadbalancer.Listener{
						listenerName: makeRandomOCIListener(func(listener *loadbalancer.Listener) {
							listener.Protocol = new(ociListenerProtocolHTTP)
						}),
					},
				},
			}, nil).
			Once()
		ociLoadBalancerClient.EXPECT().
			UpdateListener(t.Context(), mock.Anything).
			Return(loadbalancer.UpdateListenerResponse{OpcWorkRequestId: new(workRequestID)}, nil).
			Once()
		workRequestsWatcher.EXPECT().WaitFor(t.Context(), workRequestID).Return(wantErr).Once()

		err := model.ensureGRPCListenerProtocol(t.Context(), ensureGRPCListenerProtocolParams{
			loadBalancerID: loadBalancerID,
			listenerName:   listenerName,
		})

		require.ErrorIs(t, err, wantErr)
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

func Test_ociBackendSetNameFromBackendObjectRef(t *testing.T) {
	t.Run("includes backend port in identity", func(t *testing.T) {
		fake := faker.New()
		namespace := "apps" + fake.Numerify("###")
		backendName := gatewayv1.ObjectName("svc" + fake.Numerify("###"))
		oldPort := gatewayv1.PortNumber(fake.IntBetween(1024, 32767))
		newPort := gatewayv1.PortNumber(fake.IntBetween(32768, 65535))

		oldName := ociBackendSetNameFromBackendObjectRef(namespace, gatewayv1.BackendObjectReference{
			Name: backendName,
			Port: &oldPort,
		})
		newName := ociBackendSetNameFromBackendObjectRef(namespace, gatewayv1.BackendObjectReference{
			Name: backendName,
			Port: &newPort,
		})

		assert.NotEqual(t, oldName, newName)
		assert.Contains(t, oldName, fmt.Sprintf("-%d", oldPort))
		assert.Contains(t, newName, fmt.Sprintf("-%d", newPort))
		assert.LessOrEqual(t, len(oldName), maxBackendSetNameLength)
		assert.LessOrEqual(t, len(newName), maxBackendSetNameLength)
	})

	t.Run("defaults backend namespace before constructing identity", func(t *testing.T) {
		fake := faker.New()
		defaultNamespace := "default" + fake.Numerify("###")
		explicitNamespace := gatewayv1.Namespace("explicit" + fake.Numerify("###"))
		backendName := gatewayv1.ObjectName("svc" + fake.Numerify("###"))
		port := gatewayv1.PortNumber(fake.IntBetween(1024, 65535))

		defaulted := ociBackendSetNameFromBackendObjectRef(defaultNamespace, gatewayv1.BackendObjectReference{
			Name: backendName,
			Port: &port,
		})
		explicit := ociBackendSetNameFromBackendObjectRef(defaultNamespace, gatewayv1.BackendObjectReference{
			Namespace: &explicitNamespace,
			Name:      backendName,
			Port:      &port,
		})

		assert.NotEqual(t, defaulted, explicit)
		assert.Contains(t, defaulted, defaultNamespace)
		assert.Contains(t, explicit, string(explicitNamespace))
	})
}

func Test_listenerPolicyName(t *testing.T) {
	t.Run("preserves existing valid listener policy names", func(t *testing.T) {
		fake := faker.New()
		listenerName := fake.Numerify("listener_########")

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

	t.Run("always returns deterministic OCI safe names for unsafe inputs", func(t *testing.T) {
		fake := faker.New()
		listenerNames := []string{
			"valid_" + fake.Numerify("########"),
			"hyphen-" + fake.Numerify("########"),
			"dot." + fake.Numerify("########"),
			"slash/" + fake.Numerify("########"),
			"space " + fake.Numerify("########"),
			"9digit_" + fake.Numerify("########"),
			"long-" + fake.Numerify("################################################"),
		}
		seen := map[string]string{}

		for _, listenerName := range listenerNames {
			got := listenerPolicyName(listenerName)
			assert.Equal(t, got, listenerPolicyName(listenerName))
			assert.True(t, isValidOCIRoutingPolicyName(got))
			assert.False(t, invalidCharsForPolicyNamePattern.MatchString(got))
			assert.LessOrEqual(t, len(got), maxListenerPolicyNameLength)

			previousInput, exists := seen[got]
			assert.False(
				t,
				exists,
				"listener names %q and %q generated the same OCI policy name %q",
				previousInput,
				listenerName,
				got,
			)
			seen[got] = listenerName
		}
	})
}

func Test_sortRoutingRules(t *testing.T) {
	t.Run("keeps native grpc rules before http rules and catch all last", func(t *testing.T) {
		fake := faker.New()
		httpRuleName := "p0010_" + fake.Numerify("########") + "_http"
		grpcRuleName := "p0011_" + fake.Numerify("########") + "_grpc"
		firstHTTPRuleName := "p0001_" + fake.Numerify("########") + "_http"
		httpCondition := "any(http.request.url.path sw '/')"
		grpcCondition := "any(http.request.headers[(i 'content-type')][0] eq (i 'application/grpc'))"

		rules := []loadbalancer.RoutingRule{
			{Name: new(defaultCatchAllRuleName), Condition: new(httpCondition)},
			{Name: new(httpRuleName), Condition: new(httpCondition)},
			{Name: new(grpcRuleName), Condition: new(grpcCondition)},
			{Name: new(firstHTTPRuleName), Condition: new(httpCondition)},
		}

		sortRoutingRules(rules)

		assert.Equal(t, []string{
			grpcRuleName,
			firstHTTPRuleName,
			httpRuleName,
			defaultCatchAllRuleName,
		}, lo.Map(rules, func(rule loadbalancer.RoutingRule, _ int) string {
			return lo.FromPtr(rule.Name)
		}))
	})

	t.Run("is stable across input permutations", func(t *testing.T) {
		fake := faker.New()
		grpcCondition := "any(http.request.headers[(i 'content-type')][0] eq (i 'application/grpc'))"
		httpCondition := "any(http.request.url.path sw '/')"
		grpcRule := loadbalancer.RoutingRule{
			Name:      new("p0003_" + fake.Numerify("########") + "_grpc"),
			Condition: &grpcCondition,
		}
		firstHTTPRule := loadbalancer.RoutingRule{
			Name:      new("p0001_" + fake.Numerify("########") + "_http"),
			Condition: &httpCondition,
		}
		secondHTTPRule := loadbalancer.RoutingRule{
			Name:      new("p0002_" + fake.Numerify("########") + "_http"),
			Condition: &httpCondition,
		}
		defaultRule := defaultCatchAllRoutingRule("default-" + fake.Lorem().Word())
		wantNames := []string{
			lo.FromPtr(grpcRule.Name),
			lo.FromPtr(firstHTTPRule.Name),
			lo.FromPtr(secondHTTPRule.Name),
			defaultCatchAllRuleName,
		}

		for _, rules := range [][]loadbalancer.RoutingRule{
			{defaultRule, secondHTTPRule, grpcRule, firstHTTPRule},
			{firstHTTPRule, grpcRule, defaultRule, secondHTTPRule},
			{secondHTTPRule, firstHTTPRule, defaultRule, grpcRule},
		} {
			sortRoutingRules(rules)
			assert.Equal(t, wantNames, lo.Map(rules, func(rule loadbalancer.RoutingRule, _ int) string {
				return lo.FromPtr(rule.Name)
			}))
		}
	})

	t.Run("routing rule equality ignores order but detects semantic changes", func(t *testing.T) {
		fake := faker.New()
		firstRuleName := "p0001_" + fake.Numerify("########") + "_http"
		secondRuleName := "p0002_" + fake.Numerify("########") + "_http"
		firstCondition := "any(http.request.url.path sw '/" + fake.Lorem().Word() + "')"
		secondCondition := "any(http.request.url.path sw '/" + fake.Lorem().Word() + "')"
		firstBackendSet := "backend_" + fake.Numerify("########")
		secondBackendSet := "backend_" + fake.Numerify("########")
		firstRule := loadbalancer.RoutingRule{
			Name:      new(firstRuleName),
			Condition: new(firstCondition),
			Actions: []loadbalancer.Action{loadbalancer.ForwardToBackendSet{
				BackendSetName: new(firstBackendSet),
			}},
		}
		secondRule := loadbalancer.RoutingRule{
			Name:      new(secondRuleName),
			Condition: new(secondCondition),
			Actions: []loadbalancer.Action{loadbalancer.ForwardToBackendSet{
				BackendSetName: new(secondBackendSet),
			}},
		}

		assert.True(t, routingRulesEqual(
			[]loadbalancer.RoutingRule{firstRule, secondRule},
			[]loadbalancer.RoutingRule{secondRule, firstRule},
		))

		changedRule := firstRule
		changedRule.Actions = []loadbalancer.Action{loadbalancer.ForwardToBackendSet{
			BackendSetName: new(secondBackendSet),
		}}
		assert.False(t, routingRulesEqual(
			[]loadbalancer.RoutingRule{firstRule, secondRule},
			[]loadbalancer.RoutingRule{changedRule, secondRule},
		))

		changedRule = firstRule
		changedRule.Condition = new("any(http.request.url.path sw '/" + fake.Lorem().Word() + "')")
		assert.False(t, routingRulesEqual(
			[]loadbalancer.RoutingRule{firstRule, secondRule},
			[]loadbalancer.RoutingRule{changedRule, secondRule},
		))

		changedRule = firstRule
		changedRule.Name = new("p0003_" + fake.Numerify("########") + "_http")
		assert.False(t, routingRulesEqual(
			[]loadbalancer.RoutingRule{firstRule, secondRule},
			[]loadbalancer.RoutingRule{changedRule, secondRule},
		))
		assert.False(t, routingRulesEqual(
			[]loadbalancer.RoutingRule{firstRule, secondRule},
			[]loadbalancer.RoutingRule{firstRule},
		))
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
			wantName := fmt.Sprintf("%s-%s-%d", refNamespace, refName, lo.FromPtr(backendRef.Port))
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
			wantName := fmt.Sprintf("%s-%s-%d", routeNs, refName, lo.FromPtr(backendRef.Port))
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
			originalName := fmt.Sprintf("%s-%s-%d", longRefNs, longRefName, lo.FromPtr(backendRef.Port))
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

	t.Run("keeps backend refs unique by namespace name and port", func(t *testing.T) {
		fake := faker.New()
		namespace := fake.Lorem().Word() + "-ns"
		serviceName := fake.Internet().Slug() + "-svc"
		firstPort := gatewayv1.PortNumber(fake.IntBetween(1024, 30000))
		secondPort := firstPort + 1
		httpRoute := makeRandomHTTPRoute(
			randomHTTPRouteWithNamespaceOpt(namespace),
		)
		firstBackendRef := makeRandomBackendRef(func(br *gatewayv1.HTTPBackendRef) {
			br.Name = gatewayv1.ObjectName(serviceName)
			br.Namespace = nil
			br.Port = &firstPort
		})
		secondBackendRef := makeRandomBackendRef(func(br *gatewayv1.HTTPBackendRef) {
			br.Name = gatewayv1.ObjectName(serviceName)
			br.Namespace = nil
			br.Port = &secondPort
		})

		firstName := ociBackendSetNameFromBackendRef(httpRoute, firstBackendRef)
		secondName := ociBackendSetNameFromBackendRef(httpRoute, secondBackendRef)

		assert.Equal(t, firstName, ociBackendSetNameFromBackendRef(httpRoute, firstBackendRef))
		assert.Equal(t, secondName, ociBackendSetNameFromBackendRef(httpRoute, secondBackendRef))
		assert.NotEqual(t, firstName, secondName)
		assert.LessOrEqual(t, len(firstName), maxBackendSetNameLength)
		assert.LessOrEqual(t, len(secondName), maxBackendSetNameLength)
	})
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
			CertificateName:                details.CertificateName,
			CertificateIds:                 details.CertificateIds,
			CipherSuiteName:                details.CipherSuiteName,
			Protocols:                      details.Protocols,
			HasSessionResumption:           details.HasSessionResumption,
			VerifyPeerCertificate:          details.VerifyPeerCertificate,
			VerifyDepth:                    details.VerifyDepth,
			TrustedCertificateAuthorityIds: details.TrustedCertificateAuthorityIds,
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
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			certName := fake.UUID().V4()
			verifyPeer := true
			verifyDepth := 3
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName:                &certName,
				VerifyPeerCertificate:          &verifyPeer,
				VerifyDepth:                    &verifyDepth,
				TrustedCertificateAuthorityIds: []string{"ca-b", "ca-a"},
			}

			return testCase{
				name: "ssl config frontend mTLS CA ids match regardless of order",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration: &loadbalancer.SslConfiguration{
							CertificateName:                &certName,
							VerifyPeerCertificate:          &verifyPeer,
							VerifyDepth:                    &verifyDepth,
							TrustedCertificateAuthorityIds: []string{"ca-a", "ca-b"},
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
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			certName := fake.UUID().V4()
			verifyPeer := true
			oldVerifyDepth := 3
			newVerifyDepth := 4
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName:                &certName,
				VerifyPeerCertificate:          &verifyPeer,
				VerifyDepth:                    &newVerifyDepth,
				TrustedCertificateAuthorityIds: []string{"ca-a"},
			}

			return testCase{
				name: "ssl config frontend mTLS verify depth change",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration: &loadbalancer.SslConfiguration{
							CertificateName:                &certName,
							VerifyPeerCertificate:          &verifyPeer,
							VerifyDepth:                    &oldVerifyDepth,
							TrustedCertificateAuthorityIds: []string{"ca-a"},
						},
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
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()

			return testCase{
				name: "preserves existing GRPC listener protocol",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new(ociListenerProtocolGRPC),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
					preserveGRPC:          true,
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
				name: "preserves existing GRPC listener protocol while updating other fields",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new(ociListenerProtocolGRPC),
						Port:                  new(int(newPort)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
					preserveGRPC:          true,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new(ociListenerProtocolGRPC),
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
				name: "repairs HTTP2 protocol drift when listener has no GRPCRoute rules",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new(ociListenerProtocolHTTP2),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
					},
					listenerName:          listenerName,
					listenerSpec:          &listenerSpec,
					defaultBackendSetName: defaultBackendSetName,
				},
				want: loadbalancer.UpdateListenerDetails{
					Protocol:              new(ociListenerProtocolHTTP),
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
			oldCipherSuite := "oci-default-ssl-cipher-suite-v1"
			newCipherSuite := "oci-tls-12-13-ssl-cipher-suite-v3"
			oldSslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
				CipherSuiteName: &oldCipherSuite,
				Protocols:       []string{"TLSv1.2"},
			}
			newSslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
				CipherSuiteName: &newCipherSuite,
				Protocols:       []string{"TLSv1.2", "TLSv1.3"},
			}

			return testCase{
				name: "ssl config cipher suite and protocols change",
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
			cipherSuite := "oci-tls-12-13-ssl-cipher-suite-v3"
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
				CipherSuiteName: &cipherSuite,
				Protocols:       []string{"TLSv1.3", "TLSv1.2"},
			}

			return testCase{
				name: "ssl config protocols match regardless of order",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration: &loadbalancer.SslConfiguration{
							CertificateName: &certName,
							CipherSuiteName: &cipherSuite,
							Protocols:       []string{"TLSv1.2", "TLSv1.3"},
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
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			certName := fake.UUID().V4()
			cipherSuite := "oci-default-ssl-cipher-suite-v1"
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
			}

			return testCase{
				name: "ssl config ignores OCI default cipher suite and protocols when desired options are absent",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration: &loadbalancer.SslConfiguration{
							CertificateName: &certName,
							CipherSuiteName: &cipherSuite,
							Protocols:       []string{"TLSv1.2"},
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
		func() testCase {
			fake := faker.New()
			listenerName := fake.UUID().V4()
			listenerSpec := makeRandomListener(
				randomListenerWithHTTPSParamsOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			certName := fake.UUID().V4()
			cipherSuite := "oci-tls-12-13-ssl-cipher-suite-v3"
			verifyDepth := 1
			verifyPeerCertificate := false
			sslConfig := &loadbalancer.SslConfigurationDetails{
				CertificateName: &certName,
				CipherSuiteName: &cipherSuite,
				Protocols:       []string{"TLSv1.2", "TLSv1.3"},
			}

			return testCase{
				name: "ssl config ignores OCI listener default fields",
				params: makeOciListenerUpdateDetailsParams{
					existingListenerData: loadbalancer.Listener{
						Protocol:              new("HTTP"),
						Port:                  new(int(listenerSpec.Port)),
						DefaultBackendSetName: new(defaultBackendSetName),
						RoutingPolicyName:     new(listenerPolicyName(listenerName)),
						SslConfiguration: &loadbalancer.SslConfiguration{
							CertificateName:       &certName,
							CipherSuiteName:       &cipherSuite,
							Protocols:             []string{"TLSv1.2", "TLSv1.3"},
							ServerOrderPreference: loadbalancer.SslConfigurationServerOrderPreferenceEnabled,
							VerifyDepth:           &verifyDepth,
							VerifyPeerCertificate: &verifyPeerCertificate,
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

func TestListenerPolicyContainsGRPCRules(t *testing.T) {
	fake := faker.New()
	assert.False(t, listenerPolicyContainsGRPCRules(loadbalancer.RoutingPolicy{}))
	assert.False(t, listenerPolicyContainsGRPCRules(loadbalancer.RoutingPolicy{
		Rules: []loadbalancer.RoutingRule{{
			Condition: new("any(http.request.url.path sw '/')"),
		}},
	}))
	assert.True(t, listenerPolicyContainsGRPCRules(loadbalancer.RoutingPolicy{
		Name: new("policy-" + fake.Lorem().Word()),
		Rules: []loadbalancer.RoutingRule{{
			Condition: new("all(http.request.headers[(i 'content-type')][0] sw (i 'application/grpc+'))"),
		}},
	}))
}

func TestKnownFrontendMTLSCertificateNames(t *testing.T) {
	fake := faker.New()
	controllerCert := "default-gateway-tls-rev-" + fake.UUID().V4()
	frontendCert := controllerCert + "-fmtls-443-" + fake.RandomStringWithLength(8)
	externalCert := "external-gateway-tls-rev-" + fake.UUID().V4() + "-fmtls-443-" + fake.RandomStringWithLength(8)

	assert.Empty(t, knownFrontendMTLSCertificateNames(map[string]loadbalancer.Certificate{
		frontendCert: {CertificateName: &frontendCert},
	}, nil))
	assert.Equal(t, []string{frontendCert}, knownFrontendMTLSCertificateNames(
		map[string]loadbalancer.Certificate{
			frontendCert: {CertificateName: &frontendCert},
			externalCert: {CertificateName: &externalCert},
		},
		[]string{controllerCert},
	))
}

func Test_applyListenerTLSOptions(t *testing.T) {
	t.Run("does nothing without ssl config", func(_ *testing.T) {
		applyListenerTLSOptions(nil, &gatewayv1.ListenerTLSConfig{
			Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
				ListenerTLSOptionProtocols: "TLSv1.3",
			},
		})
	})

	t.Run("keeps OCI defaults when options are absent", func(t *testing.T) {
		certName := faker.New().UUID().V4()
		sslConfig := &loadbalancer.SslConfigurationDetails{CertificateName: &certName}

		applyListenerTLSOptions(sslConfig, nil)
		applyListenerTLSOptions(sslConfig, &gatewayv1.ListenerTLSConfig{})

		assert.Equal(t, certName, lo.FromPtr(sslConfig.CertificateName))
		assert.Empty(t, sslConfig.Protocols)
		assert.Nil(t, sslConfig.CipherSuiteName)
	})

	t.Run("applies cipher suite and TLS protocols", func(t *testing.T) {
		fake := faker.New()
		certName := fake.UUID().V4()
		cipherSuiteName := "oci-tls-12-13-ssl-cipher-suite-v3"
		sslConfig := &loadbalancer.SslConfigurationDetails{CertificateName: &certName}

		applyListenerTLSOptions(sslConfig, &gatewayv1.ListenerTLSConfig{
			Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
				ListenerTLSOptionCipherSuiteName: gatewayv1.AnnotationValue(" " + cipherSuiteName + " "),
				ListenerTLSOptionProtocols:       " TLSv1.2, TLSv1.3 ",
			},
		})

		assert.Equal(t, certName, lo.FromPtr(sslConfig.CertificateName))
		assert.Equal(t, cipherSuiteName, lo.FromPtr(sslConfig.CipherSuiteName))
		assert.Equal(t, []string{"TLSv1.2", "TLSv1.3"}, sslConfig.Protocols)
	})
}
