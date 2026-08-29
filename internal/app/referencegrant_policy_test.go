package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jaswdr/faker/v2"
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

	t.Run("referenceGrantAllowsServiceBackend permits unnamed to target for matching kind", func(t *testing.T) {
		fake := faker.New()
		routeNamespace := "routes-" + fake.Lorem().Word()
		backendNamespace := "backends-" + fake.Lorem().Word()
		backendName := "rtmp-" + fake.Lorem().Word()
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: backendNamespace,
							Name:      "allow-routes-" + fake.Lorem().Word(),
						},
						Spec: gatewayv1beta1.ReferenceGrantSpec{
							From: []gatewayv1beta1.ReferenceGrantFrom{{
								Group:     gatewayv1.Group(gatewayAPIGroup),
								Kind:      gatewayv1.Kind("TCPRoute"),
								Namespace: gatewayv1.Namespace(routeNamespace),
							}},
							To: []gatewayv1beta1.ReferenceGrantTo{{
								Group: gatewayv1.Group(""),
								Kind:  gatewayv1.Kind(serviceKind),
							}},
						},
					}},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsServiceBackend(
			t.Context(),
			mockClient,
			"TCPRoute",
			routeNamespace,
			types.NamespacedName{Namespace: backendNamespace, Name: backendName},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("referenceGrantAllowsSecretRef permits same namespace secret", func(t *testing.T) {
		allowed, err := referenceGrantAllowsSecretRef(
			t.Context(),
			NewMockk8sClient(t),
			"ListenerSet",
			"apps",
			types.NamespacedName{Namespace: "apps", Name: "tls-cert"},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("referenceGrantAllowsSecretRef permits matching cross namespace secret grant", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{{
						ObjectMeta: metav1.ObjectMeta{Namespace: "certs", Name: "allow-listenersets"},
						Spec: gatewayv1beta1.ReferenceGrantSpec{
							From: []gatewayv1beta1.ReferenceGrantFrom{{
								Group:     gatewayv1.Group(gatewayAPIGroup),
								Kind:      gatewayv1.Kind("ListenerSet"),
								Namespace: gatewayv1.Namespace("apps"),
							}},
							To: []gatewayv1beta1.ReferenceGrantTo{{
								Group: gatewayv1.Group(""),
								Kind:  gatewayv1.Kind("Secret"),
								Name:  lo.ToPtr(gatewayv1.ObjectName("tls-cert")),
							}},
						},
					}},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsSecretRef(
			t.Context(),
			mockClient,
			"ListenerSet",
			"apps",
			types.NamespacedName{Namespace: "certs", Name: "tls-cert"},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("referenceGrantAllowsSecretRef permits unnamed to target for matching kind", func(t *testing.T) {
		fake := faker.New()
		sourceNamespace := "apps-" + fake.Lorem().Word()
		secretNamespace := "certs-" + fake.Lorem().Word()
		secretName := "tls-" + fake.Lorem().Word()
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{{
						ObjectMeta: metav1.ObjectMeta{Namespace: secretNamespace, Name: "allow-" + fake.Lorem().Word()},
						Spec: gatewayv1beta1.ReferenceGrantSpec{
							From: []gatewayv1beta1.ReferenceGrantFrom{{
								Group:     gatewayv1.Group(gatewayAPIGroup),
								Kind:      gatewayv1.Kind("ListenerSet"),
								Namespace: gatewayv1.Namespace(sourceNamespace),
							}},
							To: []gatewayv1beta1.ReferenceGrantTo{{
								Group: gatewayv1.Group(""),
								Kind:  gatewayv1.Kind("Secret"),
							}},
						},
					}},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsSecretRef(
			t.Context(),
			mockClient,
			"ListenerSet",
			sourceNamespace,
			types.NamespacedName{Namespace: secretNamespace, Name: secretName},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("referenceGrantAllowsCoreRef handles named and unnamed targets for arbitrary core kinds", func(t *testing.T) {
		fake := faker.New()
		sourceKind := gatewayv1.Kind("BackendTLSPolicy")
		sourceNamespace := "routes-" + fake.Lorem().Word()
		refNamespace := "trust-" + fake.Lorem().Word()
		refName := "ca-" + fake.Lorem().Word()
		refObjectName := gatewayv1.ObjectName(refName)
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{
						{
							ObjectMeta: metav1.ObjectMeta{
								Namespace: refNamespace,
								Name:      "wrong-kind-" + fake.Lorem().Word(),
							},
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{{
									Group:     gatewayv1.Group(gatewayAPIGroup),
									Kind:      sourceKind,
									Namespace: gatewayv1.Namespace(sourceNamespace),
								}},
								To: []gatewayv1beta1.ReferenceGrantTo{{
									Group: gatewayv1.Group(""),
									Kind:  gatewayv1.Kind("Secret"),
								}},
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Namespace: refNamespace,
								Name:      "named-" + fake.Lorem().Word(),
							},
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{{
									Group:     gatewayv1.Group(gatewayAPIGroup),
									Kind:      sourceKind,
									Namespace: gatewayv1.Namespace(sourceNamespace),
								}},
								To: []gatewayv1beta1.ReferenceGrantTo{{
									Group: gatewayv1.Group(""),
									Kind:  gatewayv1.Kind("ConfigMap"),
									Name:  &refObjectName,
								}},
							},
						},
					},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsCoreRef(
			t.Context(),
			mockClient,
			sourceKind,
			sourceNamespace,
			"ConfigMap",
			types.NamespacedName{Namespace: refNamespace, Name: refName},
		)

		require.NoError(t, err)
		assert.True(t, allowed)

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{{
						ObjectMeta: metav1.ObjectMeta{Namespace: refNamespace, Name: "wildcard-" + fake.Lorem().Word()},
						Spec: gatewayv1beta1.ReferenceGrantSpec{
							From: []gatewayv1beta1.ReferenceGrantFrom{{
								Group:     gatewayv1.Group(gatewayAPIGroup),
								Kind:      sourceKind,
								Namespace: gatewayv1.Namespace(sourceNamespace),
							}},
							To: []gatewayv1beta1.ReferenceGrantTo{{
								Group: gatewayv1.Group(""),
								Kind:  gatewayv1.Kind("ConfigMap"),
							}},
						},
					}},
				}))
				return nil
			})

		allowed, err = referenceGrantAllowsCoreRef(
			t.Context(),
			mockClient,
			sourceKind,
			sourceNamespace,
			"ConfigMap",
			types.NamespacedName{Namespace: refNamespace, Name: "other-" + fake.Lorem().Word()},
		)

		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("referenceGrantAllowsSecretRef wraps grant list errors", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			Return(errors.New("secret grant list failed"))

		allowed, err := referenceGrantAllowsSecretRef(
			t.Context(),
			mockClient,
			"ListenerSet",
			"apps",
			types.NamespacedName{Namespace: "certs", Name: "tls-cert"},
		)

		require.ErrorContains(t, err, "failed to list ReferenceGrants")
		assert.False(t, allowed)
	})

	t.Run("referenceGrantAllowsSecretRef ignores non matching grants", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{
					Items: []gatewayv1beta1.ReferenceGrant{
						{
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{{
									Group:     gatewayv1.Group(gatewayAPIGroup),
									Kind:      gatewayv1.Kind("Gateway"),
									Namespace: gatewayv1.Namespace("apps"),
								}},
								To: []gatewayv1beta1.ReferenceGrantTo{{
									Group: gatewayv1.Group(""),
									Kind:  gatewayv1.Kind("Secret"),
								}},
							},
						},
						{
							Spec: gatewayv1beta1.ReferenceGrantSpec{
								From: []gatewayv1beta1.ReferenceGrantFrom{{
									Group:     gatewayv1.Group(gatewayAPIGroup),
									Kind:      gatewayv1.Kind("ListenerSet"),
									Namespace: gatewayv1.Namespace("apps"),
								}},
								To: []gatewayv1beta1.ReferenceGrantTo{{
									Group: gatewayv1.Group(""),
									Kind:  gatewayv1.Kind("Secret"),
									Name:  lo.ToPtr(gatewayv1.ObjectName("other-cert")),
								}},
							},
						},
					},
				}))
				return nil
			})

		allowed, err := referenceGrantAllowsSecretRef(
			t.Context(),
			mockClient,
			"ListenerSet",
			"apps",
			types.NamespacedName{Namespace: "certs", Name: "tls-cert"},
		)

		require.NoError(t, err)
		assert.False(t, allowed)
	})

	t.Run("referenceGrantAllowsSecretRef rejects cross namespace secret without grant", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1beta1.ReferenceGrantList{}))
				return nil
			})

		allowed, err := referenceGrantAllowsSecretRef(
			t.Context(),
			mockClient,
			"ListenerSet",
			"apps",
			types.NamespacedName{Namespace: "certs", Name: "tls-cert"},
		)

		require.NoError(t, err)
		assert.False(t, allowed)
	})
}
