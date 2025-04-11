package app

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// clientAdapter adapts a client.Client to our k8sClient interface.
type clientAdapter struct {
	client.Client
}

func (c *clientAdapter) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	return c.Client.Get(ctx, key, obj)
}

func (c *clientAdapter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.Client.List(ctx, list, opts...)
}

func TestGatewayClassController_Reconcile(t *testing.T) {
	// Create a test GatewayClass
	gatewayClass := &gatewayv1.GatewayClass{
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "oracle.com/oke-gateway-controller",
		},
	}

	// Create a fake client with the GatewayClass
	fakeClient := fake.NewClientBuilder().WithObjects(gatewayClass).Build()

	// Create an adapter for the fake client
	clientAdapter := &clientAdapter{fakeClient}

	// Create a test logger that writes to io.Discard
	testLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Create the controller
	controller := &GatewayClassController{
		client: clientAdapter,
		logger: testLogger,
	}

	// Create a reconcile request
	req := reconcile.Request{
		NamespacedName: client.ObjectKey{
			Name: gatewayClass.Name,
		},
	}

	// Call Reconcile
	result, err := controller.Reconcile(t.Context(), req)

	// Assert results
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}
