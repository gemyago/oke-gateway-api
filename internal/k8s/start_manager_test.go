package k8s

import (
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestHTTPRouteObjectPredicate(t *testing.T) {
	t.Run("accepts annotation only updates", func(t *testing.T) {
		oldRoute := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "demo",
				Name:       "api",
				Generation: 1,
				Annotations: map[string]string{
					"example.com/reconcile": "before",
				},
			},
		}
		newRoute := oldRoute.DeepCopy()
		newRoute.Annotations["example.com/reconcile"] = "after"

		result := httpRouteObjectPredicate().Update(event.UpdateEvent{
			ObjectOld: oldRoute,
			ObjectNew: newRoute,
		})

		assert.True(t, result)
	})
}

func TestStartManager(t *testing.T) {
	t.Run("gatewaySecretPredicate", func(t *testing.T) {
		t.Run("allows TLS Secret create events to reach Gateway mapping", func(t *testing.T) {
			fake := faker.New()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tls-" + fake.Internet().Slug(),
					Namespace: "ns-" + fake.Internet().Slug(),
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					corev1.TLSCertKey:       []byte(fake.Lorem().Sentence(10)),
					corev1.TLSPrivateKeyKey: []byte(fake.Lorem().Sentence(10)),
				},
			}

			result := gatewaySecretPredicate().Create(event.CreateEvent{Object: secret})

			assert.True(t, result)
		})
	})
}
