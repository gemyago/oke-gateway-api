package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
