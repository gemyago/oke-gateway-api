package app

import (
	"context"
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

			gatewayClass := newRandomGatewayClass()

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

			var receiver acceptedGatewayDetails
			relevant, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)

			assert.Equal(t, gatewayConfig, receiver.config)
			assert.Equal(t, *gateway, receiver.gateway)
			assert.Equal(t, *gatewayClass, receiver.gatewayClass)
		})

		t.Run("missingConfigRef", func(t *testing.T) {
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
					receiver client.Object,
					_ ...client.GetOption,
				) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*newRandomGatewayClass()))
					return nil
				})

			var receiver acceptedGatewayDetails
			_, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr, "Error should be a resourceStatusError")

			assert.Equal(t, string(gatewayv1.GatewayConditionAccepted), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
			assert.Equal(t, "spec.infrastructure is missing parametersRef", statusErr.message)
			assert.NoError(t, statusErr.cause)
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

			var receiver acceptedGatewayDetails
			accepted, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
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

			var receiver acceptedGatewayDetails
			accepted, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.False(t, accepted)
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
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)

			defaultBackendSet := makeRandomOCIBackendSet()

			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), reconcileDefaultBackendParams{
					loadBalancerID:   config.Spec.LoadBalancerID,
					knownBackendSets: loadBalancer.BackendSets,
					gateway:          gateway,
				}).
				Return(defaultBackendSet, nil)

			ociListeners := make([]loadbalancer.Listener, len(gateway.Spec.Listeners))
			for i := range gateway.Spec.Listeners {
				ociListeners[i] = makeRandomOCIListener()
			}

			for i, listener := range gateway.Spec.Listeners {
				loadBalancerModel.EXPECT().
					reconcileHTTPListener(t.Context(), reconcileHTTPListenerParams{
						loadBalancerID:        config.Spec.LoadBalancerID,
						defaultBackendSetName: *defaultBackendSet.Name,
						knownListeners:        loadBalancer.Listeners,
						listenerSpec:          &listener,
					}).
					Return(ociListeners[i], nil)
			}
			err := model.programGateway(t.Context(), &acceptedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.NoError(t, err)
		})
	})
}
