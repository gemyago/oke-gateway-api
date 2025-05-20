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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
				}).
				Return(false).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), setConditionParams{
					resource:      gateway,
					conditions:    &gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
					status:        metav1.ConditionTrue,
					reason:        string(gatewayv1.GatewayReasonAccepted),
					message:       fmt.Sprintf("Gateway %s accepted by %s", gateway.Name, ControllerClassName),
					annotations: map[string]string{
						ControllerClassName: "true",
					},
				}).
				Return(nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
					},
				}).
				Return(false).Once()

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(nil).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), setConditionParams{
					resource:      gateway,
					conditions:    &gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					status:        metav1.ConditionTrue,
					reason:        string(gatewayv1.GatewayConditionProgrammed),
					message:       fmt.Sprintf("Gateway %s programmed by %s", gateway.Name, ControllerClassName),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
					},
				}).
				Return(nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("handle regular accept errors", func(t *testing.T) {
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(false, wantErr).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("handle resource status accept errors", func(t *testing.T) {
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
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

		t.Run("handle condition update error when processing resource status accept errors", func(t *testing.T) {
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, wantErr).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), mock.Anything).
				Return(wantErr).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
				}).
				Return(true).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
					},
				}).
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
				}).
				Return(true).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
					},
				}).
				Return(false).Once()

			wantErr := &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionProgrammed),
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

		t.Run("handle set programmed condition error", func(t *testing.T) {
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
				}).
				Return(true).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
					},
				}).
				Return(false).Once()

			wantErr := errors.New(faker.Sentence())

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), mock.Anything).
				Return(nil).Once()

			mockResourcesModel.EXPECT().
				setCondition(t.Context(), mock.Anything).
				Return(wantErr).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("skip program when condition is already set", func(t *testing.T) {
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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionAccepted),
				}).
				Return(true).Once()

			mockResourcesModel.EXPECT().
				isConditionSet(isConditionSetParams{
					resource:      gateway,
					conditions:    gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
					},
				}).
				Return(true).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("ignore irrelevant requests", func(t *testing.T) {
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

			mockGatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), req, mock.Anything).
				Return(false, nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})
	})
}
