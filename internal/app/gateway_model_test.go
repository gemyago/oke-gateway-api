package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) gatewayModelDeps {
		return gatewayModelDeps{
			K8sClient:            NewMockk8sClient(t),
			RootLogger:           diag.RootTestLogger(),
			OciClient:            NewMockociLoadBalancerClient(t),
			OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
		}
	}

	t.Run("acceptReconcileRequest", func(t *testing.T) {
		t.Run("valid gateway", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gatewayClass := newRandomGatewayClass(
				randomGatewayClassWithControllerNameOpt(
					ControllerClassName,
				),
			)

			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  faker.DomainName(),
				},
			}
			gatewayConfig := types.GatewayConfig{
				Spec: types.GatewayConfigSpec{
					LoadBalancerID: faker.UUIDHyphenated(),
				},
			}
			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
					return nil
				})

			wantConfigName := apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
			}
			mockClient.EXPECT().
				Get(t.Context(), wantConfigName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(gatewayConfig))
					return nil
				})

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)

			assert.Equal(t, gatewayConfig, receiver.config)
			assert.Equal(t, *gateway, receiver.gateway)
			assert.Equal(t, *gatewayClass, receiver.gatewayClass)
		})

		t.Run("missingGateway", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					_ client.Object,
					_ ...client.GetOption,
				) error {
					return apierrors.NewNotFound(schema.GroupResource{
						Group:    gatewayv1.GroupName,
						Resource: "Gateway",
					}, gateway.Name)
				})

			var receiver resolvedGatewayDetails
			accepted, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.False(t, accepted)
		})

		t.Run("handle get gateway error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			wantErr := errors.New(faker.Sentence())
			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					_ client.Object,
					_ ...client.GetOption,
				) error {
					return wantErr
				})

			var receiver resolvedGatewayDetails
			accepted, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.False(t, accepted)
		})

		t.Run("missingGatewayClass", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					_ client.Object,
					_ ...client.GetOption,
				) error {
					return apierrors.NewNotFound(schema.GroupResource{
						Group:    gatewayv1.GroupName,
						Resource: "GatewayClass",
					}, string(gateway.Spec.GatewayClassName))
				})

			var receiver resolvedGatewayDetails
			accepted, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.False(t, accepted)
		})

		t.Run("handle get gatewayClass error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			wantErr := errors.New(faker.Sentence())
			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					_ client.Object,
					_ ...client.GetOption,
				) error {
					return wantErr
				})

			var receiver resolvedGatewayDetails
			accepted, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.False(t, accepted)
		})

		t.Run("irrelevantGatewayClass", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  faker.DomainName(),
				},
			}

			gatewayClass := newRandomGatewayClass()
			gatewayClass.Spec.ControllerName = gatewayv1.GatewayController(faker.DomainName())

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, string(gateway.Spec.GatewayClassName), nn.Name)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
					return nil
				})

			var receiver resolvedGatewayDetails
			accepted, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.False(t, accepted)
		})

		t.Run("missing parametersRef definition", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = nil

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*newRandomGatewayClass(
						randomGatewayClassWithControllerNameOpt(
							ControllerClassName,
						),
					)))
					return nil
				})

			var receiver resolvedGatewayDetails
			resolved, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			assert.False(t, resolved)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr, "Error should be a resourceStatusError")

			assert.Equal(t, string(gatewayv1.GatewayConditionAccepted), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
			assert.Equal(t, "spec.infrastructure is missing parametersRef", statusErr.message)
			assert.NoError(t, statusErr.cause)
		})

		t.Run("not existing GatewayConfig", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  faker.DomainName(),
				},
			}

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*newRandomGatewayClass(
						randomGatewayClassWithControllerNameOpt(
							ControllerClassName,
						),
					)))
					return nil
				})

			wantConfigName := apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
			}
			mockClient.EXPECT().
				Get(t.Context(), wantConfigName, mock.Anything).
				Return(apierrors.NewNotFound(schema.GroupResource{
					Group:    gatewayv1.GroupName,
					Resource: "GatewayConfig",
				}, wantConfigName.Name))

			var receiver resolvedGatewayDetails
			_, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr, "Error should be a resourceStatusError")

			assert.Equal(t, string(gatewayv1.GatewayConditionAccepted), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
			assert.Equal(t, "spec.infrastructure is pointing to a non-existent GatewayConfig", statusErr.message)
			assert.NoError(t, statusErr.cause)
		})

		t.Run("error getting GatewayConfig", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  faker.DomainName(),
				},
			}

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)

			mockClient.EXPECT().
				Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					nn apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name: string(gateway.Spec.GatewayClassName),
				}, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ apitypes.NamespacedName,
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*newRandomGatewayClass(
						randomGatewayClassWithControllerNameOpt(
							ControllerClassName,
						),
					)))
					return nil
				})

			wantErr := errors.New(faker.Sentence())
			wantConfigName := apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
			}
			mockClient.EXPECT().
				Get(t.Context(), wantConfigName, mock.Anything).
				Return(wantErr)

			var receiver resolvedGatewayDetails
			resolved, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.False(t, resolved)
		})
	})

	t.Run("programGateway", func(t *testing.T) {
		t.Run("programSucceeded", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			gateway := newRandomGateway(
				randomGatewayWithRandomListenersOpt(),
			)
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)

			knownCertificates := map[string]loadbalancer.Certificate{}
			certificatesByListener := map[string][]loadbalancer.Certificate{}

			loadBalancer.Listeners = make(map[string]loadbalancer.Listener)
			for _, listener := range gateway.Spec.Listeners {
				loadBalancer.Listeners[string(listener.Name)] = makeRandomOCIListener()
				cert := makeRandomOCICertificate()
				knownCertificates[*cert.CertificateName] = cert
				certificatesByListener[string(listener.Name)] = []loadbalancer.Certificate{cert}
			}

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadBalancer,
				}, nil)
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)

			defaultBackendSet := makeRandomOCIBackendSet()

			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), reconcileDefaultBackendParams{
					loadBalancerID:   config.Spec.LoadBalancerID,
					knownBackendSets: loadBalancer.BackendSets,
					gateway:          gateway,
				}).
				Return(defaultBackendSet, nil)

			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
					loadBalancerID:    config.Spec.LoadBalancerID,
					gateway:           gateway,
					knownCertificates: loadBalancer.Certificates,
				}).
				Return(reconcileListenersCertificatesResult{
					reconciledCertificates: knownCertificates,
					certificatesByListener: certificatesByListener,
				}, nil).
				Once()

			for _, listener := range gateway.Spec.Listeners {
				loadBalancerModel.EXPECT().
					reconcileHTTPListener(t.Context(), reconcileHTTPListenerParams{
						loadBalancerID:        config.Spec.LoadBalancerID,
						defaultBackendSetName: *defaultBackendSet.Name,
						knownListeners:        loadBalancer.Listeners,
						knownRoutingPolicies:  loadBalancer.RoutingPolicies,
						listenerCertificates:  certificatesByListener[string(listener.Name)],
						listenerSpec:          &listener,
					}).
					Return(nil).
					NotBefore(reconcileCertificatesCall)
			}

			loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), removeMissingListenersParams{
					loadBalancerID:   config.Spec.LoadBalancerID,
					knownListeners:   loadBalancer.Listeners,
					gatewayListeners: gateway.Spec.Listeners,
				}).
				Return(nil)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.NoError(t, err)
		})
		t.Run("failed to get OCI Load Balancer", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			gateway := newRandomGateway(
				randomGatewayWithRandomListenersOpt(),
			)
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
			)
			loadBalancer.Listeners = make(map[string]loadbalancer.Listener)
			for _, listener := range gateway.Spec.Listeners {
				loadBalancer.Listeners[string(listener.Name)] = makeRandomOCIListener()
			}

			wantErr := errors.New(faker.Sentence())
			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{}, wantErr)
			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
		})
		t.Run("failed to reconcile default backend set", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			gateway := newRandomGateway(
				randomGatewayWithRandomListenersOpt(),
			)
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
			)
			loadBalancer.Listeners = make(map[string]loadbalancer.Listener)
			for _, listener := range gateway.Spec.Listeners {
				loadBalancer.Listeners[string(listener.Name)] = makeRandomOCIListener()
			}

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadBalancer,
				}, nil)

			wantErr := errors.New(faker.Sentence())
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(loadbalancer.BackendSet{}, wantErr)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
		})
		t.Run("failed to reconcile listener", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			gateway := newRandomGateway(
				randomGatewayWithRandomListenersOpt(),
			)
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			loadBalancer.Listeners = make(map[string]loadbalancer.Listener)
			for _, listener := range gateway.Spec.Listeners {
				loadBalancer.Listeners[string(listener.Name)] = makeRandomOCIListener()
			}
			defaultBackendSet := makeRandomOCIBackendSet()

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadBalancer,
				}, nil)

			wantKnownCertificates := makeFewRandomOCICertificatesMap()
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
					loadBalancerID:    config.Spec.LoadBalancerID,
					gateway:           gateway,
					knownCertificates: loadBalancer.Certificates,
				}).
				Return(reconcileListenersCertificatesResult{
					reconciledCertificates: wantKnownCertificates,
				}, nil).
				Once()

			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)

			wantErr := errors.New(faker.Sentence())
			loadBalancerModel.EXPECT().
				reconcileHTTPListener(t.Context(), mock.Anything).
				Return(wantErr).
				NotBefore(reconcileCertificatesCall)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
		})
	})
}
