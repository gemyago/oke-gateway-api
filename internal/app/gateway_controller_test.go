package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayController(t *testing.T) {
	newMockDeps := func(t *testing.T) GatewayControllerDeps {
		return GatewayControllerDeps{
			K8sClient:      NewMockk8sClient(t),
			ResourcesModel: NewMockresourcesModel(t),
			GatewayModel:   NewMockgatewayModel(t),
			RootLogger:     diag.RootTestLogger(),
		}
	}

	t.Run("Reconcile", func(t *testing.T) {
		t.Run("acceptAndProgram", func(t *testing.T) {
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			controller := NewGatewayController(deps)

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			// Mock Get
			mockGatewayModel.EXPECT().
				acceptReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *gatewayData) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(gateway, gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).
				Return(false).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), setConditionParams{
					resource:      gateway,
					conditions:    &gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
					status:        metav1.ConditionTrue,
					reason:        string(gatewayv1.GatewayReasonAccepted),
					message:       fmt.Sprintf("Gateway %s accepted by %s", gateway.Name, ControllerClassName),
				}).
				Return(nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(gateway, gateway.Status.Conditions, ProgrammedGatewayConditionType).
				Return(false).Once()

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), &gatewayData{
					gateway: *gateway,
				}).
				Return(nil).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), setConditionParams{
					resource:      gateway,
					conditions:    &gateway.Status.Conditions,
					conditionType: ProgrammedGatewayConditionType,
					status:        metav1.ConditionTrue,
					reason:        LoadBalancerReconciledReason,
					message:       fmt.Sprintf("Gateway %s programmed by %s", gateway.Name, ControllerClassName),
				}).
				Return(nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("handle accept errors", func(t *testing.T) {
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			controller := NewGatewayController(deps)

			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			wantErr := errors.New(faker.Sentence())
			mockGatewayModel.EXPECT().
				acceptReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *gatewayData) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(false, wantErr).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("handle accept resource errors", func(t *testing.T) {
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			controller := NewGatewayController(deps)

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			wantErr := &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        faker.Word(),
				message:       faker.Sentence(),
			}

			mockGatewayModel.EXPECT().
				acceptReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *gatewayData) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, wantErr).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), setConditionParams{
					resource:      gateway,
					conditions:    &gateway.Status.Conditions,
					conditionType: wantErr.conditionType,
					status:        metav1.ConditionFalse,
					reason:        wantErr.reason,
					message:       wantErr.message,
				}).
				Return(nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("handle porgramGateway errors", func(t *testing.T) {
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			controller := NewGatewayController(deps)

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			mockGatewayModel.EXPECT().
				acceptReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *gatewayData) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(gateway, gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).
				Return(true).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(gateway, gateway.Status.Conditions, ProgrammedGatewayConditionType).
				Return(false).Once()

			wantErr := errors.New(faker.Sentence())

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), mock.Anything).
				Return(wantErr).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		// if error is resourceStatusError then set status to details from the error
		t.Run("handle program resourceStatusError", func(t *testing.T) {
			gateway := newRandomGateway()

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			controller := NewGatewayController(deps)

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			mockGatewayModel.EXPECT().
				acceptReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *gatewayData) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(gateway, gateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)).
				Return(true).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(gateway, gateway.Status.Conditions, ProgrammedGatewayConditionType).
				Return(false).Once()

			wantErr := &resourceStatusError{
				conditionType: ProgrammedGatewayConditionType,
				reason:        faker.Word(),
				message:       faker.Sentence(),
			}

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), mock.Anything).
				Return(wantErr).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), setConditionParams{
					resource:      gateway,
					conditions:    &gateway.Status.Conditions,
					conditionType: wantErr.conditionType,
					status:        metav1.ConditionFalse,
					reason:        wantErr.reason,
					message:       wantErr.message,
				}).
				Return(nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})
	})
}
