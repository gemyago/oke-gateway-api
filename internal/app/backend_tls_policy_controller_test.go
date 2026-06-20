package app

import (
	"context"
	"errors"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type stubBackendTLSPolicyModel struct {
	cleanupPolicy *gatewayv1.BackendTLSPolicy
	cleanupErr    error
	resolveErr    error
	resolveFunc   func(resolveBackendTLSPolicyParams) (*loadbalancer.SslConfigurationDetails, error)
}

func (s *stubBackendTLSPolicyModel) resolveForBackendRef(
	_ context.Context,
	params resolveBackendTLSPolicyParams,
) (*loadbalancer.SslConfigurationDetails, error) {
	if s.resolveFunc != nil {
		return s.resolveFunc(params)
	}
	return nil, s.resolveErr
}

func (s *stubBackendTLSPolicyModel) cleanupDeletingPolicy(
	_ context.Context,
	policy gatewayv1.BackendTLSPolicy,
) error {
	s.cleanupPolicy = &policy
	return s.cleanupErr
}

func TestBackendTLSPolicyController(t *testing.T) {
	t.Run("constructs controller", func(t *testing.T) {
		model := &stubBackendTLSPolicyModel{}

		controller := NewBackendTLSPolicyController(BackendTLSPolicyControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
			Model:      model,
		})

		require.NotNil(t, controller)
		assert.Same(t, model, controller.model)
	})

	t.Run("ignores missing policies", func(t *testing.T) {
		controller := NewBackendTLSPolicyController(BackendTLSPolicyControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
			Model:      &stubBackendTLSPolicyModel{},
		})

		result, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "default", Name: "missing"},
		})

		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns get errors", func(t *testing.T) {
		wantErr := apierrors.NewForbidden(
			schema.GroupResource{Group: gatewayv1.GroupName, Resource: "backendtlspolicies"},
			"blocked",
			errors.New("denied"),
		)
		controller := NewBackendTLSPolicyController(BackendTLSPolicyControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  &failingBackendTLSPolicyClient{err: wantErr},
			Model:      &stubBackendTLSPolicyModel{},
		})

		_, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "default", Name: "blocked"},
		})

		require.ErrorIs(t, err, wantErr)
		assert.ErrorContains(t, err, "failed to get BackendTLSPolicy")
	})

	t.Run("does not cleanup active policies", func(t *testing.T) {
		policy := gatewayv1.BackendTLSPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "active"}}
		model := &stubBackendTLSPolicyModel{}
		controller := NewBackendTLSPolicyController(BackendTLSPolicyControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).WithObjects(&policy).Build(),
			Model:      model,
		})

		_, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "default", Name: "active"},
		})

		require.NoError(t, err)
		assert.Nil(t, model.cleanupPolicy)
	})

	t.Run("cleans up deleting policies", func(t *testing.T) {
		now := metav1.Now()
		policy := gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "default",
				Name:              "deleting",
				Finalizers:        []string{BackendTLSPolicyProgrammedFinalizer},
				DeletionTimestamp: &now,
			},
		}
		model := &stubBackendTLSPolicyModel{}
		controller := NewBackendTLSPolicyController(BackendTLSPolicyControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).WithObjects(&policy).Build(),
			Model:      model,
		})

		_, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "default", Name: "deleting"},
		})

		require.NoError(t, err)
		require.NotNil(t, model.cleanupPolicy)
		assert.Equal(t, "deleting", model.cleanupPolicy.Name)
	})

	t.Run("wraps cleanup errors", func(t *testing.T) {
		now := metav1.Now()
		wantErr := errors.New("cleanup failed")
		policy := gatewayv1.BackendTLSPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "default",
				Name:              "cleanup-error",
				Finalizers:        []string{BackendTLSPolicyProgrammedFinalizer},
				DeletionTimestamp: &now,
			},
		}
		controller := NewBackendTLSPolicyController(BackendTLSPolicyControllerDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).WithObjects(&policy).Build(),
			Model:      &stubBackendTLSPolicyModel{cleanupErr: wantErr},
		})

		_, err := controller.Reconcile(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "default", Name: "cleanup-error"},
		})

		require.ErrorIs(t, err, wantErr)
		assert.ErrorContains(t, err, "failed to cleanup BackendTLSPolicy")
	})
}

type failingBackendTLSPolicyClient struct {
	k8sClient

	err error
}

func (c *failingBackendTLSPolicyClient) Get(
	_ context.Context,
	_ client.ObjectKey,
	_ client.Object,
	_ ...client.GetOption,
) error {
	return c.err
}
