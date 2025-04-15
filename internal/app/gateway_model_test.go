package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) gatewayModelDeps {
		return gatewayModelDeps{
			K8sClient:  NewMockk8sClient(t),
			RootLogger: diag.RootTestLogger(),
		}
	}

	t.Run("acceptReconcileRequest", func(t *testing.T) {
		t.Run("valid gateway", func(t *testing.T) {
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
				RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			wantConfigName := apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
			}
			mockClient.EXPECT().
				Get(t.Context(), wantConfigName, mock.Anything).
				RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(gatewayConfig))
					return nil
				})

			var receiver gatewayData
			relevant, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)

			assert.Equal(t, gatewayConfig, receiver.config)
			assert.Equal(t, *gateway, receiver.gateway)
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
				RunAndReturn(func(_ context.Context, nn apitypes.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
					assert.Equal(t, req.NamespacedName, nn)
					reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
					return nil
				})

			var receiver gatewayData
			_, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr, "Error should be a resourceStatusError")

			assert.Equal(t, AcceptedConditionReason, statusErr.conditionType)
			assert.Equal(t, MissingConfigReason, statusErr.reason)
			assert.Equal(t, "spec.infrastructure is missing parametersRef", statusErr.message)
			assert.NoError(t, statusErr.cause)
		})
	})

	t.Run("programGateway", func(t *testing.T) {
		t.Run("Stub", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()

			err := model.programGateway(t.Context(), &gatewayData{
				gateway: *gateway,
			})

			require.NoError(t, err)
		})
	})
}
