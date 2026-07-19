package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

func TestGatewayModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) gatewayModelDeps {
		return gatewayModelDeps{
			ResourcesModel:       NewMockresourcesModel(t),
			K8sClient:            NewMockk8sClient(t),
			RootLogger:           diag.RootTestLogger(),
			OciClient:            NewMockociLoadBalancerClient(t),
			OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
		}
	}

	t.Run("resolveReconcileRequest", func(t *testing.T) {
		t.Run("valid gateway", func(t *testing.T) {
			fake := faker.New()
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
					Name:  fake.Internet().Domain(),
				},
			}
			gatewayConfig := types.GatewayConfig{
				Spec: types.GatewayConfigSpec{
					LoadBalancerID: fake.UUID().V4(),
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

		t.Run("valid gateway with ListenerSet listeners", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			model.setListenerSetEnabled(true)

			gatewayClass := newRandomGatewayClass(
				randomGatewayClassWithControllerNameOpt(ControllerClassName),
			)
			gateway := newRandomGateway()
			gateway.Namespace = "infra-" + fake.Lorem().Word()
			gateway.Name = "edge-" + fake.Lorem().Word()
			fromAll := gatewayv1.NamespacesFromAll
			gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
				Namespaces: &gatewayv1.ListenerNamespaces{From: &fromAll},
			}
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  "config-" + fake.Lorem().Word(),
				},
			}
			gatewayConfig := types.GatewayConfig{
				Spec: types.GatewayConfigSpec{LoadBalancerID: fake.UUID().V4()},
			}
			parentNamespace := gatewayv1.Namespace(gateway.Namespace)
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + fake.Lorem().Word(),
					Name:      "extra-" + fake.Lorem().Word(),
				},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{
						Namespace: &parentNamespace,
						Name:      gatewayv1.ObjectName(gateway.Name),
					},
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     "https",
						Port:     443,
						Protocol: gatewayv1.HTTPSProtocolType,
						TLS: &gatewayv1.ListenerTLSConfig{
							CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-cert"}},
						},
					}},
				},
			}
			secret := makeRandomSecret(
				randomSecretWithNameOpt("tls-cert"),
				randomSecretWithTLSDataOpt(),
			)
			secret.Namespace = listenerSet.Namespace
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gateway)}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			setupClientGet(t, mockClient, req.NamespacedName, *gateway)
			setupClientGet(t, mockClient, apitypes.NamespacedName{
				Name: string(gateway.Spec.GatewayClassName),
			}, *gatewayClass)
			setupClientGet(t, mockClient, apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
			}, gatewayConfig)
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{},
					client.MatchingFields{listenerSetParentGatewayIndexKey: req.NamespacedName.String()}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
						listenerSet,
						{
							ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "ignored"},
							Spec: gatewayv1.ListenerSetSpec{ParentRef: gatewayv1.ParentGatewayReference{
								Name: "other",
							}},
						},
					}))
					return nil
				})
			setupClientGet(t, mockClient, apitypes.NamespacedName{Name: listenerSet.Namespace}, corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: listenerSet.Namespace},
			})
			setupClientGet(t, mockClient, apitypes.NamespacedName{
				Namespace: secret.Namespace,
				Name:      secret.Name,
			}, secret)

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)
			require.Len(t, receiver.listenerSets, 1)
			require.Len(t, receiver.effectiveListeners, 1+len(gateway.Spec.Listeners))
			assert.Contains(t, receiver.gatewaySecrets, secret.Namespace+"/"+secret.Name)
		})

		t.Run("returns invalid certificate option errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gatewayClass := newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(ControllerClassName))
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{{
				Name:     "https",
				Protocol: gatewayv1.HTTPSProtocolType,
				TLS: &gatewayv1.ListenerTLSConfig{
					CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-cert"}},
					Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
						ListenerTLSOptionOCICertificateOCID: "ocid1.certificate.oc1..test",
					},
				},
			}}
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
			}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gateway)}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			setupClientGet(t, mockClient, req.NamespacedName, *gateway)
			setupClientGet(
				t,
				mockClient,
				apitypes.NamespacedName{Name: string(gateway.Spec.GatewayClassName)},
				*gatewayClass,
			)
			setupClientGet(t, mockClient, apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      "alb-config",
			}, makeRandomGatewayConfig())

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			assert.False(t, relevant)
			require.ErrorContains(t, err, "cannot be used together with listener.tls.certificateRefs")
		})

		t.Run("returns ListenerSet population errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			model.setListenerSetEnabled(true)
			gatewayClass := newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(ControllerClassName))
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{Name: "alb-config"},
			}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(gateway)}
			wantErr := errors.New("listenerset list failed")

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			setupClientGet(t, mockClient, req.NamespacedName, *gateway)
			setupClientGet(
				t,
				mockClient,
				apitypes.NamespacedName{Name: string(gateway.Spec.GatewayClassName)},
				*gatewayClass,
			)
			setupClientGet(t, mockClient, apitypes.NamespacedName{
				Namespace: gateway.Namespace,
				Name:      "alb-config",
			}, makeRandomGatewayConfig())
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{},
					client.MatchingFields{listenerSetParentGatewayIndexKey: req.NamespacedName.String()}).
				Return(wantErr)

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			assert.False(t, relevant)
			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to list ListenerSets")
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
			fake := faker.New()
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

			wantErr := errors.New(fake.Lorem().Sentence(10))
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
			fake := faker.New()
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

			wantErr := errors.New(fake.Lorem().Sentence(10))
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
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  fake.Internet().Domain(),
				},
			}

			gatewayClass := newRandomGatewayClass()
			gatewayClass.Spec.ControllerName = gatewayv1.GatewayController(fake.Internet().Domain())

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
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  fake.Internet().Domain(),
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
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  fake.Internet().Domain(),
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

			wantErr := errors.New(fake.Lorem().Sentence(10))
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

		t.Run("gatewaySecretsPopulated", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			// Create gateway with HTTPS listeners that reference secrets
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  fake.Internet().Domain(),
				},
			}

			// Create two listeners with TLS configurations
			listener1 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			listener2 := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway.Spec.Listeners = []gatewayv1.Listener{listener1, listener2}

			// Generate secrets corresponding to certificate references
			secretsMap := make(map[string]corev1.Secret)

			for _, listener := range gateway.Spec.Listeners {
				if listener.TLS != nil {
					for _, certRef := range listener.TLS.CertificateRefs {
						secretName := string(certRef.Name)
						secretNamespace := gateway.Namespace
						if certRef.Namespace != nil {
							secretNamespace = string(*certRef.Namespace)
						}

						fullName := secretNamespace + "/" + secretName
						secret := makeRandomSecret(
							randomSecretWithNameOpt(secretName),
							randomSecretWithTLSDataOpt(),
						)
						// Override namespace since makeRandomSecret generates random one
						secret.Namespace = secretNamespace
						secretsMap[fullName] = secret
					}
				}
			}

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			gatewayClass := newRandomGatewayClass(
				randomGatewayClassWithControllerNameOpt(
					ControllerClassName,
				),
			)

			gatewayConfig := types.GatewayConfig{
				Spec: types.GatewayConfigSpec{
					LoadBalancerID: fake.UUID().V4(),
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

			// Expect calls to get secrets
			for _, listener := range gateway.Spec.Listeners {
				if listener.TLS != nil {
					for _, certRef := range listener.TLS.CertificateRefs {
						secretName := string(certRef.Name)
						secretNamespace := gateway.Namespace
						if certRef.Namespace != nil {
							secretNamespace = string(*certRef.Namespace)
						}

						fullName := secretNamespace + "/" + secretName
						secretObj := secretsMap[fullName]

						mockClient.EXPECT().
							Get(t.Context(), apitypes.NamespacedName{
								Name:      secretName,
								Namespace: secretNamespace,
							}, mock.Anything).
							RunAndReturn(func(
								_ context.Context,
								_ apitypes.NamespacedName,
								receiver client.Object,
								_ ...client.GetOption,
							) error {
								reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(secretObj))
								return nil
							})
					}
				}
			}

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)

			// Verify that gatewaySecrets map is populated with all the certificate secrets
			require.NotNil(t, receiver.gatewaySecrets)
			for _, listener := range gateway.Spec.Listeners {
				if listener.TLS != nil {
					for _, certRef := range listener.TLS.CertificateRefs {
						secretName := string(certRef.Name)
						secretNamespace := gateway.Namespace
						if certRef.Namespace != nil {
							secretNamespace = string(*certRef.Namespace)
						}

						fullName := secretNamespace + "/" + secretName
						secret, exists := receiver.gatewaySecrets[fullName]
						assert.True(t, exists, "Secret %s should exist in gatewaySecrets", fullName)
						assert.Equal(t, secretName, secret.Name)
						assert.Equal(t, secretNamespace, secret.Namespace)
					}
				}
			}
		})

		t.Run("missingGatewaySecret", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			// Create gateway with HTTPS listener that references a secret
			gateway := newRandomGateway()
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  fake.Internet().Domain(),
				},
			}

			// Create a listener with TLS configuration
			listener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway.Spec.Listeners = []gatewayv1.Listener{listener}

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: gateway.Namespace,
					Name:      gateway.Name,
				},
			}

			gatewayClass := newRandomGatewayClass(
				randomGatewayClassWithControllerNameOpt(
					ControllerClassName,
				),
			)

			gatewayConfig := types.GatewayConfig{
				Spec: types.GatewayConfigSpec{
					LoadBalancerID: fake.UUID().V4(),
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

			// Make one of the secret fetches fail with NotFound
			certRef := listener.TLS.CertificateRefs[0]
			secretName := string(certRef.Name)
			secretNamespace := gateway.Namespace
			if certRef.Namespace != nil {
				secretNamespace = string(*certRef.Namespace)
			}

			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Name:      secretName,
					Namespace: secretNamespace,
				}, mock.Anything).
				Return(apierrors.NewNotFound(schema.GroupResource{
					Group:    "",
					Resource: "Secret",
				}, secretName))

			var receiver resolvedGatewayDetails
			resolved, err := model.resolveReconcileRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			assert.False(t, resolved)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, string(gatewayv1.GatewayConditionAccepted), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)

			// The full message includes the actual secret name, so we just check that it contains this substring
			fullSecretName := secretNamespace + "/" + secretName
			assert.Contains(t, statusErr.message, fmt.Sprintf("referenced secret %s not found", fullSecretName))
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
			gateway.Annotations = map[string]string{
				GatewayProgrammedCertificatesAnnotation: "previous-cert",
			}
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
					gatewayListeners:  gateway.Spec.Listeners,
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

			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), removeMissingListenersParams{
					loadBalancerID:       config.Spec.LoadBalancerID,
					knownListeners:       loadBalancer.Listeners,
					knownRoutingPolicies: loadBalancer.RoutingPolicies,
					gatewayListeners:     gateway.Spec.Listeners,
				}).
				Return(nil)

			loadBalancerModel.EXPECT().
				removeUnusedCertificates(t.Context(), removeUnusedCertificatesParams{
					loadBalancerID:                   config.Spec.LoadBalancerID,
					previouslyProgrammedCertificates: []string{"previous-cert"},
					desiredCertificates:              certificateNamesFromListenerCertificates(certificatesByListener),
					knownCertificates:                loadBalancer.Certificates,
				}).
				Return(nil).
				NotBefore(removeCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.NoError(t, err)
		})
		t.Run("programs frontend mTLS listener params and cleanup", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			fakeData := faker.New()

			config := makeRandomGatewayConfig()
			httpsListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{httpsListener}
			ref := gatewayv1.ObjectReference{
				Group: "",
				Kind:  "ConfigMap",
				Name:  gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word()),
			}
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{
						CACertificateRefs: []gatewayv1.ObjectReference{ref},
					}},
				},
			}
			compartmentID := "ocid1.compartment.oc1.." + fakeData.UUID().V4()
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			loadBalancer.CompartmentId = &compartmentID
			defaultBackendSet := makeRandomOCIBackendSet()
			certificatesByListener := map[string][]loadbalancer.Certificate{
				string(httpsListener.Name): {makeRandomOCICertificate()},
			}
			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)
			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), mock.Anything).
				Return(reconcileListenersCertificatesResult{certificatesByListener: certificatesByListener}, nil)
			loadBalancerModel.EXPECT().
				reconcileHTTPListener(t.Context(), mock.MatchedBy(func(params reconcileHTTPListenerParams) bool {
					return params.gateway != nil &&
						params.gateway.Name == gateway.Name &&
						params.loadBalancerCompartmentID == compartmentID &&
						params.listenerSpec != nil &&
						params.listenerSpec.Name == httpsListener.Name
				})).
				Return(nil).
				Once().
				NotBefore(reconcileCertificatesCall.Call)
			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), mock.Anything).
				Return(nil)
			loadBalancerModel.EXPECT().
				removeUnusedCertificates(t.Context(), mock.Anything).
				Return(nil).
				NotBefore(removeCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.NoError(t, err)
		})
		t.Run("wraps frontend mTLS cleanup errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			fakeData := faker.New()

			config := makeRandomGatewayConfig()
			httpsListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{httpsListener}
			gateway.Annotations = map[string]string{}
			gateway.Annotations[GatewayFrontendMTLSCABundleCompartmentsAnnotation] =
				"ocid1.compartment.oc1.." + fakeData.UUID().V4()
			compartmentID := "ocid1.compartment.oc1.." + fakeData.UUID().V4()
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			loadBalancer.CompartmentId = &compartmentID
			defaultBackendSet := makeRandomOCIBackendSet()

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)
			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), mock.Anything).
				Return(reconcileListenersCertificatesResult{}, nil)
			loadBalancerModel.EXPECT().
				reconcileHTTPListener(t.Context(), mock.Anything).
				Return(nil).
				NotBefore(reconcileCertificatesCall.Call)
			loadBalancerModel.EXPECT().
				cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).
				Return(errors.New("cleanup failed"))

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.ErrorContains(t, err, "failed to clean up frontend mTLS CA bundles")
		})
		t.Run("programs ListenerSet listeners with derived OCI listener names", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			directListener := makeRandomListener(randomListenerWithHTTPProtocolOpt())
			listenerSetListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{directListener}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         gateway.Namespace,
					Name:              "team-" + faker.New().Internet().Slug(),
					CreationTimestamp: metav1.Now(),
				},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: gatewayv1.ObjectName(gateway.Name)},
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     listenerSetListener.Name,
						Protocol: listenerSetListener.Protocol,
						Port:     listenerSetListener.Port,
						Hostname: listenerSetListener.Hostname,
						TLS:      listenerSetListener.TLS,
					}},
				},
			}
			data := &resolvedGatewayDetails{
				gateway:            *gateway,
				config:             config,
				listenerSets:       []gatewayv1.ListenerSet{listenerSet},
				effectiveListeners: effectiveListenersForGateway(*gateway, []gatewayv1.ListenerSet{listenerSet}),
			}
			gatewayListeners := effectiveOCIListenersForGateway(data)
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			defaultBackendSet := makeRandomOCIBackendSet()
			certificatesByListener := map[string][]loadbalancer.Certificate{}
			for _, listener := range gatewayListeners {
				certificatesByListener[string(listener.Name)] = []loadbalancer.Certificate{makeRandomOCICertificate()}
			}

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)
			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), reconcileListenersCertificatesParams{
					loadBalancerID:    config.Spec.LoadBalancerID,
					gateway:           gateway,
					gatewayListeners:  gatewayListeners,
					knownCertificates: loadBalancer.Certificates,
				}).
				Return(reconcileListenersCertificatesResult{
					certificatesByListener: certificatesByListener,
				}, nil)
			for _, listener := range gatewayListeners {
				loadBalancerModel.EXPECT().
					reconcileHTTPListener(t.Context(), mock.MatchedBy(func(params reconcileHTTPListenerParams) bool {
						return params.listenerSpec != nil &&
							params.listenerSpec.Name == listener.Name &&
							reflect.DeepEqual(
								params.listenerCertificates,
								certificatesByListener[string(listener.Name)],
							)
					})).
					Return(nil).
					Once().
					NotBefore(reconcileCertificatesCall.Call)
			}
			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), removeMissingListenersParams{
					loadBalancerID:       config.Spec.LoadBalancerID,
					knownListeners:       loadBalancer.Listeners,
					knownRoutingPolicies: loadBalancer.RoutingPolicies,
					gatewayListeners:     gatewayListeners,
				}).
				Return(nil)
			loadBalancerModel.EXPECT().
				removeUnusedCertificates(t.Context(), mock.Anything).
				Return(nil).
				NotBefore(removeCall.Call)

			err := model.programGateway(t.Context(), data)

			require.NoError(t, err)
			assert.NotEqual(t, listenerSetListener.Name, gatewayListeners[1].Name)
			assert.Contains(t, string(gatewayListeners[1].Name), "ls_")
		})
		t.Run("skips TLS listeners because TLSRoute owns ALB TLS listener reconciliation", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			httpsListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			tlsListener := makeRandomListener(func(listener *gatewayv1.Listener) {
				listener.Protocol = gatewayv1.TLSProtocolType
				listener.TLS = &gatewayv1.ListenerTLSConfig{
					CertificateRefs: []gatewayv1.SecretObjectReference{randomSecretObjectReference()},
				}
			})
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{httpsListener, tlsListener}
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			defaultBackendSet := makeRandomOCIBackendSet()
			certificatesByListener := map[string][]loadbalancer.Certificate{
				string(httpsListener.Name): {makeRandomOCICertificate()},
				string(tlsListener.Name):   {makeRandomOCICertificate()},
			}

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)

			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)
			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), mock.Anything).
				Return(reconcileListenersCertificatesResult{
					certificatesByListener: certificatesByListener,
				}, nil)
			loadBalancerModel.EXPECT().
				reconcileHTTPListener(t.Context(), mock.MatchedBy(func(params reconcileHTTPListenerParams) bool {
					return params.listenerSpec != nil && params.listenerSpec.Name == httpsListener.Name
				})).
				Return(nil).
				Once().
				NotBefore(reconcileCertificatesCall.Call)
			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), mock.Anything).
				Return(nil)
			loadBalancerModel.EXPECT().
				removeUnusedCertificates(t.Context(), mock.Anything).
				Return(nil).
				NotBefore(removeCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.NoError(t, err)
		})
		t.Run("failed to get OCI Load Balancer", func(t *testing.T) {
			fake := faker.New()
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

			wantErr := errors.New(fake.Lorem().Sentence(10))
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
		t.Run("returns programmed false status error when OCI Load Balancer is not found", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			gateway := newRandomGateway(
				randomGatewayWithRandomListenersOpt(),
			)

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(
					loadbalancer.GetLoadBalancerResponse{},
					ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(404)),
				)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, string(gatewayv1.GatewayConditionProgrammed), statusErr.conditionType)
			assert.Equal(t, string(gatewayv1.GatewayReasonPending), statusErr.reason)
			assert.Equal(t,
				fmt.Sprintf("referenced OCI Load Balancer %s not found", config.Spec.LoadBalancerID),
				statusErr.message,
			)
		})
		t.Run("failed to reconcile default backend set", func(t *testing.T) {
			fake := faker.New()
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

			wantErr := errors.New(fake.Lorem().Sentence(10))
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
			fake := faker.New()
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
					gatewayListeners:  gateway.Spec.Listeners,
					knownCertificates: loadBalancer.Certificates,
				}).
				Return(reconcileListenersCertificatesResult{
					reconciledCertificates: wantKnownCertificates,
				}, nil).
				Once()

			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)

			wantErr := errors.New(fake.Lorem().Sentence(10))
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

		t.Run("failed to reconcile listeners certificates", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			gateway := newRandomGateway(randomGatewayWithRandomListenersOpt())
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)

			defaultBackendSet := makeRandomOCIBackendSet()
			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			loadBalancerModel.EXPECT().
				reconcileDefaultBackendSet(t.Context(), mock.Anything).
				Return(defaultBackendSet, nil)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), mock.Anything).
				Return(reconcileListenersCertificatesResult{}, wantErr)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("failed to remove stale listeners or certificates", func(t *testing.T) {
			for name, failCertificates := range map[string]bool{
				"listeners":    false,
				"certificates": true,
			} {
				t.Run(name, func(t *testing.T) {
					deps := newMockDeps(t)
					model := newGatewayModel(deps)
					config := makeRandomGatewayConfig()
					gateway := newRandomGateway(randomGatewayWithRandomListenersOpt())
					loadBalancer := makeRandomOCILoadBalancer(
						randomOCILoadBalancerWithRandomBackendSetsOpt(),
						randomOCILoadBalancerWithRandomCertificatesOpt(),
					)
					defaultBackendSet := makeRandomOCIBackendSet()
					mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
					mockOciClient.EXPECT().
						GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
							LoadBalancerId: &config.Spec.LoadBalancerID,
						}).
						Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)
					loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
					loadBalancerModel.EXPECT().
						reconcileDefaultBackendSet(t.Context(), mock.Anything).
						Return(defaultBackendSet, nil)
					loadBalancerModel.EXPECT().
						reconcileListenersCertificates(t.Context(), mock.Anything).
						Return(reconcileListenersCertificatesResult{}, nil)
					for range gateway.Spec.Listeners {
						loadBalancerModel.EXPECT().
							reconcileHTTPListener(t.Context(), mock.Anything).
							Return(nil)
					}
					wantErr := errors.New(faker.New().Lorem().Sentence(10))
					removeMissingErr := wantErr
					if failCertificates {
						removeMissingErr = nil
					}
					removeCall := loadBalancerModel.EXPECT().
						removeMissingListeners(t.Context(), mock.Anything).
						Return(removeMissingErr)
					if failCertificates {
						loadBalancerModel.EXPECT().
							removeUnusedCertificates(t.Context(), mock.Anything).
							Return(wantErr).
							NotBefore(removeCall.Call)
					}

					err := model.programGateway(t.Context(), &resolvedGatewayDetails{
						gateway: *gateway,
						config:  config,
					})

					require.ErrorIs(t, err, wantErr)
				})
			}
		})
	})

	t.Run("setProgrammed", func(t *testing.T) {
		t.Run("should set programmed condition", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gateway := newRandomGateway()
			data := &resolvedGatewayDetails{
				gateway: *gateway,
				loadBalancer: &loadbalancer.LoadBalancer{
					IpAddresses: []loadbalancer.IpAddress{
						{IpAddress: new("10.0.0.12")},
						{IpAddress: new("198.51.100.20")},
						{IpAddress: new("10.0.0.12")},
						{},
						{IpAddress: new("")},
					},
				},
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().setCondition(
				t.Context(),
				setConditionParams{
					resource:      &data.gateway,
					conditions:    &data.gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					status:        metav1.ConditionTrue,
					reason:        string(gatewayv1.GatewayReasonProgrammed),
					message:       fmt.Sprintf("Gateway %s programmed by %s", data.gateway.Name, ControllerClassName),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation:    GatewayProgrammingRevisionValue,
						GatewayProgrammedCertificatesAnnotation: "",
					},
				},
			).Return(nil)

			err := model.setProgrammed(t.Context(), data)
			require.NoError(t, err)
			addressType := gatewayv1.IPAddressType
			assert.Equal(t, []gatewayv1.GatewayStatusAddress{
				{Type: &addressType, Value: "198.51.100.20"},
				{Type: &addressType, Value: "10.0.0.12"},
			}, data.gateway.Status.Addresses)

			mockResourcesModel.AssertExpectations(t)
		})

		t.Run("should set programmed condition with secrets", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gateway := newRandomGateway()
			numSecrets := 2 + rand.IntN(2) // Generate 2 or 3 secrets
			gatewaySecretsMap := make(map[string]corev1.Secret)
			expectedAnnotations := map[string]string{
				GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
			}

			for range numSecrets {
				secret := makeRandomSecret() // Generate secret with random name/namespace
				fullName := secret.Namespace + "/" + secret.Name
				gatewaySecretsMap[fullName] = secret
				secretUID := string(secret.UID)
				expectedAnnotations[GatewayUsedSecretsAnnotationPrefix+"/"+secretUID] = secret.ResourceVersion
			}
			expectedAnnotations[GatewayProgrammedCertificatesAnnotation] =
				programmedGatewayCertificatesAnnotation(programmedCertificateNamesFromSecrets(gatewaySecretsMap))

			data := &resolvedGatewayDetails{
				gateway:        *gateway,
				gatewaySecrets: gatewaySecretsMap,
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().setCondition(
				t.Context(),
				setConditionParams{
					resource:      &data.gateway,
					conditions:    &data.gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					status:        metav1.ConditionTrue,
					reason:        string(gatewayv1.GatewayReasonProgrammed),
					message:       fmt.Sprintf("Gateway %s programmed by %s", data.gateway.Name, ControllerClassName),
					annotations:   expectedAnnotations,
				},
			).Return(nil)

			err := model.setProgrammed(t.Context(), data)
			require.NoError(t, err)

			mockResourcesModel.AssertExpectations(t)
		})

		t.Run("should return error when setCondition fails", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gateway := newRandomGateway()
			data := &resolvedGatewayDetails{
				gateway: *gateway,
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			expectedErr := errors.New("setCondition error")
			mockResourcesModel.EXPECT().setCondition(
				t.Context(),
				mock.AnythingOfType("setConditionParams"),
			).Return(expectedErr)

			err := model.setProgrammed(t.Context(), data)
			require.Error(t, err)
			require.ErrorIs(t, err,
				expectedErr,
				"expected error to wrap original error")

			mockResourcesModel.AssertExpectations(t)
		})
	})

	t.Run("isProgrammed", func(t *testing.T) {
		t.Run("should return true when programmed condition is set with correct annotation", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gateway := newRandomGateway()
			data := &resolvedGatewayDetails{
				gateway: *gateway,
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().isConditionSet(
				isConditionSetParams{
					resource:      &data.gateway,
					conditions:    data.gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation:    GatewayProgrammingRevisionValue,
						GatewayProgrammedCertificatesAnnotation: "",
					},
				},
			).Return(true)

			result := model.isProgrammed(t.Context(), data)
			require.True(t, result)

			mockResourcesModel.AssertExpectations(t)
		})

		t.Run("should return false when programmed condition is not set", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gateway := newRandomGateway()
			data := &resolvedGatewayDetails{
				gateway: *gateway,
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().isConditionSet(
				isConditionSetParams{
					resource:      &data.gateway,
					conditions:    data.gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations: map[string]string{
						GatewayProgrammingRevisionAnnotation:    GatewayProgrammingRevisionValue,
						GatewayProgrammedCertificatesAnnotation: "",
					},
				},
			).Return(false)

			result := model.isProgrammed(t.Context(), data)
			require.False(t, result)

			mockResourcesModel.AssertExpectations(t)
		})

		t.Run("should check with secret annotations when gateway has secrets", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			gateway := newRandomGateway()
			numSecrets := 2 + rand.IntN(2) // Generate 2 or 3 secrets
			gatewaySecretsMap := make(map[string]corev1.Secret)
			expectedAnnotations := map[string]string{
				GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
			}

			for range numSecrets {
				secret := makeRandomSecret() // Generate secret with random name/namespace
				fullName := secret.Namespace + "/" + secret.Name
				gatewaySecretsMap[fullName] = secret
				secretUID := string(secret.UID)
				expectedAnnotations[GatewayUsedSecretsAnnotationPrefix+"/"+secretUID] = secret.ResourceVersion
			}
			expectedAnnotations[GatewayProgrammedCertificatesAnnotation] =
				programmedGatewayCertificatesAnnotation(programmedCertificateNamesFromSecrets(gatewaySecretsMap))

			data := &resolvedGatewayDetails{
				gateway:        *gateway,
				gatewaySecrets: gatewaySecretsMap,
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().isConditionSet(
				isConditionSetParams{
					resource:      &data.gateway,
					conditions:    data.gateway.Status.Conditions,
					conditionType: string(gatewayv1.GatewayConditionProgrammed),
					annotations:   expectedAnnotations,
				},
			).Return(true)

			result := model.isProgrammed(t.Context(), data)
			require.True(t, result)

			mockResourcesModel.AssertExpectations(t)
		})
	})

	t.Run("populateAttachedListenerSets", func(t *testing.T) {
		t.Run("returns indexed list errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			data := makeRandomAcceptedGatewayDetails()
			wantErr := errors.New("list failed")

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{},
					client.MatchingFields{
						listenerSetParentGatewayIndexKey: client.ObjectKeyFromObject(&data.gateway).String(),
					}).
				Return(wantErr)

			err := populateAttachedListenerSets(t.Context(), model.client, data)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to list ListenerSets")
		})

		t.Run("returns namespace lookup errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			data := makeRandomAcceptedGatewayDetails()
			fromAll := gatewayv1.NamespacesFromAll
			data.gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
				Namespaces: &gatewayv1.ListenerNamespaces{From: &fromAll},
			}
			parentNamespace := gatewayv1.Namespace(data.gateway.Namespace)
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{
						Namespace: &parentNamespace,
						Name:      gatewayv1.ObjectName(data.gateway.Name),
					},
				},
			}
			wantErr := errors.New("namespace failed")

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{},
					client.MatchingFields{
						listenerSetParentGatewayIndexKey: client.ObjectKeyFromObject(&data.gateway).String(),
					}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).
						Elem().
						FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.ListenerSet{listenerSet}))
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{Name: listenerSet.Namespace}, mock.AnythingOfType("*v1.Namespace")).
				Return(wantErr)

			err := populateAttachedListenerSets(t.Context(), model.client, data)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to get ListenerSet namespace")
		})

		t.Run("unindexed list skips missing namespaces and attaches selected namespaces", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			data := makeRandomAcceptedGatewayDetails()
			fromSelector := gatewayv1.NamespacesFromSelector
			data.gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
				Namespaces: &gatewayv1.ListenerNamespaces{
					From: &fromSelector,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
						"team": "edge",
					}},
				},
			}
			parentNamespace := gatewayv1.Namespace(data.gateway.Namespace)
			attachedListenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{
						Namespace: &parentNamespace,
						Name:      gatewayv1.ObjectName(data.gateway.Name),
					},
					Listeners: []gatewayv1.ListenerEntry{
						{Name: "tcp", Port: 1935, Protocol: gatewayv1.TCPProtocolType},
					},
				},
			}
			missingNamespaceListenerSet := attachedListenerSet
			missingNamespaceListenerSet.Namespace = "missing"
			missingNamespaceListenerSet.Name = "missing"

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().FieldByName("Items").Set(reflect.ValueOf([]gatewayv1.ListenerSet{
						missingNamespaceListenerSet,
						attachedListenerSet,
					}))
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{Name: missingNamespaceListenerSet.Namespace}, mock.Anything).
				Return(apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, missingNamespaceListenerSet.Namespace))
			setupClientGet(
				t,
				mockClient,
				apitypes.NamespacedName{Name: attachedListenerSet.Namespace},
				corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:   attachedListenerSet.Namespace,
						Labels: map[string]string{"team": "edge"},
					},
				},
			)

			err := populateAttachedListenerSetsUnindexed(t.Context(), model.client, data)

			require.NoError(t, err)
			require.Len(t, data.listenerSets, 1)
			assert.Equal(t, attachedListenerSet.Name, data.listenerSets[0].Name)
			require.Len(t, data.effectiveListeners, len(data.gateway.Spec.Listeners)+1)
		})
	})

	t.Run("setListenerSetsProgrammed", func(t *testing.T) {
		t.Run("updates only semantically changed ListenerSet status", func(t *testing.T) {
			deps := newMockDeps(t)
			gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:  "apps",
					Name:       "extra",
					Generation: 3,
				},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: gatewayv1.ObjectName(gateway.Name)},
					Listeners: []gatewayv1.ListenerEntry{
						{Name: "https", Port: 443, Protocol: gatewayv1.HTTPSProtocolType},
					},
				},
			}
			upToDate := listenerSet
			upToDate.Name = "current"
			upToDate.Spec.Listeners = append([]gatewayv1.ListenerEntry(nil), listenerSet.Spec.Listeners...)
			upToDate.Spec.Listeners[0].Port = 8443
			data := &resolvedGatewayDetails{
				gateway: gateway,
				listenerSets: []gatewayv1.ListenerSet{
					upToDate,
					listenerSet,
				},
			}
			data.effectiveListeners = effectiveListenersForGateway(gateway, data.listenerSets)
			data.listenerSets[0].Status = listenerSetStatusForGateway(
				gateway,
				data.listenerSets[0],
				data.effectiveListeners,
				gatewayv1.GatewayController(ControllerClassName),
			)

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					updated, ok := obj.(*gatewayv1.ListenerSet)
					return ok &&
						updated.Namespace == listenerSet.Namespace &&
						updated.Name == listenerSet.Name &&
						meta.IsStatusConditionTrue(
							updated.Status.Conditions,
							string(gatewayv1.ListenerSetConditionProgrammed),
						)
				})).
				Return(nil).
				Once()

			err := setListenerSetsProgrammed(
				t.Context(),
				mockClient,
				data,
				gatewayv1.GatewayController(ControllerClassName),
			)

			require.NoError(t, err)
		})

		t.Run("returns status update errors", func(t *testing.T) {
			deps := newMockDeps(t)
			gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: gatewayv1.ObjectName(gateway.Name)},
					Listeners: []gatewayv1.ListenerEntry{
						{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType},
					},
				},
			}
			data := &resolvedGatewayDetails{
				gateway:            gateway,
				listenerSets:       []gatewayv1.ListenerSet{listenerSet},
				effectiveListeners: effectiveListenersForGateway(gateway, []gatewayv1.ListenerSet{listenerSet}),
			}
			wantErr := errors.New("status failed")

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
			mockStatusWriter.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr).Once()

			err := setListenerSetsProgrammed(
				t.Context(),
				mockClient,
				data,
				gatewayv1.GatewayController(ControllerClassName),
			)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to update ListenerSet apps/extra status")
		})
	})

	t.Run("setProgrammed returns ListenerSet status update errors", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newGatewayModel(deps)
		listenerSet := gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
				Listeners: []gatewayv1.ListenerEntry{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
			},
		}
		data := &resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
			listenerSets: []gatewayv1.ListenerSet{
				listenerSet,
			},
			effectiveListeners: effectiveListenersForGateway(
				gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
				[]gatewayv1.ListenerSet{listenerSet},
			),
		}
		wantErr := errors.New("listenerset status failed")

		mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
		mockResourcesModel.EXPECT().
			setCondition(t.Context(), mock.Anything).
			Return(nil).
			Once()
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr).Once()

		err := model.setProgrammed(t.Context(), data)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("populateGatewaySecrets with effective listeners", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newGatewayModel(deps)
		gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}}
		certNamespace := gatewayv1.Namespace("apps")
		certName := gatewayv1.ObjectName("tls-cert")
		data := &resolvedGatewayDetails{
			gateway: gateway,
			effectiveListeners: []effectiveListener{
				{
					listener: gatewayv1.Listener{
						Name:     "conflicted",
						Protocol: gatewayv1.HTTPSProtocolType,
						TLS: &gatewayv1.ListenerTLSConfig{
							CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "ignored-conflict"}},
						},
					},
					conflicted: true,
				},
				{
					listener: gatewayv1.Listener{
						Name:     "oci-cert",
						Protocol: gatewayv1.HTTPSProtocolType,
						TLS: &gatewayv1.ListenerTLSConfig{
							Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
								ListenerTLSOptionOCICertificateOCID: "ocid1.certificate.oc1..test",
							},
						},
					},
				},
				{
					sourceNamespace: string(certNamespace),
					listener: gatewayv1.Listener{
						Name:     "https",
						Protocol: gatewayv1.HTTPSProtocolType,
						TLS: &gatewayv1.ListenerTLSConfig{
							CertificateRefs: []gatewayv1.SecretObjectReference{{
								Namespace: &certNamespace,
								Name:      certName,
							}},
						},
					},
				},
			},
		}
		secret := makeRandomSecret(
			randomSecretWithNameOpt(string(certName)),
			randomSecretWithTLSDataOpt(),
		)
		secret.Namespace = string(certNamespace)

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		setupClientGet(t, mockClient, apitypes.NamespacedName{
			Namespace: secret.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		err := model.populateGatewaySecrets(t.Context(), data)

		require.NoError(t, err)
		assert.Len(t, data.gatewaySecrets, 1)
		assert.Contains(t, data.gatewaySecrets, secret.Namespace+"/"+secret.Name)
	})

	t.Run(
		"populateGatewaySecrets rejects cross namespace ListenerSet certificate without ReferenceGrant",
		func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			certNamespace := gatewayv1.Namespace("certs")
			certName := gatewayv1.ObjectName("tls-cert")
			data := &resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
				effectiveListeners: []effectiveListener{{
					sourceKind:      effectiveListenerSourceListenerSet,
					sourceNamespace: "apps",
					listener: gatewayv1.Listener{
						Name:     "https",
						Protocol: gatewayv1.HTTPSProtocolType,
						TLS: &gatewayv1.ListenerTLSConfig{
							CertificateRefs: []gatewayv1.SecretObjectReference{{
								Namespace: &certNamespace,
								Name:      certName,
							}},
						},
					},
				}},
			}
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{}))
					return nil
				})

			err := model.populateGatewaySecrets(t.Context(), data)

			var statusErr *resourceStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), statusErr.reason)
			assert.Contains(t, statusErr.message, "certificateRef certs/tls-cert is not permitted by a ReferenceGrant")
		},
	)
}

