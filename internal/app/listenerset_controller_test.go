package app

import (
	"context"
	"errors"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
)

func TestListenerSetController(t *testing.T) {
	makeController := func(objects ...runtime.Object) (*ListenerSetController, client.Client) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			WithStatusSubresource(&gatewayv1.ListenerSet{}).
			Build()
		return NewListenerSetController(ListenerSetControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
		}), k8sClient
	}
	makeRequest := func(listenerSet *gatewayv1.ListenerSet) reconcile.Request {
		return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(listenerSet)}
	}
	getStatus := func(t *testing.T, k8sClient client.Client, listenerSet *gatewayv1.ListenerSet) gatewayv1.ListenerSetStatus {
		t.Helper()
		var updated gatewayv1.ListenerSet
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(listenerSet), &updated))
		return updated.Status
	}
	assertAcceptedReason := func(
		t *testing.T,
		status gatewayv1.ListenerSetStatus,
		conditionStatus metav1.ConditionStatus,
		reason gatewayv1.ListenerSetConditionReason,
	) {
		t.Helper()
		accepted := meta.FindStatusCondition(status.Conditions, string(gatewayv1.ListenerSetConditionAccepted))
		require.NotNil(t, accepted)
		assert.Equal(t, conditionStatus, accepted.Status)
		assert.Equal(t, string(reason), accepted.Reason)
	}

	t.Run("ignores missing and deleting ListenerSets", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "apps",
				Name:      "deleting",
			},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		deletedAt := metav1.Now()
		listenerSet.DeletionTimestamp = &deletedAt
		listenerSet.Finalizers = []string{"example.com/finalizer"}
		controller, _ := makeController(listenerSet)

		missingResult, missingErr := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: "apps", Name: "missing"},
		})
		deletingResult, deletingErr := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, missingErr)
		require.NoError(t, deletingErr)
		assert.Zero(t, missingResult)
		assert.Zero(t, deletingResult)
	})

	t.Run("returns ListenerSet get errors", func(t *testing.T) {
		wantErr := errors.New("get failed")
		mockClient := NewMockk8sClient(t)
		req := reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "apps", Name: "extra"}}
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.ListenerSet")).
			Return(wantErr)
		controller := NewListenerSetController(ListenerSetControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		result, err := controller.Reconcile(t.Context(), req)

		require.ErrorIs(t, err, wantErr)
		assert.Zero(t, result)
	})

	t.Run("sets invalid status for non Gateway parent refs", func(t *testing.T) {
		parentKind := gatewayv1.Kind("Service")
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 2},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Kind: &parentKind, Name: "edge"},
			},
		}
		controller, k8sClient := makeController(listenerSet)

		result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
		status := getStatus(t, k8sClient, listenerSet)
		assertAcceptedReason(t, status, metav1.ConditionFalse, gatewayv1.ListenerSetReasonInvalid)
	})

	t.Run("sets parent not accepted status when parent Gateway is missing", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 3},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		controller, k8sClient := makeController(listenerSet)

		result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
		assertAcceptedReason(
			t,
			getStatus(t, k8sClient, listenerSet),
			metav1.ConditionFalse,
			gatewayv1.ListenerSetReasonParentNotAccepted,
		)
	})

	t.Run("returns parent Gateway get errors", func(t *testing.T) {
		wantErr := errors.New("gateway get failed")
		listenerSet := gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.ListenerSet) = listenerSet
				return nil
			})
		mockClient.EXPECT().
			Get(t.Context(), client.ObjectKey{Namespace: "apps", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
			Return(wantErr)
		controller := NewListenerSetController(ListenerSetControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		result, err := controller.Reconcile(t.Context(), makeRequest(&listenerSet))

		require.ErrorIs(t, err, wantErr)
		assert.Zero(t, result)
	})

	t.Run("sets not allowed status when Gateway does not opt in", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 4},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oci",
			},
		}
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oci"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		}
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}}
		controller, k8sClient := makeController(listenerSet, gateway, gatewayClass, namespace)

		result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
		assertAcceptedReason(
			t,
			getStatus(t, k8sClient, listenerSet),
			metav1.ConditionFalse,
			gatewayv1.ListenerSetReasonNotAllowed,
		)
	})

	t.Run("sets parent not accepted status for missing or unmanaged GatewayClass", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			gatewayClass *gatewayv1.GatewayClass
		}{
			{name: "missing GatewayClass"},
			{
				name: "unmanaged GatewayClass",
				gatewayClass: &gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "oci"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: gatewayv1.GatewayController("example.com/other"),
					},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				listenerSet := &gatewayv1.ListenerSet{
					ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 4},
					Spec: gatewayv1.ListenerSetSpec{
						ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
					},
				}
				gateway := &gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						GatewayClassName: "oci",
					},
				}
				objects := []runtime.Object{listenerSet, gateway}
				if tc.gatewayClass != nil {
					objects = append(objects, tc.gatewayClass)
				}
				controller, k8sClient := makeController(objects...)

				result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

				require.NoError(t, err)
				assert.Zero(t, result)
				assertAcceptedReason(
					t,
					getStatus(t, k8sClient, listenerSet),
					metav1.ConditionFalse,
					gatewayv1.ListenerSetReasonParentNotAccepted,
				)
			})
		}
	})

	t.Run("returns GatewayClass and namespace get errors", func(t *testing.T) {
		listenerSet := gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oci",
			},
		}
		gatewayClass := gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oci"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		}

		t.Run("GatewayClass get error", func(t *testing.T) {
			wantErr := errors.New("gatewayclass get failed")
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.ListenerSet) = listenerSet
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKey{Namespace: "apps", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.Gateway) = gateway
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKey{Name: "oci"}, mock.AnythingOfType("*v1.GatewayClass")).
				Return(wantErr)
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			result, err := controller.Reconcile(t.Context(), makeRequest(&listenerSet))

			require.ErrorIs(t, err, wantErr)
			assert.Zero(t, result)
		})

		t.Run("namespace get error", func(t *testing.T) {
			wantErr := errors.New("namespace get failed")
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.ListenerSet) = listenerSet
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKey{Namespace: "apps", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.Gateway) = gateway
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKey{Name: "oci"}, mock.AnythingOfType("*v1.GatewayClass")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.GatewayClass) = gatewayClass
					return nil
				})
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKey{Name: "apps"}, mock.AnythingOfType("*v1.Namespace")).
				Return(wantErr)
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			result, err := controller.Reconcile(t.Context(), makeRequest(&listenerSet))

			require.ErrorIs(t, err, wantErr)
			assert.Zero(t, result)
		})
	})

	t.Run("sets not allowed status when namespace is missing", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 4},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oci",
			},
		}
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oci"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		}
		controller, k8sClient := makeController(listenerSet, gateway, gatewayClass)

		result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
		assertAcceptedReason(
			t,
			getStatus(t, k8sClient, listenerSet),
			metav1.ConditionFalse,
			gatewayv1.ListenerSetReasonNotAllowed,
		)
	})

	t.Run("sets accepted and pending status for allowed ListenerSet", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 5},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
				Listeners: []gatewayv1.ListenerEntry{{
					Name:     "web",
					Port:     8080,
					Protocol: gatewayv1.HTTPProtocolType,
				}},
			},
		}
		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oci",
				AllowedListeners: &gatewayv1.AllowedListeners{
					Namespaces: &gatewayv1.ListenerNamespaces{From: lo.ToPtr(gatewayv1.NamespacesFromAll)},
				},
			},
		}
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "oci"},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		}
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}}
		controller, k8sClient := makeController(listenerSet, gateway, gatewayClass, namespace)

		result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
		status := getStatus(t, k8sClient, listenerSet)
		assert.True(t, meta.IsStatusConditionTrue(status.Conditions, string(gatewayv1.ListenerSetConditionAccepted)))
		programmed := meta.FindStatusCondition(status.Conditions, string(gatewayv1.ListenerSetConditionProgrammed))
		require.NotNil(t, programmed)
		assert.Equal(t, metav1.ConditionUnknown, programmed.Status)
		assert.Equal(t, string(gatewayv1.ListenerSetReasonPending), programmed.Reason)
		require.Len(t, status.Listeners, 1)
		assert.Equal(t, gatewayv1.SectionName("web"), status.Listeners[0].Name)
		listenerAccepted := meta.FindStatusCondition(
			status.Listeners[0].Conditions,
			string(gatewayv1.ListenerConditionAccepted),
		)
		require.NotNil(t, listenerAccepted)
		assert.Equal(t, metav1.ConditionTrue, listenerAccepted.Status)
		assert.Equal(t, string(gatewayv1.ListenerReasonAccepted), listenerAccepted.Reason)
		listenerProgrammed := meta.FindStatusCondition(
			status.Listeners[0].Conditions,
			string(gatewayv1.ListenerConditionProgrammed),
		)
		require.NotNil(t, listenerProgrammed)
		assert.Equal(t, metav1.ConditionUnknown, listenerProgrammed.Status)
		assert.Equal(t, string(gatewayv1.ListenerReasonPending), listenerProgrammed.Reason)
		assert.ElementsMatch(t, []gatewayv1.RouteGroupKind{{
			Group: lo.ToPtr(gatewayv1.Group(gatewayv1.GroupName)),
			Kind:  "HTTPRoute",
		}, {
			Group: lo.ToPtr(gatewayv1.Group(gatewayv1.GroupName)),
			Kind:  "GRPCRoute",
		}}, status.Listeners[0].SupportedKinds)

		var updated gatewayv1.ListenerSet
		require.NoError(t, k8sClient.Get(t.Context(), client.ObjectKeyFromObject(listenerSet), &updated))
		assert.Equal(t, "apps/edge", updated.Annotations[ListenerSetParentGatewayAnnotation])
	})

	t.Run("does not update semantically current status", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra", Generation: 5},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
			},
		}
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "edge"},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "oci",
				AllowedListeners: &gatewayv1.AllowedListeners{
					Namespaces: &gatewayv1.ListenerNamespaces{From: lo.ToPtr(gatewayv1.NamespacesFromAll)},
				},
			},
		}
		listenerSet.Status = pendingListenerSetStatus(*listenerSet, gateway)
		controller, _ := makeController(
			listenerSet,
			&gateway,
			&gatewayv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "oci"},
				Spec: gatewayv1.GatewayClassSpec{
					ControllerName: gatewayv1.GatewayController(ControllerClassName),
				},
			},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}},
		)

		result, err := controller.Reconcile(t.Context(), makeRequest(listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
	})

	t.Run("returns status update errors", func(t *testing.T) {
		wantErr := errors.New("status update failed")
		invalidKind := gatewayv1.Kind("Service")
		listenerSet := gatewayv1.ListenerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
			Spec: gatewayv1.ListenerSetSpec{
				ParentRef: gatewayv1.ParentGatewayReference{Kind: &invalidKind, Name: "edge"},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().
			Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
			RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.ListenerSet) = listenerSet
				return nil
			})
		mockClient.EXPECT().
			Status().
			Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.ListenerSet")).
			Return(wantErr)
		controller := NewListenerSetController(ListenerSetControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		result, err := controller.Reconcile(t.Context(), makeRequest(&listenerSet))

		require.ErrorIs(t, err, wantErr)
		assert.Zero(t, result)
	})

	t.Run("treats missing ListenerSet as not found", func(t *testing.T) {
		listenerSet := gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"}}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
			Return(apierrors.NewNotFound(schema.GroupResource{Resource: "listenersets"}, listenerSet.Name))
		controller := NewListenerSetController(ListenerSetControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		result, err := controller.Reconcile(t.Context(), makeRequest(&listenerSet))

		require.NoError(t, err)
		assert.Zero(t, result)
	})

	t.Run("updateParentGatewayAnnotation", func(t *testing.T) {
		t.Run("skips missing ListenerSet refresh", func(t *testing.T) {
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
				},
			}
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				Return(apierrors.NewNotFound(schema.GroupResource{Resource: "listenersets"}, listenerSet.Name))
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			err := controller.updateParentGatewayAnnotation(
				t.Context(),
				client.ObjectKeyFromObject(&listenerSet),
				listenerSet,
			)

			require.NoError(t, err)
		})

		t.Run("returns refresh errors", func(t *testing.T) {
			wantErr := errors.New("refresh failed")
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
				},
			}
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				Return(wantErr)
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			err := controller.updateParentGatewayAnnotation(
				t.Context(),
				client.ObjectKeyFromObject(&listenerSet),
				listenerSet,
			)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("skips current annotation", func(t *testing.T) {
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps",
					Name:      "extra",
					Annotations: map[string]string{
						ListenerSetParentGatewayAnnotation: "apps/edge",
					},
				},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
				},
			}
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.ListenerSet) = listenerSet
					return nil
				})
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			err := controller.updateParentGatewayAnnotation(
				t.Context(),
				client.ObjectKeyFromObject(&listenerSet),
				listenerSet,
			)

			require.NoError(t, err)
		})

		t.Run("removes stale annotation for invalid parent ref", func(t *testing.T) {
			invalidKind := gatewayv1.Kind("Service")
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "apps",
					Name:      "extra",
					Annotations: map[string]string{
						ListenerSetParentGatewayAnnotation: "apps/edge",
					},
				},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Kind: &invalidKind, Name: "edge"},
				},
			}
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.ListenerSet) = listenerSet
					return nil
				})
			mockClient.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					updated, ok := obj.(*gatewayv1.ListenerSet)
					return ok && updated.Annotations[ListenerSetParentGatewayAnnotation] == ""
				})).
				Return(nil)
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			err := controller.updateParentGatewayAnnotation(
				t.Context(),
				client.ObjectKeyFromObject(&listenerSet),
				listenerSet,
			)

			require.NoError(t, err)
		})

		t.Run("returns annotation update errors", func(t *testing.T) {
			wantErr := errors.New("update failed")
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "extra"},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{Name: "edge"},
				},
			}
			mockClient := NewMockk8sClient(t)
			mockClient.EXPECT().
				Get(t.Context(), client.ObjectKeyFromObject(&listenerSet), mock.AnythingOfType("*v1.ListenerSet")).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.ListenerSet) = listenerSet
					return nil
				})
			mockClient.EXPECT().
				Update(t.Context(), mock.AnythingOfType("*v1.ListenerSet")).
				Return(wantErr)
			controller := NewListenerSetController(ListenerSetControllerDeps{
				RootLogger: diag.RootTestLogger(),
				K8sClient:  mockClient,
			})

			err := controller.updateParentGatewayAnnotation(
				t.Context(),
				client.ObjectKeyFromObject(&listenerSet),
				listenerSet,
			)

			require.ErrorIs(t, err, wantErr)
		})
	})
}
