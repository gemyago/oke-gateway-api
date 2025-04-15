package app

import (
	"context"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This is an internal interface used only to describe what we need from the client.
type k8sClient interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	Status() client.StatusWriter
	Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
}

// This file contains application required ports (e.g external dependencies)
// ociLoadBalancerClient defines the interface for interacting with OCI Load Balancer service.
type ociLoadBalancerClient interface {
	GetLoadBalancer(ctx context.Context, request loadbalancer.GetLoadBalancerRequest) (
		response loadbalancer.GetLoadBalancerResponse, err error)

	CreateBackendSet(ctx context.Context, request loadbalancer.CreateBackendSetRequest) (
		response loadbalancer.CreateBackendSetResponse, err error)

	GetBackendSet(ctx context.Context, request loadbalancer.GetBackendSetRequest) (
		response loadbalancer.GetBackendSetResponse, err error)
}