func TestProgrammedGatewayCertificatesAnnotation(t *testing.T) {
	t.Run("collects OCI certificate IDs by listener", func(t *testing.T) {
		assert.Equal(t, map[string]string{
			"https": "ocid1.certificate.oc1..test",
		}, gatewayCertificateIDsByListener(gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{
				{Name: "http"},
				{
					Name: "https",
					TLS: &gatewayv1.ListenerTLSConfig{
						Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
							ListenerTLSOptionOCICertificateOCID: "ocid1.certificate.oc1..test",
						},
					},
				},
			}},
		}))
	})

	t.Run("normalizes annotation values", func(t *testing.T) {
		got := programmedGatewayCertificatesAnnotation([]string{
			"kora-cert-rev-2",
			"",
			"kora-cert-rev-1",
			"kora-cert-rev-2",
		})

		assert.Equal(t, "kora-cert-rev-1,kora-cert-rev-2", got)
	})

	t.Run("parses annotation values", func(t *testing.T) {
		got := parseProgrammedGatewayCertificatesAnnotation(" cert-b,,cert-a, cert-b ")

		assert.Equal(t, []string{"cert-a", "cert-b"}, got)
	})

	t.Run("maps secrets to certificate names", func(t *testing.T) {
		secretA := makeRandomSecret()
		secretB := makeRandomSecret()
		got := programmedCertificateNamesFromSecrets(map[string]corev1.Secret{
			secretA.Namespace + "/" + secretA.Name: secretA,
			secretB.Namespace + "/" + secretB.Name: secretB,
		})

		assert.ElementsMatch(t, []string{
			ociCertificateNameFromSecret(secretA),
			ociCertificateNameFromSecret(secretB),
		}, got)
	})
}

