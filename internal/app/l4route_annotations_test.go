package app

import (
	"strings"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestResourceNameAnnotations(t *testing.T) {
	t.Run("joined annotation names are deterministic and sorted", func(t *testing.T) {
		fake := faker.New()
		names := map[string]struct{}{
			"bs_" + fake.Numerify("30########"): {},
			"bs_" + fake.Numerify("10########"): {},
			"bs_" + fake.Numerify("20########"): {},
		}

		got := joinedAnnotationNames(names)

		assert.Equal(t, got, joinedAnnotationNames(names))
		parts := strings.Split(got, ",")
		assert.Len(t, parts, len(names))
		assert.IsIncreasing(t, parts)
	})

	t.Run("route annotation names round trip as a set", func(t *testing.T) {
		fake := faker.New()
		annotation := "oke-gateway-api.gemyago.github.io/" + fake.Internet().Slug()
		route := &gatewayv1.TCPRoute{}
		wantNames := map[string]struct{}{
			"bs_" + fake.Numerify("########"):       {},
			"owned_" + fake.Numerify("########"):    {},
			"listener_" + fake.Numerify("########"): {},
		}

		setAnnotatedBackendSetNames(route, annotation, wantNames)
		gotNames := annotatedBackendSetNames(route, annotation)

		assert.Equal(t, wantNames, gotNames)
		assert.Equal(t, joinedAnnotationNames(wantNames), route.GetAnnotations()[annotation])
	})

	t.Run("setting empty route annotation names clears stale annotation", func(t *testing.T) {
		fake := faker.New()
		annotation := "oke-gateway-api.gemyago.github.io/" + fake.Internet().Slug()
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					annotation:                     "stale_" + fake.Numerify("########"),
					"other.example.com/annotation": "value-" + fake.Lorem().Word(),
				},
			},
		}

		setAnnotatedBackendSetNames(route, annotation, map[string]struct{}{})

		assert.NotContains(t, route.GetAnnotations(), annotation)
		assert.Contains(t, route.GetAnnotations(), "other.example.com/annotation")
	})

	t.Run("setting empty route annotation names handles nil annotations", func(t *testing.T) {
		fake := faker.New()
		annotation := "oke-gateway-api.gemyago.github.io/" + fake.Internet().Slug()
		route := &gatewayv1.UDPRoute{}

		setAnnotatedBackendSetNames(route, annotation, map[string]struct{}{})

		assert.Empty(t, annotatedBackendSetNames(route, annotation))
		assert.NotContains(t, route.GetAnnotations(), annotation)
	})

	t.Run("gateway annotation names trim blanks and deduplicate", func(t *testing.T) {
		fake := faker.New()
		annotation := "oke-gateway-api.gemyago.github.io/" + fake.Internet().Slug()
		firstName := "listener_" + fake.Numerify("########")
		secondName := "backend_" + fake.Numerify("########")
		gateway := gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					annotation: " " + firstName + ",, " + secondName + " ," + firstName + " ",
				},
			},
		}

		got := annotatedResourceNames(gateway, annotation)

		assert.Equal(t, map[string]struct{}{
			firstName:  {},
			secondName: {},
		}, got)
	})

	t.Run("merge name sets is idempotent and keeps all unique names", func(t *testing.T) {
		fake := faker.New()
		firstName := "listener_" + fake.Numerify("########")
		secondName := "backend_" + fake.Numerify("########")
		thirdName := "policy_" + fake.Numerify("########")
		firstSet := map[string]struct{}{
			firstName:  {},
			secondName: {},
		}
		secondSet := map[string]struct{}{
			secondName: {},
			thirdName:  {},
		}

		got := mergeNameSets(firstSet, secondSet, firstSet)

		assert.Equal(t, map[string]struct{}{
			firstName:  {},
			secondName: {},
			thirdName:  {},
		}, got)
	})
}
