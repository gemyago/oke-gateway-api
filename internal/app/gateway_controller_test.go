package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/types"
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
	markGatewayAccepted := func(gateway *gatewayv1.Gateway) {
		gateway.Status.Conditions = append(gateway.Status.Conditions, metav1.Condition{
			Type:               string(gatewayv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.GatewayReasonAccepted),
			Message:            fmt.Sprintf("Gateway %s accepted by %s", gateway.Name, ControllerClassName),
			ObservedGeneration: gateway.Generation,
			LastTransitionTime: metav1.Now(),
		})
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

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(false).Once()

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(nil).Once()

			mockGatewayModel.EXPECT().
				setProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("handle regular accept errors", func(t *testing.T) {
			fake := faker.New()
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

			wantErr := errors.New(fake.Lorem().Sentence(10))
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
			fake := faker.New()
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
				reason:        fake.Lorem().Word(),
				message:       fake.Lorem().Sentence(10),
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
			fake := faker.New()
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
				reason:        fake.Lorem().Word(),
				message:       fake.Lorem().Sentence(10),
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
			fake := faker.New()
			gateway := newRandomGateway()
			markGatewayAccepted(gateway)

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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(false).Once()

			wantErr := errors.New(fake.Lorem().Sentence(10))

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
			fake := faker.New()
			gateway := newRandomGateway()
			markGatewayAccepted(gateway)

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

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(false).Once()

			wantErr := &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionProgrammed),
				reason:        fake.Lorem().Word(),
				message:       fake.Lorem().Sentence(10),
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

		t.Run("returns drift requeue for program resourceStatusError when drift is enabled", func(t *testing.T) {
			fake := faker.New()
			gateway := newRandomGateway()
			markGatewayAccepted(gateway)
			driftInterval := 23 * time.Minute

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			deps.DriftInterval = driftInterval
			controller := NewGatewayController(deps)

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			mockGatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(false).Once()

			wantErr := &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionProgrammed),
				reason:        fake.Lorem().Word(),
				message:       fake.Lorem().Sentence(10),
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
			assertDriftRequeue(t, result, driftInterval)
		})

		t.Run("handle set programmed condition error", func(t *testing.T) {
			fake := faker.New()
			gateway := newRandomGateway()
			markGatewayAccepted(gateway)

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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(false).Once()

			wantErr := errors.New(fake.Lorem().Sentence(10))

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), mock.Anything).
				Return(nil).Once()

			mockGatewayModel.EXPECT().
				setProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(wantErr).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("skip program when condition is already set", func(t *testing.T) {
			gateway := newRandomGateway()
			markGatewayAccepted(gateway)

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
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(true).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("programs already programmed gateway when drift interval is enabled", func(t *testing.T) {
			gateway := newRandomGateway()
			markGatewayAccepted(gateway)
			driftInterval := 7 * time.Minute

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			deps := newMockDeps(t)
			deps.DriftInterval = driftInterval
			controller := NewGatewayController(deps)

			mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			mockGatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), req, mock.MatchedBy(func(receiver *resolvedGatewayDetails) bool {
					receiver.gateway = *gateway
					return true
				})).
				Return(true, nil).Once()

			mockGatewayModel.EXPECT().
				isProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(true).Once()

			mockGatewayModel.EXPECT().
				programGateway(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(nil).Once()

			mockGatewayModel.EXPECT().
				setProgrammed(t.Context(), &resolvedGatewayDetails{
					gateway: *gateway,
				}).
				Return(nil).Once()

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assertDriftRequeue(t, result, driftInterval)
		})

		t.Run("clears accepted false after previously missing TLS secret exists", func(t *testing.T) {
			fakeData := faker.New()
			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, gatewayv1.Install(scheme))
			require.NoError(t, types.AddKnownTypes(scheme))

			secretName := "tls-" + fakeData.Internet().Slug()
			configName := "config-" + fakeData.Internet().Slug()
			loadBalancerID := fakeData.UUID().V4()
			secretUID := apitypes.UID(fakeData.UUID().V4())
			secretResourceVersion := fakeData.UUID().V4()

			gateway := newRandomGateway(
				randomGatewayWithListenersOpt(gatewayv1.Listener{
					Name:     "https",
					Port:     443,
					Protocol: gatewayv1.HTTPSProtocolType,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{
							{Name: gatewayv1.ObjectName(secretName)},
						},
					},
				}),
			)
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  configName,
				},
			}
			gateway.Status.Conditions = []metav1.Condition{
				{
					Type:               string(gatewayv1.GatewayConditionAccepted),
					Status:             metav1.ConditionFalse,
					Reason:             string(gatewayv1.GatewayReasonInvalidParameters),
					Message:            fmt.Sprintf("referenced secret %s/%s not found", gateway.Namespace, secretName),
					ObservedGeneration: gateway.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               string(gatewayv1.GatewayConditionProgrammed),
					Status:             metav1.ConditionTrue,
					Reason:             string(gatewayv1.GatewayReasonProgrammed),
					Message:            fmt.Sprintf("Gateway %s programmed by %s", gateway.Name, ControllerClassName),
					ObservedGeneration: gateway.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			gateway.Annotations = map[string]string{
				GatewayProgrammingRevisionAnnotation:                         GatewayProgrammingRevisionValue,
				GatewayUsedSecretsAnnotationPrefix + "/" + string(secretUID): secretResourceVersion,
				GatewayProgrammedCertificatesAnnotation: fmt.Sprintf(
					"%s-%s-rev-%s",
					gateway.Namespace,
					secretName,
					secretResourceVersion,
				),
			}

			gatewayClass := newRandomGatewayClass(
				randomGatewayClassWithControllerNameOpt(ControllerClassName),
			)
			gateway.Spec.GatewayClassName = gatewayv1.ObjectName(gatewayClass.Name)

			gatewayConfig := &types.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: gateway.Namespace,
				},
				Spec: types.GatewayConfigSpec{
					LoadBalancerID: loadBalancerID,
				},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            secretName,
					Namespace:       gateway.Namespace,
					UID:             secretUID,
					ResourceVersion: secretResourceVersion,
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					corev1.TLSCertKey:       []byte(fakeData.Lorem().Sentence(10)),
					corev1.TLSPrivateKeyKey: []byte(fakeData.Lorem().Sentence(10)),
				},
			}

			k8sClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&gatewayv1.Gateway{}).
				WithObjects(gateway, gatewayClass, gatewayConfig, secret).
				Build()

			resourcesModel := newResourcesModel(resourcesModelDeps{
				K8sClient:  k8sClient,
				RootLogger: diag.RootTestLogger(),
			})
			gatewayModel := newGatewayModel(gatewayModelDeps{
				K8sClient:            k8sClient,
				ResourcesModel:       resourcesModel,
				RootLogger:           diag.RootTestLogger(),
				OciClient:            NewMockociLoadBalancerClient(t),
				OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
			})
			controller := NewGatewayController(GatewayControllerDeps{
				K8sClient:      k8sClient,
				ResourcesModel: resourcesModel,
				GatewayModel:   gatewayModel,
				RootLogger:     diag.RootTestLogger(),
			})

			result, err := controller.Reconcile(t.Context(), reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(gateway),
			})

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)

			var updatedGateway gatewayv1.Gateway
			require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(gateway), &updatedGateway))
			accepted := meta.FindStatusCondition(
				updatedGateway.Status.Conditions,
				string(gatewayv1.GatewayConditionAccepted),
			)
			require.NotNil(t, accepted)
			assert.Equal(t, metav1.ConditionTrue, accepted.Status)
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
