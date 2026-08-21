package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	expectEmptyListenerSetRouteCountLists := func(t *testing.T, mockClient *Mockk8sClient, listenerSetCount int) {
		t.Helper()
		mockClient.EXPECT().
			List(t.Context(), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.Zero(reflect.ValueOf(list).Elem().Type()))
				return nil
			}).
			Times(5 * listenerSetCount)
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

		t.Run("deleting gateway can finalize without infrastructure", func(t *testing.T) {
			fakeData := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fakeData.UUID().V4()
			gatewayClass := newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(ControllerClassName))
			gateway := newRandomGateway()
			deletionTime := metav1.Now()
			gateway.DeletionTimestamp = &deletionTime
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Annotations = map[string]string{LoadBalancerGatewayIDAnnotation: loadBalancerID}
			gateway.Spec.GatewayClassName = gatewayv1.ObjectName(gatewayClass.Name)
			gateway.Spec.Infrastructure = nil
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(gateway, gatewayClass).
				Build()
			model := newGatewayModel(gatewayModelDeps{
				K8sClient:            k8sClient,
				ResourcesModel:       NewMockresourcesModel(t),
				RootLogger:           diag.RootTestLogger(),
				OciClient:            NewMockociLoadBalancerClient(t),
				OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
			})

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(gateway),
			}, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)
			assert.Equal(t, loadBalancerID, receiver.config.Spec.LoadBalancerID)
		})

		t.Run("deleting gateway can finalize without GatewayConfig", func(t *testing.T) {
			fakeData := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fakeData.UUID().V4()
			configName := "config-" + fakeData.Lorem().Word()
			gatewayClass := newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(ControllerClassName))
			gateway := newRandomGateway()
			deletionTime := metav1.Now()
			gateway.DeletionTimestamp = &deletionTime
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Annotations = map[string]string{LoadBalancerGatewayIDAnnotation: loadBalancerID}
			gateway.Spec.GatewayClassName = gatewayv1.ObjectName(gatewayClass.Name)
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  configName,
				},
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(gateway, gatewayClass).
				Build()
			model := newGatewayModel(gatewayModelDeps{
				K8sClient:            k8sClient,
				ResourcesModel:       NewMockresourcesModel(t),
				RootLogger:           diag.RootTestLogger(),
				OciClient:            NewMockociLoadBalancerClient(t),
				OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
			})

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(gateway),
			}, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)
			assert.Equal(t, loadBalancerID, receiver.config.Spec.LoadBalancerID)
		})

		t.Run("deleting gateway skips missing TLS secret resolution", func(t *testing.T) {
			fakeData := faker.New()
			configName := "config-" + fakeData.Lorem().Word()
			gatewayClass := newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(ControllerClassName))
			gateway := newRandomGateway(randomGatewayWithListenersOpt(gatewayv1.Listener{
				Name:     "https",
				Protocol: gatewayv1.HTTPSProtocolType,
				Port:     443,
				TLS: &gatewayv1.ListenerTLSConfig{
					CertificateRefs: []gatewayv1.SecretObjectReference{{
						Name: gatewayv1.ObjectName("missing-" + fakeData.Lorem().Word()),
					}},
				},
			}))
			deletionTime := metav1.Now()
			gateway.DeletionTimestamp = &deletionTime
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Spec.GatewayClassName = gatewayv1.ObjectName(gatewayClass.Name)
			gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: ConfigRefGroup,
					Kind:  ConfigRefKind,
					Name:  configName,
				},
			}
			gatewayConfig := &types.GatewayConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: gateway.Namespace, Name: configName},
				Spec: types.GatewayConfigSpec{
					LoadBalancerID: "ocid1.loadbalancer.oc1.." + fakeData.UUID().V4(),
				},
			}
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(gateway, gatewayClass, gatewayConfig).
				Build()
			model := newGatewayModel(gatewayModelDeps{
				K8sClient:            k8sClient,
				ResourcesModel:       NewMockresourcesModel(t),
				RootLogger:           diag.RootTestLogger(),
				OciClient:            NewMockociLoadBalancerClient(t),
				OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
			})

			var receiver resolvedGatewayDetails
			relevant, err := model.resolveReconcileRequest(t.Context(), reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(gateway),
			}, &receiver)

			require.NoError(t, err)
			assert.True(t, relevant)
			assert.Empty(t, receiver.gatewaySecrets)
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
			fake := faker.New()
			listenerPort := 8000 + fake.Int32Between(1, 1000)
			firstListener := makeRandomListener(
				randomListenerWithNameOpt(gatewayv1.SectionName("listener-"+fake.UUID().V4())),
				randomListenerWithHTTPProtocolOpt(),
			)
			firstListener.Port = listenerPort
			secondListener := makeRandomListener(
				randomListenerWithNameOpt(gatewayv1.SectionName("listener-"+fake.UUID().V4())),
				randomListenerWithHTTPProtocolOpt(),
			)
			secondListener.Port = listenerPort + 1
			gateway := newRandomGateway(
				randomGatewayWithListenersOpt(firstListener, secondListener),
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
					cleanupListenerNames: listenerNamesSet(gateway.Spec.Listeners),
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

		t.Run("does not cleanup another gateway listener when its default backend set drifts", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			ownedListener := makeRandomListener(
				randomListenerWithHTTPProtocolOpt(),
				randomListenerWithNameOpt(gatewayv1.SectionName("owned-"+fake.Lorem().Word())),
			)
			gateway := newRandomGateway(randomGatewayWithListenersOpt(ownedListener))
			defaultBackendSet := makeRandomOCIBackendSet(func(backendSet *loadbalancer.BackendSet) {
				backendSet.Name = new(gatewayDefaultBackendSetName(*gateway))
			})
			ownedOCIListener := makeRandomOCIListener(func(listener *loadbalancer.Listener) {
				listener.Name = new(string(ownedListener.Name))
				listener.DefaultBackendSetName = defaultBackendSet.Name
			})
			otherGatewayListenerName := "other-" + fake.Lorem().Word()
			otherGatewayOCIListener := makeRandomOCIListener(func(listener *loadbalancer.Listener) {
				listener.Name = &otherGatewayListenerName
				listener.DefaultBackendSetName = defaultBackendSet.Name
				listener.RoutingPolicyName = new(listenerPolicyName(otherGatewayListenerName))
			})
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			loadBalancer.Listeners = map[string]loadbalancer.Listener{
				*ownedOCIListener.Name:        ownedOCIListener,
				*otherGatewayOCIListener.Name: otherGatewayOCIListener,
			}

			mockOciClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOciClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{
					LoadBalancerId: &config.Spec.LoadBalancerID,
				}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadBalancer}, nil)

			loadBalancerModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
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
				Return(reconcileListenersCertificatesResult{}, nil)
			loadBalancerModel.EXPECT().
				reconcileHTTPListener(t.Context(), mock.MatchedBy(func(params reconcileHTTPListenerParams) bool {
					return params.loadBalancerID == config.Spec.LoadBalancerID &&
						params.defaultBackendSetName == *defaultBackendSet.Name &&
						params.listenerSpec != nil &&
						params.listenerSpec.Name == ownedListener.Name
				})).
				Return(nil).
				NotBefore(reconcileCertificatesCall.Call)
			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), mock.MatchedBy(func(params removeMissingListenersParams) bool {
					_, hasOwned := params.cleanupListenerNames[*ownedOCIListener.Name]
					_, hasOtherGateway := params.cleanupListenerNames[*otherGatewayOCIListener.Name]
					return hasOwned && !hasOtherGateway
				})).
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
			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), mock.Anything).
				Return(nil)
			loadBalancerModel.EXPECT().
				cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).
				Return(errors.New("cleanup failed")).
				NotBefore(removeCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.ErrorContains(t, err, "failed to clean up frontend mTLS CA bundles")
		})
		t.Run("removes missing listeners before cleaning up frontend mTLS CA bundles", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			fakeData := faker.New()

			config := makeRandomGatewayConfig()
			httpsListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{httpsListener}
			gateway.Annotations = map[string]string{
				GatewayFrontendMTLSCABundleCompartmentsAnnotation: "ocid1.compartment.oc1.." + fakeData.UUID().V4(),
			}
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
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
			reconcileCertificatesCall := loadBalancerModel.EXPECT().
				reconcileListenersCertificates(t.Context(), mock.Anything).
				Return(reconcileListenersCertificatesResult{}, nil)
			loadBalancerModel.EXPECT().
				reconcileHTTPListener(t.Context(), mock.Anything).
				Return(nil).
				NotBefore(reconcileCertificatesCall.Call)
			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), mock.Anything).
				Return(nil)
			cleanupCall := loadBalancerModel.EXPECT().
				cleanupFrontendMTLSCABundles(t.Context(), mock.Anything)
			cleanupCall.Return(nil).NotBefore(removeCall.Call)
			loadBalancerModel.EXPECT().
				removeUnusedCertificates(t.Context(), mock.Anything).
				Return(nil).
				NotBefore(cleanupCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.NoError(t, err)
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
					cleanupListenerNames: listenerNamesSet(gatewayListeners),
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
		t.Run("removes frontend mTLS listener when CA ReferenceGrant is revoked", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			listener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway()
			caNamespace := gatewayv1.Namespace("security-" + fake.Lorem().Word())
			gateway.Spec.Listeners = []gatewayv1.Listener{listener}
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					PerPort: []gatewayv1.TLSPortConfig{{
						Port: listener.Port,
						TLS: gatewayv1.TLSConfig{
							Validation: &gatewayv1.FrontendTLSValidation{
								CACertificateRefs: []gatewayv1.ObjectReference{{
									Name:      gatewayv1.ObjectName("ca-bundle-" + fake.Lorem().Word()),
									Namespace: &caNamespace,
								}},
							},
						},
					}},
				},
			}
			knownListener := makeRandomOCIListener()
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			loadBalancer.Listeners = map[string]loadbalancer.Listener{
				string(listener.Name): knownListener,
			}
			defaultBackendSet := makeRandomOCIBackendSet()
			statusErr := frontendMTLSStatusError(
				string(gatewayv1.GatewayReasonInvalidParameters),
				"frontend mTLS caCertificateRef is not permitted by a ReferenceGrant",
			)

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
				Return(reconcileListenersCertificatesResult{}, statusErr)
			loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), removeMissingListenersParams{
					loadBalancerID:       config.Spec.LoadBalancerID,
					knownListeners:       loadBalancer.Listeners,
					knownRoutingPolicies: loadBalancer.RoutingPolicies,
					cleanupListenerNames: map[string]struct{}{},
					gatewayListeners:     nil,
				}).
				Return(nil).
				Once().
				NotBefore(reconcileCertificatesCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.ErrorIs(t, err, statusErr)
		})
		t.Run("returns fail closed listener removal errors", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			config := makeRandomGatewayConfig()
			listener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
			gateway := newRandomGateway()
			gateway.Spec.Listeners = []gatewayv1.Listener{listener}
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{
						CACertificateRefs: []gatewayv1.ObjectReference{{
							Name: gatewayv1.ObjectName("ca-bundle-" + fake.Lorem().Word()),
						}},
					}},
				},
			}
			loadBalancer := makeRandomOCILoadBalancer(
				randomOCILoadBalancerWithRandomBackendSetsOpt(),
				randomOCILoadBalancerWithRandomPoliciesOpt(),
				randomOCILoadBalancerWithRandomCertificatesOpt(),
			)
			loadBalancer.Listeners = map[string]loadbalancer.Listener{
				string(listener.Name): makeRandomOCIListener(),
			}
			defaultBackendSet := makeRandomOCIBackendSet()
			statusErr := frontendMTLSStatusError(
				string(gatewayv1.GatewayReasonInvalidParameters),
				"frontend mTLS caCertificateRef is not permitted by a ReferenceGrant",
			)
			wantErr := errors.New("remove failed")

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
				Return(reconcileListenersCertificatesResult{}, statusErr)
			loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), mock.Anything).
				Return(wantErr).
				Once().
				NotBefore(reconcileCertificatesCall.Call)

			err := model.programGateway(t.Context(), &resolvedGatewayDetails{
				gateway: *gateway,
				config:  config,
			})

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to fail closed frontend mTLS listeners")
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
						LoadBalancerGatewayProgrammedListenersAnnotation: programmedGatewayListenersAnnotation(
							gatewayManagedOCIListenersForLoadBalancer(data),
						),
					},
					removeAnnotations: []string{
						GatewayFrontendMTLSConfigMapsAnnotation,
						GatewayFrontendMTLSReferenceGrantsAnnotation,
					},
					finalizer: LoadBalancerGatewayProgrammedFinalizer,
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
			expectedAnnotations[LoadBalancerGatewayProgrammedListenersAnnotation] =
				programmedGatewayListenersAnnotation(gatewayManagedOCIListenersForLoadBalancer(data))

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
					removeAnnotations: []string{
						GatewayFrontendMTLSConfigMapsAnnotation,
						GatewayFrontendMTLSReferenceGrantsAnnotation,
					},
					finalizer: LoadBalancerGatewayProgrammedFinalizer,
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

	t.Run("deprovisionGateway", func(t *testing.T) {
		t.Run("uses annotated load balancer id when config is empty", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			gateway := newRandomGateway()
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Annotations = map[string]string{LoadBalancerGatewayIDAnnotation: loadBalancerID}
			data := &resolvedGatewayDetails{gateway: *gateway}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						Listeners:       map[string]loadbalancer.Listener{},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{},
						Certificates:    map[string]loadbalancer.Certificate{},
					},
				}, nil)
			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().removeMissingListeners(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, gatewayDefaultBackendSetName(data.gateway)).
				Return(nil)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(nil)

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("removes finalizer when load balancer is already gone", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			gateway := newRandomGateway()
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			data := &resolvedGatewayDetails{
				gateway: *gateway,
				config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID}},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{},
					ociapi.NewRandomServiceError(ociapi.RandomServiceErrorWithStatusCode(http.StatusNotFound)))
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(nil)

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("keeps finalizer when load balancer lookup fails", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))
			data := &resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID}},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{}, wantErr)

			err := model.deprovisionGateway(t.Context(), data)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("keeps finalizer when listener cleanup fails", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			wantErr := errors.New(fake.Lorem().Sentence(10))
			data := &resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID}},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{}}, nil)
			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().removeMissingListeners(t.Context(), mock.Anything).Return(wantErr)

			err := model.deprovisionGateway(t.Context(), data)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("keeps finalizer when later OCI cleanup fails", func(t *testing.T) {
			fake := faker.New()
			for name, setup := range map[string]func(*MockociLoadBalancerModel, error){
				"certificates": func(mockLBModel *MockociLoadBalancerModel, wantErr error) {
					mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(wantErr)
				},
				"frontend mTLS CA bundles": func(mockLBModel *MockociLoadBalancerModel, wantErr error) {
					mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(nil)
					mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(wantErr)
				},
				"default backend set": func(mockLBModel *MockociLoadBalancerModel, wantErr error) {
					mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(nil)
					mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(nil)
					mockLBModel.EXPECT().
						deprovisionBackendSetByName(t.Context(), mock.Anything, mock.Anything).
						Return(wantErr)
				},
			} {
				t.Run(name, func(t *testing.T) {
					loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
					wantErr := errors.New(fake.Lorem().Sentence(10))
					data := &resolvedGatewayDetails{
						gateway: *newRandomGateway(),
						config:  types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID}},
					}
					deps := newMockDeps(t)
					model := newGatewayModel(deps)
					mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
					mockOCIClient.EXPECT().
						GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
						Return(loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{}}, nil)
					mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
					mockLBModel.EXPECT().removeMissingListeners(t.Context(), mock.Anything).Return(nil)
					setup(mockLBModel, wantErr)

					err := model.deprovisionGateway(t.Context(), data)

					require.ErrorIs(t, err, wantErr)
				})
			}
		})

		t.Run("wraps finalizer update errors", func(t *testing.T) {
			fake := faker.New()
			wantErr := errors.New(fake.Lorem().Sentence(10))
			data := &resolvedGatewayDetails{gateway: *newRandomGateway()}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(wantErr)

			err := model.deprovisionGateway(t.Context(), data)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to remove finalizer from Gateway")
		})

		t.Run("ignores finalizer update not found after namespace deletion", func(t *testing.T) {
			gateway := newRandomGateway()
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			data := &resolvedGatewayDetails{gateway: *gateway}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).
				Return(apierrors.NewNotFound(gatewayv1.Resource("gateways"), gateway.Name))

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("removes owned OCI resources and preserves unrelated listeners", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			listenerName := "https-" + fake.Lorem().Word()
			unrelatedListenerName := "manual-" + fake.Lorem().Word()
			routingPolicyName := listenerPolicyName(listenerName)
			certName := "cert-" + fake.Lorem().Word()
			compartmentID := "ocid1.compartment.oc1.." + fake.UUID().V4()
			gateway := newRandomGateway(randomGatewayWithListenersOpt(gatewayv1.Listener{
				Name:     gatewayv1.SectionName(listenerName),
				Protocol: gatewayv1.HTTPSProtocolType,
				Port:     443,
			}))
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Annotations = map[string]string{
				LoadBalancerGatewayIDAnnotation:                             loadBalancerID,
				GatewayProgrammingRevisionAnnotation:                        GatewayProgrammingRevisionValue,
				GatewayProgrammedCertificatesAnnotation:                     certName,
				LoadBalancerGatewayProgrammedListenersAnnotation:            listenerName,
				GatewayFrontendMTLSCABundleCompartmentsAnnotation:           compartmentID,
				GatewayUsedSecretsAnnotationPrefix + "/" + fake.UUID().V4(): fake.UUID().V4(),
			}
			data := &resolvedGatewayDetails{
				gateway: *gateway,
				config: types.GatewayConfig{
					Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID},
				},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						CompartmentId: &compartmentID,
						Listeners: map[string]loadbalancer.Listener{
							listenerName: {
								Name:                  new(listenerName),
								RoutingPolicyName:     new(routingPolicyName),
								DefaultBackendSetName: new(gatewayDefaultBackendSetName(data.gateway)),
							},
							unrelatedListenerName: {
								Name:                  new(unrelatedListenerName),
								DefaultBackendSetName: new("other-" + fake.Lorem().Word()),
							},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							routingPolicyName: {Name: new(routingPolicyName)},
						},
						Certificates: map[string]loadbalancer.Certificate{
							certName: {CertificateName: new(certName)},
						},
					},
				}, nil)

			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().
				removeMissingListeners(t.Context(), mock.MatchedBy(func(params removeMissingListenersParams) bool {
					_, hasOwned := params.knownListeners[listenerName]
					_, hasUnrelated := params.knownListeners[unrelatedListenerName]
					return params.loadBalancerID == loadBalancerID &&
						hasOwned &&
						hasUnrelated &&
						assert.ObjectsAreEqual(
							map[string]struct{}{listenerName: {}},
							params.cleanupListenerNames,
						) &&
						len(params.gatewayListeners) == 0
				})).
				Return(nil)
			mockLBModel.EXPECT().
				removeUnusedCertificates(t.Context(), mock.MatchedBy(func(params removeUnusedCertificatesParams) bool {
					return params.loadBalancerID == loadBalancerID &&
						assert.ObjectsAreEqual([]string{certName}, params.previouslyProgrammedCertificates) &&
						len(params.desiredCertificates) == 0
				})).
				Return(nil)
			mockLBModel.EXPECT().
				cleanupFrontendMTLSCABundles(t.Context(), mock.MatchedBy(func(params cleanupFrontendMTLSCABundlesParams) bool {
					return params.gateway == &data.gateway &&
						params.compartmentID == compartmentID &&
						len(params.desiredBundleNames) == 0
				})).
				Return(nil)
			mockLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, gatewayDefaultBackendSetName(data.gateway)).
				Return(nil)

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).
				RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
					assert.NotContains(t, obj.GetFinalizers(), LoadBalancerGatewayProgrammedFinalizer)
					assert.NotContains(t, obj.GetAnnotations(), LoadBalancerGatewayIDAnnotation)
					assert.NotContains(t, obj.GetAnnotations(), GatewayProgrammingRevisionAnnotation)
					assert.NotContains(t, obj.GetAnnotations(), GatewayProgrammedCertificatesAnnotation)
					assert.NotContains(t, obj.GetAnnotations(), LoadBalancerGatewayProgrammedListenersAnnotation)
					assert.NotContains(t, obj.GetAnnotations(), GatewayFrontendMTLSCABundleCompartmentsAnnotation)
					for key := range obj.GetAnnotations() {
						assert.NotContains(t, key, GatewayUsedSecretsAnnotationPrefix+"/")
					}
					return nil
				})

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("preserves certificate used by another listener during deprovision", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			listenerName := "https-" + fake.Lorem().Word()
			sharedListenerName := "shared-" + fake.Lorem().Word()
			routingPolicyName := listenerPolicyName(listenerName)
			certName := "cert-" + fake.Lorem().Word()
			gateway := newRandomGateway(randomGatewayWithListenersOpt(gatewayv1.Listener{
				Name:     gatewayv1.SectionName(listenerName),
				Protocol: gatewayv1.HTTPSProtocolType,
				Port:     443,
			}))
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Annotations = map[string]string{
				LoadBalancerGatewayIDAnnotation:                  loadBalancerID,
				GatewayProgrammedCertificatesAnnotation:          certName,
				LoadBalancerGatewayProgrammedListenersAnnotation: listenerName,
			}
			data := &resolvedGatewayDetails{
				gateway: *gateway,
				config: types.GatewayConfig{
					Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID},
				},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						Listeners: map[string]loadbalancer.Listener{
							listenerName: {
								Name:                  new(listenerName),
								RoutingPolicyName:     new(routingPolicyName),
								DefaultBackendSetName: new(gatewayDefaultBackendSetName(data.gateway)),
								SslConfiguration:      &loadbalancer.SslConfiguration{CertificateName: new(certName)},
							},
							sharedListenerName: {
								Name:                  new(sharedListenerName),
								DefaultBackendSetName: new("other-" + fake.Lorem().Word()),
								SslConfiguration:      &loadbalancer.SslConfiguration{CertificateName: new(certName)},
							},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							routingPolicyName: {Name: new(routingPolicyName)},
						},
						Certificates: map[string]loadbalancer.Certificate{
							certName: {CertificateName: new(certName)},
						},
					},
				}, nil)

			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().removeMissingListeners(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().
				removeUnusedCertificates(t.Context(), mock.MatchedBy(func(params removeUnusedCertificatesParams) bool {
					return params.loadBalancerID == loadBalancerID &&
						assert.ObjectsAreEqual([]string{certName}, params.previouslyProgrammedCertificates) &&
						assert.ObjectsAreEqual([]string{certName}, params.desiredCertificates)
				})).
				Return(nil)
			mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, gatewayDefaultBackendSetName(data.gateway)).
				Return(nil)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(nil)

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("removes desired OCI listeners when ownership annotation is missing", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			listenerName := gatewayv1.SectionName("https-" + fake.Lorem().Word())
			unrelatedListenerName := "manual-" + fake.Lorem().Word()
			routingPolicyName := listenerPolicyName(string(listenerName))
			gateway := newRandomGateway(randomGatewayWithListenersOpt(gatewayv1.Listener{
				Name:     listenerName,
				Protocol: gatewayv1.HTTPSProtocolType,
				Port:     443,
			}))
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			gateway.Annotations = map[string]string{
				LoadBalancerGatewayIDAnnotation: loadBalancerID,
			}
			data := &resolvedGatewayDetails{
				gateway: *gateway,
				config: types.GatewayConfig{
					Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID},
				},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)

			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						Listeners: map[string]loadbalancer.Listener{
							string(listenerName): {
								Name:                  new(string(listenerName)),
								RoutingPolicyName:     new(routingPolicyName),
								DefaultBackendSetName: new(gatewayDefaultBackendSetName(data.gateway)),
							},
							unrelatedListenerName: {
								Name:                  new(unrelatedListenerName),
								DefaultBackendSetName: new("other-" + fake.Lorem().Word()),
							},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							routingPolicyName: {Name: new(routingPolicyName)},
						},
						Certificates: map[string]loadbalancer.Certificate{},
					},
				}, nil)

			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().
				removeMissingListeners(t.Context(), mock.MatchedBy(func(params removeMissingListenersParams) bool {
					_, hasOwned := params.knownListeners[string(listenerName)]
					_, hasUnrelated := params.knownListeners[unrelatedListenerName]
					return params.loadBalancerID == loadBalancerID &&
						hasOwned &&
						hasUnrelated &&
						assert.ObjectsAreEqual(
							map[string]struct{}{string(listenerName): {}},
							params.cleanupListenerNames,
						)
				})).
				Return(nil)
			mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, gatewayDefaultBackendSetName(data.gateway)).
				Return(nil)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(nil)

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("removes ListenerSet derived OCI listeners", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			gateway := newRandomGateway()
			gateway.Namespace = "infra-" + fake.Lorem().Word()
			gateway.Name = "edge-" + fake.Lorem().Word()
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + fake.Lorem().Word(),
					Name:      "media-" + fake.Lorem().Word(),
				},
			}
			listener := gatewayv1.Listener{Name: "https", Protocol: gatewayv1.HTTPSProtocolType, Port: 443}
			ownedListenerName := listenerSetOCIListenerName(*gateway, listenerSet, listener)
			gateway.Annotations = map[string]string{
				LoadBalancerGatewayProgrammedListenersAnnotation: ownedListenerName,
			}
			routingPolicyName := listenerPolicyName(ownedListenerName)
			otherGateway := *gateway.DeepCopy()
			otherGateway.Name = "other-" + fake.Lorem().Word()
			otherGatewayOwnedListenerName := listenerSetOCIListenerName(otherGateway, listenerSet, listener)
			data := &resolvedGatewayDetails{
				gateway:      *gateway,
				listenerSets: []gatewayv1.ListenerSet{listenerSet},
				effectiveListeners: []effectiveListener{{
					sourceKind:      effectiveListenerSourceListenerSet,
					sourceNamespace: listenerSet.Namespace,
					sourceName:      listenerSet.Name,
					listener:        listener,
					ociName:         ownedListenerName,
				}},
				config: types.GatewayConfig{
					Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID},
				},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						Listeners: map[string]loadbalancer.Listener{
							ownedListenerName: {
								Name:                  new(ownedListenerName),
								RoutingPolicyName:     new(routingPolicyName),
								DefaultBackendSetName: new(gatewayDefaultBackendSetName(data.gateway)),
							},
							otherGatewayOwnedListenerName: {
								Name:                  new(otherGatewayOwnedListenerName),
								DefaultBackendSetName: new("other-" + fake.Lorem().Word()),
							},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							routingPolicyName: {Name: new(routingPolicyName)},
						},
						Certificates: map[string]loadbalancer.Certificate{},
					},
				}, nil)
			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().
				removeMissingListeners(t.Context(), mock.MatchedBy(func(params removeMissingListenersParams) bool {
					_, hasOwned := params.knownListeners[ownedListenerName]
					_, hasOtherGateway := params.knownListeners[otherGatewayOwnedListenerName]
					return hasOwned &&
						hasOtherGateway &&
						assert.ObjectsAreEqual(
							map[string]struct{}{ownedListenerName: {}},
							params.cleanupListenerNames,
						)
				})).
				Return(nil)
			mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, gatewayDefaultBackendSetName(data.gateway)).
				Return(nil)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(nil)

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})

		t.Run("removes ListenerSet listeners after ListenerSet is deleted", func(t *testing.T) {
			fake := faker.New()
			loadBalancerID := "ocid1.loadbalancer.oc1.." + fake.UUID().V4()
			gateway := newRandomGateway()
			gateway.Namespace = "infra-" + fake.Lorem().Word()
			gateway.Name = "edge-" + fake.Lorem().Word()
			gateway.Finalizers = []string{LoadBalancerGatewayProgrammedFinalizer}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps-" + fake.Lorem().Word(),
					Name:      "media-" + fake.Lorem().Word(),
				},
			}
			listener := gatewayv1.Listener{Name: "https", Protocol: gatewayv1.HTTPSProtocolType, Port: 443}
			vanishedListenerSetListenerName := listenerSetOCIListenerName(*gateway, listenerSet, listener)
			gateway.Annotations = map[string]string{
				LoadBalancerGatewayProgrammedListenersAnnotation: vanishedListenerSetListenerName,
			}
			routingPolicyName := listenerPolicyName(vanishedListenerSetListenerName)
			data := &resolvedGatewayDetails{
				gateway: *gateway,
				config: types.GatewayConfig{
					Spec: types.GatewayConfigSpec{LoadBalancerID: loadBalancerID},
				},
			}
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			mockOCIClient, _ := deps.OciClient.(*MockociLoadBalancerClient)
			mockOCIClient.EXPECT().
				GetLoadBalancer(t.Context(), loadbalancer.GetLoadBalancerRequest{LoadBalancerId: &loadBalancerID}).
				Return(loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						Listeners: map[string]loadbalancer.Listener{
							vanishedListenerSetListenerName: {
								Name:                  new(vanishedListenerSetListenerName),
								RoutingPolicyName:     new(routingPolicyName),
								DefaultBackendSetName: new(gatewayDefaultBackendSetName(data.gateway)),
							},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							routingPolicyName: {Name: new(routingPolicyName)},
						},
						Certificates: map[string]loadbalancer.Certificate{},
					},
				}, nil)
			mockLBModel, _ := deps.OciLoadBalancerModel.(*MockociLoadBalancerModel)
			mockLBModel.EXPECT().
				removeMissingListeners(t.Context(), mock.MatchedBy(func(params removeMissingListenersParams) bool {
					_, hasListenerSetListener := params.knownListeners[vanishedListenerSetListenerName]
					_, cleansListenerSetListener := params.cleanupListenerNames[vanishedListenerSetListenerName]
					return hasListenerSetListener &&
						cleansListenerSetListener
				})).
				Return(nil)
			mockLBModel.EXPECT().removeUnusedCertificates(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().cleanupFrontendMTLSCABundles(t.Context(), mock.Anything).Return(nil)
			mockLBModel.EXPECT().
				deprovisionBackendSetByName(t.Context(), loadBalancerID, gatewayDefaultBackendSetName(data.gateway)).
				Return(nil)
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.Gateway")).Return(nil)

			err := model.deprovisionGateway(t.Context(), data)

			require.NoError(t, err)
		})
	})

	t.Run("gatewayCleanupListenerNames", func(t *testing.T) {
		t.Run("returns desired and previously programmed listeners", func(t *testing.T) {
			fake := faker.New()
			desiredListenerName := gatewayv1.SectionName("desired-" + fake.Lorem().Word())
			previousListenerName := "previous-" + fake.Lorem().Word()
			gateway := newRandomGateway()
			gateway.Annotations = map[string]string{
				LoadBalancerGatewayProgrammedListenersAnnotation: previousListenerName,
			}

			result := gatewayCleanupListenerNames(*gateway, []gatewayv1.Listener{{
				Name: desiredListenerName,
			}})

			assert.Equal(t, map[string]struct{}{
				string(desiredListenerName): {},
				previousListenerName:        {},
			}, result)
		})

		t.Run("returns empty cleanup scope without desired or previously programmed listeners", func(t *testing.T) {
			gateway := newRandomGateway()

			result := gatewayCleanupListenerNames(*gateway, nil)

			require.Empty(t, result)
			require.NotNil(t, result)
		})

		t.Run("does not claim another gateway listener only because default backend set drifted", func(t *testing.T) {
			gateway := newRandomGateway()

			result := gatewayCleanupListenerNames(*gateway, nil)

			require.Empty(t, result)
			require.NotNil(t, result)
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
						LoadBalancerGatewayProgrammedListenersAnnotation: programmedGatewayListenersAnnotation(
							gatewayManagedOCIListenersForLoadBalancer(data),
						),
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
						LoadBalancerGatewayProgrammedListenersAnnotation: programmedGatewayListenersAnnotation(
							gatewayManagedOCIListenersForLoadBalancer(data),
						),
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
			expectedAnnotations[LoadBalancerGatewayProgrammedListenersAnnotation] =
				programmedGatewayListenersAnnotation(gatewayManagedOCIListenersForLoadBalancer(data))

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

		t.Run(
			"should check frontend mTLS dependency annotations when gateway references CA config maps",
			func(t *testing.T) {
				deps := newMockDeps(t)
				model := newGatewayModel(deps)
				fakeData := faker.New()

				configMapName := "ca-" + fakeData.Lorem().Word()
				configMapUID := apitypes.UID(fakeData.UUID().V4())
				configMapResourceVersion := fakeData.UUID().V4()
				gateway := newRandomGateway()
				gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
					Frontend: &gatewayv1.FrontendTLSConfig{
						Default: gatewayv1.TLSConfig{
							Validation: &gatewayv1.FrontendTLSValidation{
								CACertificateRefs: []gatewayv1.ObjectReference{{
									Group: "",
									Kind:  "ConfigMap",
									Name:  gatewayv1.ObjectName(configMapName),
								}},
							},
						},
					},
				}
				data := &resolvedGatewayDetails{
					gateway: *gateway,
					gatewayFrontendMTLSConfigMaps: map[string]corev1.ConfigMap{
						gateway.Namespace + "/" + configMapName: {
							ObjectMeta: metav1.ObjectMeta{
								Namespace:       gateway.Namespace,
								Name:            configMapName,
								UID:             configMapUID,
								ResourceVersion: configMapResourceVersion,
							},
						},
					},
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
							LoadBalancerGatewayProgrammedListenersAnnotation: programmedGatewayListenersAnnotation(
								gatewayManagedOCIListenersForLoadBalancer(data),
							),
							GatewayFrontendMTLSConfigMapsAnnotation: gateway.Namespace + "/" + configMapName +
								"=" + string(configMapUID) + "/" + configMapResourceVersion,
						},
					},
				).Return(false)

				result := model.isProgrammed(t.Context(), data)

				require.False(t, result)
				mockResourcesModel.AssertExpectations(t)
			},
		)

		t.Run(
			"should check frontend mTLS ReferenceGrant annotations for cross namespace CA config maps",
			func(t *testing.T) {
				deps := newMockDeps(t)
				model := newGatewayModel(deps)
				fakeData := faker.New()

				configMapNamespace := "ca-" + fakeData.Lorem().Word()
				configMapName := "ca-" + fakeData.Lorem().Word()
				configMapNamespaceRef := gatewayv1.Namespace(configMapNamespace)
				configMapUID := apitypes.UID(fakeData.UUID().V4())
				configMapResourceVersion := fakeData.UUID().V4()
				grantName := "allow-" + fakeData.Lorem().Word()
				grantUID := apitypes.UID(fakeData.UUID().V4())
				grantResourceVersion := fakeData.UUID().V4()
				gateway := newRandomGateway()
				gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
					Frontend: &gatewayv1.FrontendTLSConfig{
						Default: gatewayv1.TLSConfig{
							Validation: &gatewayv1.FrontendTLSValidation{
								CACertificateRefs: []gatewayv1.ObjectReference{{
									Group:     "",
									Kind:      "ConfigMap",
									Name:      gatewayv1.ObjectName(configMapName),
									Namespace: &configMapNamespaceRef,
								}},
							},
						},
					},
				}
				data := &resolvedGatewayDetails{
					gateway: *gateway,
					gatewayFrontendMTLSConfigMaps: map[string]corev1.ConfigMap{
						configMapNamespace + "/" + configMapName: {
							ObjectMeta: metav1.ObjectMeta{
								Namespace:       configMapNamespace,
								Name:            configMapName,
								UID:             configMapUID,
								ResourceVersion: configMapResourceVersion,
							},
						},
					},
					gatewayFrontendMTLSReferenceGrants: map[string]gatewayv1beta1.ReferenceGrant{
						configMapNamespace + "/" + grantName: {
							ObjectMeta: metav1.ObjectMeta{
								Namespace:       configMapNamespace,
								Name:            grantName,
								UID:             grantUID,
								ResourceVersion: grantResourceVersion,
							},
						},
					},
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
							LoadBalancerGatewayProgrammedListenersAnnotation: programmedGatewayListenersAnnotation(
								gatewayManagedOCIListenersForLoadBalancer(data),
							),
							GatewayFrontendMTLSConfigMapsAnnotation: configMapNamespace + "/" + configMapName +
								"=" + string(configMapUID) + "/" + configMapResourceVersion,
							GatewayFrontendMTLSReferenceGrantsAnnotation: configMapNamespace + "/" + grantName +
								"=" + string(grantUID) + "/" + grantResourceVersion,
						},
					},
				).Return(false)

				result := model.isProgrammed(t.Context(), data)

				require.False(t, result)
				mockResourcesModel.AssertExpectations(t)
			},
		)
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

		t.Run("unindexed list returns namespace lookup errors", func(t *testing.T) {
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
				List(t.Context(), &gatewayv1.ListenerSetList{}).
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

			err := populateAttachedListenerSetsUnindexed(t.Context(), model.client, data)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to get ListenerSet namespace")
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
				nil,
			)

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			expectEmptyListenerSetRouteCountLists(t, mockClient, len(data.listenerSets))
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
			expectEmptyListenerSetRouteCountLists(t, mockClient, len(data.listenerSets))
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

		t.Run("returns attached route count errors", func(t *testing.T) {
			deps := newMockDeps(t)
			wantErr := errors.New("list failed")
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     "http",
						Port:     80,
						Protocol: gatewayv1.HTTPProtocolType,
					}},
				},
			}
			gateway := gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"},
			}
			effectiveListeners := effectiveListenersForGateway(gateway, []gatewayv1.ListenerSet{listenerSet})
			data := &resolvedGatewayDetails{
				gateway:            gateway,
				listenerSets:       []gatewayv1.ListenerSet{listenerSet},
				effectiveListeners: effectiveListeners,
			}

			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().List(t.Context(), mock.Anything).Return(wantErr)

			err := setListenerSetsProgrammed(
				t.Context(),
				mockClient,
				data,
				gatewayv1.GatewayController(ControllerClassName),
			)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("counts accepted direct ListenerSet route parents", func(t *testing.T) {
			httpListener := gatewayv1.SectionName("http")
			grpcListener := gatewayv1.SectionName("grpc")
			tcpListener := gatewayv1.SectionName("tcp")
			udpListener := gatewayv1.SectionName("udp")
			tlsListener := gatewayv1.SectionName("tls")
			listenerSetKind := gatewayv1.Kind("ListenerSet")
			httpParentRef := gatewayv1.ParentReference{
				Kind:        &listenerSetKind,
				Name:        "extra",
				SectionName: &httpListener,
			}
			grpcParentRef := httpParentRef
			grpcParentRef.SectionName = &grpcListener
			tcpParentRef := httpParentRef
			tcpParentRef.SectionName = &tcpListener
			udpParentRef := httpParentRef
			udpParentRef.SectionName = &udpListener
			tlsParentRef := httpParentRef
			tlsParentRef.SectionName = &tlsListener
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: string(httpParentRef.Name)},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
					Listeners: []gatewayv1.ListenerEntry{
						{Name: httpListener, Port: 8080, Protocol: gatewayv1.HTTPProtocolType},
						{Name: grpcListener, Port: 8081, Protocol: gatewayv1.HTTPSProtocolType},
						{Name: tcpListener, Port: 8082, Protocol: gatewayv1.TCPProtocolType},
						{Name: udpListener, Port: 8083, Protocol: gatewayv1.UDPProtocolType},
						{Name: tlsListener, Port: 8084, Protocol: gatewayv1.TLSProtocolType},
					},
				},
			}
			acceptedParentStatus := func(parentRef gatewayv1.ParentReference) []gatewayv1.RouteParentStatus {
				return []gatewayv1.RouteParentStatus{{
					ParentRef:      parentRef,
					ControllerName: gatewayv1.GatewayController(ControllerClassName),
					Conditions: []metav1.Condition{{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonAccepted),
						ObservedGeneration: 4,
					}},
				}}
			}
			route := gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "api", Generation: 4},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{httpParentRef}},
				},
				Status: gatewayv1.HTTPRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{Parents: acceptedParentStatus(httpParentRef)},
				},
			}
			grpcRoute := gatewayv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "grpc", Generation: 4},
				Spec: gatewayv1.GRPCRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{grpcParentRef}},
				},
				Status: gatewayv1.GRPCRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{Parents: acceptedParentStatus(grpcParentRef)},
				},
			}
			tcpRoute := gatewayv1.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "tcp", Generation: 4},
				Spec: gatewayv1.TCPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{tcpParentRef}},
				},
				Status: gatewayv1.TCPRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{Parents: acceptedParentStatus(tcpParentRef)},
				},
			}
			udpRoute := gatewayv1.UDPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "udp", Generation: 4},
				Spec: gatewayv1.UDPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{udpParentRef}},
				},
				Status: gatewayv1.UDPRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{Parents: acceptedParentStatus(udpParentRef)},
				},
			}
			tlsRoute := gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "tls", Generation: 4},
				Spec: gatewayv1.TLSRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{tlsParentRef}},
				},
				Status: gatewayv1.TLSRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{Parents: acceptedParentStatus(tlsParentRef)},
				},
			}
			data := &resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
				gatewayClass: gatewayv1.GatewayClass{
					Spec: gatewayv1.GatewayClassSpec{ControllerName: gatewayv1.GatewayController(ControllerClassName)},
				},
				listenerSets: []gatewayv1.ListenerSet{listenerSet},
			}
			data.effectiveListeners = effectiveListenersForGateway(data.gateway, data.listenerSets)
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(&route, &grpcRoute, &tcpRoute, &udpRoute, &tlsRoute).
				Build()

			counts, err := listenerSetAttachedRouteCounts(t.Context(), k8sClient, data, listenerSet)

			require.NoError(t, err)
			assert.Equal(t, int32(1), counts[httpListener])
			assert.Equal(t, int32(1), counts[grpcListener])
			assert.Equal(t, int32(1), counts[tcpListener])
			assert.Equal(t, int32(1), counts[udpListener])
			assert.Equal(t, int32(1), counts[tlsListener])
		})

		t.Run("returns attached route count list errors", func(t *testing.T) {
			wantErr := errors.New("list failed")
			data := &resolvedGatewayDetails{}
			listenerSet := gatewayv1.ListenerSet{}
			testCases := []struct {
				name string
				run  func(context.Context, k8sClient, *resolvedGatewayDetails, gatewayv1.ListenerSet) error
			}{
				{
					name: "HTTPRoute",
					run: func(ctx context.Context,
						k8sClient k8sClient,
						data *resolvedGatewayDetails,
						listenerSet gatewayv1.ListenerSet,
					) error {
						counts := map[gatewayv1.SectionName]int32{}
						return addListenerSetHTTPRouteCounts(ctx, k8sClient, data, listenerSet, counts)
					},
				},
				{
					name: "GRPCRoute",
					run: func(ctx context.Context,
						k8sClient k8sClient,
						data *resolvedGatewayDetails,
						listenerSet gatewayv1.ListenerSet,
					) error {
						counts := map[gatewayv1.SectionName]int32{}
						return addListenerSetGRPCRouteCounts(ctx, k8sClient, data, listenerSet, counts)
					},
				},
				{
					name: "TCPRoute",
					run: func(ctx context.Context,
						k8sClient k8sClient,
						data *resolvedGatewayDetails,
						listenerSet gatewayv1.ListenerSet,
					) error {
						counts := map[gatewayv1.SectionName]int32{}
						return addListenerSetTCPRouteCounts(ctx, k8sClient, data, listenerSet, counts)
					},
				},
				{
					name: "UDPRoute",
					run: func(ctx context.Context,
						k8sClient k8sClient,
						data *resolvedGatewayDetails,
						listenerSet gatewayv1.ListenerSet,
					) error {
						counts := map[gatewayv1.SectionName]int32{}
						return addListenerSetUDPRouteCounts(ctx, k8sClient, data, listenerSet, counts)
					},
				},
				{
					name: "TLSRoute",
					run: func(ctx context.Context,
						k8sClient k8sClient,
						data *resolvedGatewayDetails,
						listenerSet gatewayv1.ListenerSet,
					) error {
						counts := map[gatewayv1.SectionName]int32{}
						return addListenerSetTLSRouteCounts(ctx, k8sClient, data, listenerSet, counts)
					},
				},
			}

			for _, tc := range testCases {
				t.Run(tc.name, func(t *testing.T) {
					mockClient := NewMockk8sClient(t)
					mockClient.EXPECT().List(t.Context(), mock.Anything).Return(wantErr)

					err := tc.run(t.Context(), mockClient, data, listenerSet)

					require.ErrorIs(t, err, wantErr)
				})
			}
		})

		t.Run("listenerSetAttachedRouteCounts returns staged list errors", func(t *testing.T) {
			wantErr := errors.New("list failed")
			data := &resolvedGatewayDetails{}
			listenerSet := gatewayv1.ListenerSet{}
			for _, failAtCall := range []int{1, 2, 3, 4, 5} {
				t.Run(fmt.Sprintf("call %d", failAtCall), func(t *testing.T) {
					mockClient := NewMockk8sClient(t)
					call := 0
					mockClient.EXPECT().
						List(t.Context(), mock.Anything).
						RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
							call++
							if call == failAtCall {
								return wantErr
							}
							reflect.ValueOf(list).Elem().Set(reflect.Zero(reflect.ValueOf(list).Elem().Type()))
							return nil
						}).
						Times(failAtCall)

					counts, err := listenerSetAttachedRouteCounts(t.Context(), mockClient, data, listenerSet)

					require.ErrorIs(t, err, wantErr)
					assert.Nil(t, counts)
				})
			}
		})

		t.Run("ignores attached route counts for nonmatching parent refs", func(t *testing.T) {
			listenerSetKind := gatewayv1.Kind("ListenerSet")
			listenerName := gatewayv1.SectionName("http")
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     listenerName,
						Port:     80,
						Protocol: gatewayv1.HTTPProtocolType,
					}},
				},
			}
			data := &resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
				effectiveListeners: effectiveListenersForGateway(
					gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
					[]gatewayv1.ListenerSet{listenerSet},
				),
			}
			counts := map[gatewayv1.SectionName]int32{}
			statusParentRef := gatewayv1.ParentReference{Kind: &listenerSetKind, Name: "extra"}
			otherParentRef := gatewayv1.ParentReference{Kind: &listenerSetKind, Name: "other"}

			addListenerSetRouteCountForParentRef(
				data,
				listenerSet,
				counts,
				"apps",
				statusParentRef,
				otherParentRef,
				func(gatewayv1.ParentReference, gatewayv1.Listener) bool { return true },
			)

			assert.Zero(t, counts[listenerName])
			_, found := listenerSetEntryForEffectiveListener(data.gateway, listenerSet, "missing")
			assert.False(t, found)

			addListenerSetRouteCounts(
				data,
				listenerSet,
				counts,
				"apps",
				[]gatewayv1.RouteParentStatus{{ParentRef: statusParentRef}},
				[]gatewayv1.ParentReference{statusParentRef},
				func(gatewayv1.ParentReference, gatewayv1.Listener) bool { return true },
			)
			assert.Zero(t, counts[listenerName])
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
		expectEmptyListenerSetRouteCountLists(t, mockClient, len(data.listenerSets))
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr).Once()

		err := model.setProgrammed(t.Context(), data)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("populateGatewayFrontendMTLSDependencies", func(t *testing.T) {
		t.Run("initializes empty dependency maps when gateway has no frontend mTLS refs", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			data := &resolvedGatewayDetails{gateway: *newRandomGateway()}

			err := model.populateGatewayFrontendMTLSDependencies(t.Context(), data)

			require.NoError(t, err)
			assert.Empty(t, data.gatewayFrontendMTLSConfigMaps)
			assert.Empty(t, data.gatewayFrontendMTLSReferenceGrants)
		})

		t.Run("collects local and cross namespace CA ConfigMaps and ReferenceGrants", func(t *testing.T) {
			fakeData := faker.New()
			gateway := newRandomGateway()
			localRefName := gatewayv1.ObjectName("local-" + fakeData.Lorem().Word())
			crossNamespace := "security-" + fakeData.Lorem().Word()
			crossNamespaceRef := gatewayv1.Namespace(crossNamespace)
			crossRefName := gatewayv1.ObjectName("cross-" + fakeData.Lorem().Word())
			perPortRefName := gatewayv1.ObjectName("per-port-" + fakeData.Lorem().Word())
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{
						Validation: &gatewayv1.FrontendTLSValidation{
							CACertificateRefs: []gatewayv1.ObjectReference{
								{Group: "", Kind: "ConfigMap", Name: localRefName},
								{Group: "", Kind: "ConfigMap", Name: crossRefName, Namespace: &crossNamespaceRef},
								{Group: "example.com", Kind: "Other", Name: "ignored"},
							},
						},
					},
					PerPort: []gatewayv1.TLSPortConfig{{
						Port: 8443,
						TLS: gatewayv1.TLSConfig{
							Validation: &gatewayv1.FrontendTLSValidation{
								CACertificateRefs: []gatewayv1.ObjectReference{
									{Group: "", Kind: "ConfigMap", Name: perPortRefName, Namespace: &crossNamespaceRef},
								},
							},
						},
					}},
				},
			}
			localConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: gateway.Namespace,
					Name:      string(localRefName),
				},
			}
			crossConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: crossNamespace,
					Name:      string(crossRefName),
				},
			}
			perPortConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: crossNamespace,
					Name:      string(perPortRefName),
				},
			}
			grantName := "allow-" + fakeData.Lorem().Word()
			grantToName := crossRefName
			grant := gatewayv1beta1.ReferenceGrant{
				ObjectMeta: metav1.ObjectMeta{Namespace: crossNamespace, Name: grantName},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1.Group(gatewayAPIGroup),
						Kind:      gatewayv1.Kind("Gateway"),
						Namespace: gatewayv1.Namespace(gateway.Namespace),
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{
						Group: "",
						Kind:  gatewayv1.Kind("ConfigMap"),
						Name:  &grantToName,
					}},
				},
			}
			irrelevantGrant := *grant.DeepCopy()
			irrelevantGrant.Name = "ignore-" + fakeData.Lorem().Word()
			irrelevantGrant.Spec.From[0].Namespace = gatewayv1.Namespace("other-" + fakeData.Lorem().Word())
			k8sClient := fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(&localConfigMap, &crossConfigMap, &perPortConfigMap, &grant, &irrelevantGrant).
				Build()
			model := newGatewayModel(gatewayModelDeps{
				K8sClient:            k8sClient,
				ResourcesModel:       NewMockresourcesModel(t),
				RootLogger:           diag.RootTestLogger(),
				OciClient:            NewMockociLoadBalancerClient(t),
				OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
			})
			data := &resolvedGatewayDetails{gateway: *gateway}

			err := model.populateGatewayFrontendMTLSDependencies(t.Context(), data)

			require.NoError(t, err)
			assert.Contains(t, data.gatewayFrontendMTLSConfigMaps, gateway.Namespace+"/"+string(localRefName))
			assert.Contains(t, data.gatewayFrontendMTLSConfigMaps, crossNamespace+"/"+string(crossRefName))
			assert.Contains(t, data.gatewayFrontendMTLSConfigMaps, crossNamespace+"/"+string(perPortRefName))
			assert.Contains(t, data.gatewayFrontendMTLSReferenceGrants, crossNamespace+"/"+grantName)
			assert.NotContains(t, data.gatewayFrontendMTLSReferenceGrants, crossNamespace+"/"+irrelevantGrant.Name)

			refs := frontendMTLSConfigMapRefs(*gateway)
			assert.ElementsMatch(t, []apitypes.NamespacedName{
				{Namespace: gateway.Namespace, Name: string(localRefName)},
				{Namespace: crossNamespace, Name: string(crossRefName)},
				{Namespace: crossNamespace, Name: string(perPortRefName)},
			}, refs)
			assert.True(t, gatewayHasCrossNamespaceFrontendMTLSConfigMapRefs(*gateway))
		})

		t.Run("tracks missing ConfigMaps by omitting their revision", func(t *testing.T) {
			fakeData := faker.New()
			gateway := newRandomGateway()
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{
						Validation: &gatewayv1.FrontendTLSValidation{
							CACertificateRefs: []gatewayv1.ObjectReference{{
								Group: "",
								Kind:  "ConfigMap",
								Name:  gatewayv1.ObjectName("missing-" + fakeData.Lorem().Word()),
							}},
						},
					},
				},
			}
			k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
			model := newGatewayModel(gatewayModelDeps{
				K8sClient:            k8sClient,
				ResourcesModel:       NewMockresourcesModel(t),
				RootLogger:           diag.RootTestLogger(),
				OciClient:            NewMockociLoadBalancerClient(t),
				OciLoadBalancerModel: NewMockociLoadBalancerModel(t),
			})
			data := &resolvedGatewayDetails{gateway: *gateway}

			err := model.populateGatewayFrontendMTLSDependencies(t.Context(), data)

			require.NoError(t, err)
			assert.Empty(t, data.gatewayFrontendMTLSConfigMaps)
			assert.Empty(t, data.gatewayFrontendMTLSReferenceGrants)
		})

		t.Run("returns ConfigMap lookup errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			fakeData := faker.New()
			refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
			gateway := newRandomGateway()
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{
						Validation: &gatewayv1.FrontendTLSValidation{
							CACertificateRefs: []gatewayv1.ObjectReference{{
								Group: "",
								Kind:  "ConfigMap",
								Name:  refName,
							}},
						},
					},
				},
			}
			wantErr := errors.New("get failed")
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				Get(t.Context(), apitypes.NamespacedName{
					Namespace: gateway.Namespace,
					Name:      string(refName),
				}, mock.AnythingOfType("*v1.ConfigMap")).
				Return(wantErr)

			err := model.populateGatewayFrontendMTLSDependencies(
				t.Context(),
				&resolvedGatewayDetails{gateway: *gateway},
			)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to get frontend mTLS ConfigMap")
		})

		t.Run("returns ReferenceGrant list errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGatewayModel(deps)
			fakeData := faker.New()
			refNamespace := "security-" + fakeData.Lorem().Word()
			refNamespaceValue := gatewayv1.Namespace(refNamespace)
			refName := gatewayv1.ObjectName("ca-" + fakeData.Lorem().Word())
			gateway := newRandomGateway()
			gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
				Frontend: &gatewayv1.FrontendTLSConfig{
					Default: gatewayv1.TLSConfig{
						Validation: &gatewayv1.FrontendTLSValidation{
							CACertificateRefs: []gatewayv1.ObjectReference{{
								Group:     "",
								Kind:      "ConfigMap",
								Name:      refName,
								Namespace: &refNamespaceValue,
							}},
						},
					},
				},
			}
			wantErr := errors.New("list failed")
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			setupClientGet(t, mockClient, apitypes.NamespacedName{
				Namespace: refNamespace,
				Name:      string(refName),
			}, corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: refNamespace, Name: string(refName)}})
			mockClient.EXPECT().
				List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
				Return(wantErr)

			err := model.populateGatewayFrontendMTLSDependencies(
				t.Context(),
				&resolvedGatewayDetails{gateway: *gateway},
			)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to list frontend mTLS ReferenceGrants")
		})
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
		"populateGatewaySecrets isolates cross namespace ListenerSet certificate without ReferenceGrant",
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

			require.NoError(t, err)
			require.Len(t, data.effectiveListeners, 1)
			assert.True(t, data.effectiveListeners[0].unsupported)
			assert.Equal(t, gatewayv1.ListenerReasonRefNotPermitted, data.effectiveListeners[0].unsupportedReason)
			assert.Contains(
				t,
				data.effectiveListeners[0].unsupportedMessage,
				"certificateRef certs/tls-cert is not permitted by a ReferenceGrant",
			)
		},
	)

	t.Run("populateGatewaySecrets returns Gateway effective listener secret errors", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newGatewayModel(deps)
		data := &resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "edge"}},
			effectiveListeners: []effectiveListener{{
				sourceKind:      effectiveListenerSourceGateway,
				sourceNamespace: "infra",
				listener: gatewayv1.Listener{
					Name:     "https",
					Protocol: gatewayv1.HTTPSProtocolType,
					TLS: &gatewayv1.ListenerTLSConfig{
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: "tls-cert"}},
					},
				},
			}},
		}
		wantErr := errors.New("secret lookup failed")
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "infra", Name: "tls-cert"}, mock.Anything).
			Return(wantErr)

		err := model.populateGatewaySecrets(t.Context(), data)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("populateGatewayListenerSecrets returns ReferenceGrant lookup errors", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newGatewayModel(deps)
		certNamespace := gatewayv1.Namespace("certs")
		wantErr := errors.New("referencegrant list failed")
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			Return(wantErr)

		err := model.populateGatewayListenerSecrets(
			t.Context(),
			&resolvedGatewayDetails{},
			gatewayv1.Kind(effectiveListenerSourceListenerSet),
			"apps",
			gatewayv1.Listener{
				Name:     "https",
				Protocol: gatewayv1.HTTPSProtocolType,
				TLS: &gatewayv1.ListenerTLSConfig{
					CertificateRefs: []gatewayv1.SecretObjectReference{{
						Namespace: &certNamespace,
						Name:      "tls-cert",
					}},
				},
			},
		)

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("markListenerSetSecretError only isolates ListenerSet listener errors", func(t *testing.T) {
		gatewayListener := effectiveListener{sourceKind: effectiveListenerSourceGateway}
		assert.False(t, markListenerSetSecretError(&gatewayListener, errors.New("gateway failed")))
		assert.False(t, gatewayListener.unsupported)

		listenerSetListener := effectiveListener{sourceKind: effectiveListenerSourceListenerSet}
		handled := markListenerSetSecretError(&listenerSetListener, &resourceStatusError{
			reason:  string(gatewayv1.GatewayReasonInvalidParameters),
			message: "certificateRef certs/tls-cert is not permitted by a ReferenceGrant",
		})

		assert.True(t, handled)
		assert.True(t, listenerSetListener.unsupported)
		assert.Equal(t, gatewayv1.ListenerReasonRefNotPermitted, listenerSetListener.unsupportedReason)
		assert.Contains(t, listenerSetListener.unsupportedMessage, "not permitted")
	})
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

func TestGatewayFrontendMTLSListenerFiltering(t *testing.T) {
	fake := faker.New()
	plainHTTPListener := makeRandomListener(func(listener *gatewayv1.Listener) {
		listener.Protocol = gatewayv1.HTTPProtocolType
		listener.TLS = nil
	})
	httpsListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
	ociCAListener := makeRandomListener(randomListenerWithHTTPSParamsOpt())
	gateway := newRandomGateway()
	gateway.Spec.Listeners = []gatewayv1.Listener{plainHTTPListener, httpsListener, ociCAListener}
	gateway.Annotations = map[string]string{}
	gateway.Spec.TLS = &gatewayv1.GatewayTLSConfig{
		Frontend: &gatewayv1.FrontendTLSConfig{
			PerPort: []gatewayv1.TLSPortConfig{{
				Port: httpsListener.Port,
				TLS: gatewayv1.TLSConfig{Validation: &gatewayv1.FrontendTLSValidation{
					CACertificateRefs: []gatewayv1.ObjectReference{{
						Name: gatewayv1.ObjectName("ca-" + fake.Lorem().Word()),
					}},
				}},
			}},
		},
	}
	gateway.Annotations[frontendMTLSPortTrustedCABundleOCIDsAnnotation(ociCAListener.Port)] =
		"ocid1.cabundle.oc1.." + fake.UUID().V4()

	assert.False(t, listenerUsesFrontendMTLS(*gateway, plainHTTPListener))
	assert.True(t, listenerUsesFrontendMTLS(*gateway, httpsListener))
	assert.True(t, listenerUsesFrontendMTLS(*gateway, ociCAListener))
	assert.Equal(t, []gatewayv1.Listener{plainHTTPListener}, gatewayListenersWithoutFrontendMTLS(
		*gateway,
		[]gatewayv1.Listener{plainHTTPListener, httpsListener, ociCAListener},
	))
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
