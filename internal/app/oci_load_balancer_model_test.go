package app

import (
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

	t.Run("reconcileDefaultBackendSet", func(t *testing.T) {
		t.Run("when backend set exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			existingBackendSet := makeRandomOCIBackendSet()
			gw := newRandomGateway()

			wantBsName := gw.Name + "-default"

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
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
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
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
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
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
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
						l.TLS = &gatewayv1.GatewayTLSConfig{
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

			routingPolicyName := string(gwListener.Name) + "_policy"

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
					routingPolicyName: makeRandomOCIRoutingPolicy(),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
					fake.UUID().V4():        makeRandomOCIListener(),
				},
				defaultBackendSetName: fake.UUID().V4(),
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
		t.Run("when listener exists no changes", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)
			defaultBackendSetName := fake.UUID().V4()
			routingPolicyName := string(gwListener.Name) + "_policy"
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
					routingPolicyName: makeRandomOCIRoutingPolicy(),
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

			routingPolicyName := string(gwListener.Name) + "_policy"

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
					routingPolicyName: makeRandomOCIRoutingPolicy(),
				},
				knownListeners: map[string]loadbalancer.Listener{
					string(gwListener.Name): lbListener,
					fake.UUID().V4():        makeRandomOCIListener(),
				},
				listenerCertificates:  listenerCertificates,
				defaultBackendSetName: fake.UUID().V4(),
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
			routingPolicyName := string(gwListener.Name) + "_policy"
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
			routingPolicyName := string(gwListener.Name) + "_policy"
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

		t.Run("when routing policy exists", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			gwListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
			)

			routingPolicyName := string(gwListener.Name) + "_policy"

			params := reconcileHTTPListenerParams{
				loadBalancerID: fake.UUID().V4(),
				knownListeners: map[string]loadbalancer.Listener{
					fake.UUID().V4(): makeRandomOCIListener(),
					fake.UUID().V4(): makeRandomOCIListener(),
				},
				knownRoutingPolicies: map[string]loadbalancer.RoutingPolicy{
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
					routingPolicyName: makeRandomOCIRoutingPolicy(),
					fake.UUID().V4():  makeRandomOCIRoutingPolicy(),
				},
				defaultBackendSetName: fake.UUID().V4(),
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
			routingRulesMapper.EXPECT().mapHTTPRouteMatchesToCondition(
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
			routingRulesMapper.EXPECT().mapHTTPRouteMatchesToCondition(
				httpRoute.Spec.Rules[ruleIndex].Matches,
			).Return("", expectedErr).Once()

			_, err := model.makeRoutingRule(t.Context(), params)
			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
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
				loadBalancerID:       fake.UUID().V4(),
				listenerCertificates: listenerCertificates,
				knownCertificates:    knownCertificates,
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
			unusedCert1 := makeRandomOCICertificate()
			unusedCert2 := makeRandomOCICertificate()

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
				loadBalancerID:       fake.UUID().V4(),
				listenerCertificates: listenerCertificates,
				knownCertificates:    knownCertificates,
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

		t.Run("continues deletion even if one fails", func(t *testing.T) {
			fake := faker.New()
			deps := makeMockDeps(t)
			model := newOciLoadBalancerModel(deps)
			ociLoadBalancerClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			workRequestsWatcher, _ := deps.WorkRequestsWatcher.(*MockworkRequestsWatcher)

			// Create certificates, some used and some unused
			usedCert := makeRandomOCICertificate()
			unusedCert1 := makeRandomOCICertificate() // This one will fail
			unusedCert2 := makeRandomOCICertificate() // This one will succeed

			knownCertificates := map[string]loadbalancer.Certificate{
				*usedCert.CertificateName:    usedCert,
				*unusedCert1.CertificateName: unusedCert1,
				*unusedCert2.CertificateName: unusedCert2,
			}

			listenerCertificates := map[string][]loadbalancer.Certificate{
				"listener1": {usedCert},
			}

			params := removeUnusedCertificatesParams{
				loadBalancerID:       fake.UUID().V4(),
				listenerCertificates: listenerCertificates,
				knownCertificates:    knownCertificates,
			}

			// First certificate deletion fails
			wantErr := errors.New(fake.Lorem().Sentence(10))
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert1.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{}, wantErr).Once()

			// Second certificate deletion succeeds
			workRequestID := fake.UUID().V4()
			ociLoadBalancerClient.EXPECT().DeleteCertificate(t.Context(), loadbalancer.DeleteCertificateRequest{
				LoadBalancerId:  &params.loadBalancerID,
				CertificateName: unusedCert2.CertificateName,
			}).Return(loadbalancer.DeleteCertificateResponse{
				OpcWorkRequestId: &workRequestID,
			}, nil).Once()
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
			unusedCert := makeRandomOCICertificate()

			knownCertificates := map[string]loadbalancer.Certificate{
				*usedCert.CertificateName:   usedCert,
				*unusedCert.CertificateName: unusedCert,
			}

			listenerCertificates := map[string][]loadbalancer.Certificate{
				"listener1": {usedCert},
			}

			params := removeUnusedCertificatesParams{
				loadBalancerID:       fake.UUID().V4(),
				listenerCertificates: listenerCertificates,
				knownCertificates:    knownCertificates,
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
