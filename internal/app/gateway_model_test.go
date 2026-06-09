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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
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

			removeCall := loadBalancerModel.EXPECT().
				removeMissingListeners(t.Context(), removeMissingListenersParams{
					loadBalancerID:   config.Spec.LoadBalancerID,
					knownListeners:   loadBalancer.Listeners,
					gatewayListeners: gateway.Spec.Listeners,
				}).
				Return(nil)

			loadBalancerModel.EXPECT().
				removeUnusedCertificates(t.Context(), removeUnusedCertificatesParams{
					loadBalancerID:       config.Spec.LoadBalancerID,
					listenerCertificates: certificatesByListener,
					knownCertificates:    loadBalancer.Certificates,
				}).
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
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
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
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
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
						GatewayProgrammingRevisionAnnotation: GatewayProgrammingRevisionValue,
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

		require.ErrorContains(t, err, "can only be used with HTTPS listeners")
	})

	t.Run("rejects certificate option on TLS listener", func(t *testing.T) {
		err := validateGatewayCertificateOptions(makeGateway(withOCICertificateOption(gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
			Port:     443,
		}, "ocid1.certificate.oc1..test")))

		require.ErrorContains(t, err, "can only be used with HTTPS listeners")
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