func TestGatewayCertificateOptionsValidation(t *testing.T) {
	makeGateway := func(listeners ...gatewayv1.Listener) gatewayv1.Gateway {
		return gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{Listeners: listeners},
		}
	}
	withOCICertificateOption := func(listener gatewayv1.Listener, certificateID string) gatewayv1.Listener {
		listener.TLS = &gatewayv1.ListenerTLSConfig{
			Options: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
				gatewayv1.AnnotationKey(ListenerTLSOptionOCICertificateOCID): gatewayv1.AnnotationValue(certificateID),
			},
		}
		return listener
	}
	withTLSMode := func(listener gatewayv1.Listener, mode gatewayv1.TLSModeType) gatewayv1.Listener {
		listener.TLS.Mode = &mode
		return listener
	}
	httpsListener := gatewayv1.Listener{
		Name:     "https",
		Protocol: gatewayv1.HTTPSProtocolType,
		Port:     443,
	}

	t.Run("accepts OCI certificate option without certificateRefs", func(t *testing.T) {
		err := validateGatewayCertificateOptions(makeGateway(
			withOCICertificateOption(httpsListener, "ocid1.certificate.oc1..test"),
		))

		require.NoError(t, err)
	})

	t.Run("accepts OCI certificate option with terminate TLS mode", func(t *testing.T) {
		err := validateGatewayCertificateOptions(makeGateway(
			withTLSMode(
				withOCICertificateOption(httpsListener, "ocid1.certificate.oc1..test"),
				gatewayv1.TLSModeTerminate,
			),
		))

		require.NoError(t, err)
	})

	t.Run("accepts multiple OCI certificate options for different listeners", func(t *testing.T) {
		adminListener := gatewayv1.Listener{
			Name:     "admin-https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     8443,
		}

		err := validateGatewayCertificateOptions(makeGateway(
			withOCICertificateOption(httpsListener, "ocid1.certificate.oc1..public"),
			withOCICertificateOption(adminListener, "ocid1.certificate.oc1..admin"),
		))

		require.NoError(t, err)
	})

	t.Run("rejects certificate option on non TLS listener", func(t *testing.T) {
		err := validateGatewayCertificateOptions(makeGateway(withOCICertificateOption(gatewayv1.Listener{
			Name:     "http",
			Protocol: gatewayv1.HTTPProtocolType,
			Port:     80,
		}, "ocid1.certificate.oc1..test")))

		require.ErrorContains(t, err, "can only be used with HTTPS or TLS listeners")
	})

	t.Run("accepts certificate option on TLS terminate listener", func(t *testing.T) {
		err := validateGatewayCertificateOptions(makeGateway(withOCICertificateOption(gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
		}, "ocid1.certificate.oc1..test")))

		require.NoError(t, err)
	})

	t.Run("rejects certificate option with passthrough TLS mode", func(t *testing.T) {
		err := validateGatewayCertificateOptions(makeGateway(
			withTLSMode(
				withOCICertificateOption(httpsListener, "ocid1.certificate.oc1..test"),
				gatewayv1.TLSModePassthrough,
			),
		))

		require.ErrorContains(t, err, "can only be used with Terminate TLS mode")
	})

	t.Run("rejects conflict with certificateRefs", func(t *testing.T) {
		listener := withOCICertificateOption(httpsListener, "ocid1.certificate.oc1..test")
		listener.TLS.CertificateRefs = []gatewayv1.SecretObjectReference{{Name: "tls-secret"}}

		err := validateGatewayCertificateOptions(makeGateway(listener))

		require.ErrorContains(t, err, "cannot be used together with listener.tls.certificateRefs")
	})

	t.Run("skips secret population for OCI certificate listeners", func(t *testing.T) {
		model := &gatewayModelImpl{client: NewMockk8sClient(t)}
		details := resolvedGatewayDetails{
			gateway: makeGateway(withOCICertificateOption(httpsListener, "ocid1.certificate.oc1..test")),
		}

		err := model.populateGatewaySecrets(t.Context(), &details)

		require.NoError(t, err)
		assert.Empty(t, details.gatewaySecrets)
	})

	t.Run("does not reload duplicate certificateRefs", func(t *testing.T) {
		model := &gatewayModelImpl{client: NewMockk8sClient(t)}
		listener := httpsListener
		listener.TLS = &gatewayv1.ListenerTLSConfig{
			CertificateRefs: []gatewayv1.SecretObjectReference{
				{Name: "tls-secret"},
				{Name: "tls-secret"},
			},
		}
		details := resolvedGatewayDetails{
			gateway: makeGateway(listener),
		}
		details.gateway.Namespace = "gateway-ns"
		secret := makeRandomSecret(
			randomSecretWithNameOpt("tls-secret"),
			randomSecretWithTLSDataOpt(),
		)
		secret.Namespace = details.gateway.Namespace

		mockClient, _ := model.client.(*Mockk8sClient)
		setupClientGet(t, mockClient, apitypes.NamespacedName{
			Namespace: details.gateway.Namespace,
			Name:      secret.Name,
		}, secret).Once()

		err := model.populateGatewaySecrets(t.Context(), &details)

		require.NoError(t, err)
		assert.Len(t, details.gatewaySecrets, 1)
	})

	t.Run("returns generic secret lookup errors", func(t *testing.T) {
		model := &gatewayModelImpl{client: NewMockk8sClient(t)}
		listener := httpsListener
		listener.TLS = &gatewayv1.ListenerTLSConfig{
			CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-secret"}},
		}
		details := resolvedGatewayDetails{
			gateway: makeGateway(listener),
		}
		details.gateway.Namespace = "gateway-ns"
		wantErr := errors.New("k8s unavailable")

		mockClient, _ := model.client.(*Mockk8sClient)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{
				Namespace: details.gateway.Namespace,
				Name:      "tls-secret",
			}, mock.Anything).
			Return(wantErr).
			Once()

		err := model.populateGatewaySecrets(t.Context(), &details)

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to get secret gateway-ns/tls-secret")
	})
}
