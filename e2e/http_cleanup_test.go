package e2e

import (
	"fmt"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
)

func TestProgrammedPolicyRuleNames(t *testing.T) {
	t.Run("extracts rule names from listener scoped and legacy annotations", func(t *testing.T) {
		fake := faker.New()
		listenerName := "listener-" + fake.Lorem().Word()
		scopedRuleName := "scoped-" + fake.Lorem().Word()
		legacyRuleName := "legacy-" + fake.Lorem().Word()
		route := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route-" + fake.UUID().V4(),
				Namespace: "namespace-" + fake.UUID().V4(),
				Annotations: map[string]string{
					httpRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf(
						" %s/%s , %s ",
						listenerName,
						scopedRuleName,
						legacyRuleName,
					),
				},
			},
		}

		got, err := programmedPolicyRuleNames(route)

		require.NoError(t, err)
		require.Equal(t, []string{scopedRuleName, legacyRuleName}, got)
	})

	t.Run("ignores malformed listener scoped annotations", func(t *testing.T) {
		fake := faker.New()
		route := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "route-" + fake.UUID().V4(),
				Namespace: "namespace-" + fake.UUID().V4(),
				Annotations: map[string]string{
					httpRouteProgrammedPolicyRulesAnnotation: "/,listener/",
				},
			},
		}

		got, err := programmedPolicyRuleNames(route)

		require.Error(t, err)
		require.Empty(t, got)
	})
}

func TestDeleteNamespaceHTTPRoutes(t *testing.T) {
	t.Run("deletes only routes in the target namespace", func(t *testing.T) {
		fakeGen := faker.New()
		targetNamespace := "oke-gw-e2e-" + fakeGen.UUID().V4()
		otherNamespace := "oke-gw-e2e-" + fakeGen.UUID().V4()
		targetRouteA := "echo-route-" + fakeGen.UUID().V4()
		targetRouteB := "echo-route-" + fakeGen.UUID().V4()
		otherRoute := "echo-route-" + fakeGen.UUID().V4()

		scheme, err := e2ek8s.NewScheme()
		require.NoError(t, err)

		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(
				&gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      targetRouteA,
						Namespace: targetNamespace,
					},
				},
				&gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      targetRouteB,
						Namespace: targetNamespace,
					},
				},
				&gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      otherRoute,
						Namespace: otherNamespace,
					},
				},
			).
			Build()

		err = deleteNamespaceHTTPRoutes(t.Context(), kubeClient, targetNamespace)
		require.NoError(t, err)

		for _, key := range []ctrlclient.ObjectKey{
			{Name: targetRouteA, Namespace: targetNamespace},
			{Name: targetRouteB, Namespace: targetNamespace},
		} {
			route := &gatewayv1.HTTPRoute{}
			err = kubeClient.Get(t.Context(), key, route)
			require.Error(t, err)
		}

		remainingRoute := &gatewayv1.HTTPRoute{}
		err = kubeClient.Get(
			t.Context(),
			ctrlclient.ObjectKey{Name: otherRoute, Namespace: otherNamespace},
			remainingRoute,
		)
		require.NoError(t, err)
	})
}
