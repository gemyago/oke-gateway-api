package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

func TestReferenceGrantPolicy(t *testing.T) {
	t.Run("referenceGrantAllowsServiceBackend permits same namespace backend", func(t *testing.T) {
		allowed, err := referenceGrantAllowsServiceBackend(
			t.Context(),
			NewMockk8sClient(t),
			"TCPRoute",
			"iot",
			types.NamespacedName{Namespace: "iot", Name: "rtmp"},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("referenceGrantAllowsServiceBackend rejects cross namespace backend without grant", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{}))
				return nil
			})

		allowed, err := referenceGrantAllowsServiceBackend(
			t.Context(),
			mockClient,
			"TCPRoute",
			"routes",
			types.NamespacedName{Namespace: "backends", Name: "rtmp"},
		)

		require.NoError(t, err)
		assert.False(t, allowed)
	})

	t.Run("referenceGrantAllowsServiceBackend wraps grant list errors", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			Return(errors.New("list failed"))

		allowed, err := referenceGrantAllowsServiceBackend(
			t.Context(),
			mockClient,
			"TCPRoute",
			"routes",
			types.NamespacedName{Namespace: "backends", Name: "rtmp"},
		)

		require.ErrorContains(t, err, "failed to list ReferenceGrants")
		assert.False(t, allowed)
	})

	t.Run("referenceGrantAllowsServiceBackend ignores non matching grants", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{
						{
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{
									{
										Group:     gatewayv1.Group(gatewayAPIGroup),
										Kind:      gatewayv1.Kind("UDPRoute"),
										Namespace: gatewayv1.Namespace("routes"),
									},
								},
								To: []gatewayv1beta1.ReferenceGrantTo{
									{Group: gatewayv1.Group(""), Kind: gatewayv1.Kind(serviceKind)},
								},
							},
						},
						{
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{
									{
										Group:     gatewayv1.Group(gatewayAPIGroup),
										Kind:      gatewayv1.Kind("TCPRoute"),
										Namespace: gatewayv1.Namespace("routes"),
									},
								},
								To: []gatewayv1beta1.ReferenceGrantTo{
									{Group: gatewayv1.Group("apps"), Kind: gatewayv1.Kind("Deployment")},
									{
										Group: gatewayv1.Group(""),
										Kind:  gatewayv1.Kind(serviceKind),
										Name:  lo.ToPtr(gatewayv1.ObjectName("other")),
									},
								},
							},
						},
					},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsServiceBackend(
			t.Context(),
			mockClient,
			"TCPRoute",
			"routes",
			types.NamespacedName{Namespace: "backends", Name: "rtmp"},
		)

		require.NoError(t, err)
		assert.False(t, allowed)
	})

	t.Run("referenceGrantAllowsServiceBackend permits matching cross namespace backend grant", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{
						{
							ObjectMeta: metav1.ObjectMeta{
								Namespace: "backends",
								Name:      "allow-routes",
							},
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{
									{
										Group:     gatewayv1.Group(gatewayAPIGroup),
										Kind:      gatewayv1.Kind("TCPRoute"),
										Namespace: gatewayv1.Namespace("routes"),
									},
								},
								To: []gatewayv1beta1.ReferenceGrantTo{
									{
										Group: gatewayv1.Group(""),
										Kind:  gatewayv1.Kind(serviceKind),
										Name:  lo.ToPtr(gatewayv1.ObjectName("rtmp")),
									},
								},
							},
						},
					},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsServiceBackend(
			t.Context(),
			mockClient,
			"TCPRoute",
			"routes",
			types.NamespacedName{Namespace: "backends", Name: "rtmp"},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})
}
