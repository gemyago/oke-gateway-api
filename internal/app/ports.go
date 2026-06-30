package app

import (
	"context"

	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
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

	DeleteBackendSet(ctx context.Context, request loadbalancer.DeleteBackendSetRequest) (
		response loadbalancer.DeleteBackendSetResponse, err error)

	GetBackendSet(ctx context.Context, request loadbalancer.GetBackendSetRequest) (
		response loadbalancer.GetBackendSetResponse, err error)

	CreateListener(ctx context.Context, request loadbalancer.CreateListenerRequest) (
		response loadbalancer.CreateListenerResponse, err error)

	UpdateListener(ctx context.Context, request loadbalancer.UpdateListenerRequest) (
		response loadbalancer.UpdateListenerResponse, err error)

	CreateHostname(ctx context.Context, request loadbalancer.CreateHostnameRequest) (
		response loadbalancer.CreateHostnameResponse, err error)

	GetHostname(ctx context.Context, request loadbalancer.GetHostnameRequest) (
		response loadbalancer.GetHostnameResponse, err error)

	CreateBackend(ctx context.Context, request loadbalancer.CreateBackendRequest) (
		response loadbalancer.CreateBackendResponse, err error)

	UpdateBackendSet(ctx context.Context, request loadbalancer.UpdateBackendSetRequest) (
		response loadbalancer.UpdateBackendSetResponse, err error)

	DeleteListener(ctx context.Context, request loadbalancer.DeleteListenerRequest) (
		response loadbalancer.DeleteListenerResponse, err error)

	UpdateRuleSet(ctx context.Context, request loadbalancer.UpdateRuleSetRequest) (
		response loadbalancer.UpdateRuleSetResponse, err error)

	GetRuleSet(ctx context.Context, request loadbalancer.GetRuleSetRequest) (
		response loadbalancer.GetRuleSetResponse, err error)

	GetRoutingPolicy(ctx context.Context, request loadbalancer.GetRoutingPolicyRequest) (
		response loadbalancer.GetRoutingPolicyResponse, err error)

	CreateRoutingPolicy(ctx context.Context, request loadbalancer.CreateRoutingPolicyRequest) (
		response loadbalancer.CreateRoutingPolicyResponse, err error)

	UpdateRoutingPolicy(ctx context.Context, request loadbalancer.UpdateRoutingPolicyRequest) (
		response loadbalancer.UpdateRoutingPolicyResponse, err error)

	DeleteRoutingPolicy(ctx context.Context, request loadbalancer.DeleteRoutingPolicyRequest) (
		response loadbalancer.DeleteRoutingPolicyResponse, err error)

	CreateCertificate(ctx context.Context, request loadbalancer.CreateCertificateRequest) (
		response loadbalancer.CreateCertificateResponse, err error)

	DeleteCertificate(ctx context.Context, request loadbalancer.DeleteCertificateRequest) (
		response loadbalancer.DeleteCertificateResponse, err error)
}

type workRequestsWatcher interface {
	WaitFor(ctx context.Context, workRequestID string) error
}

type noopWorkRequestsWatcher struct{}

func (noopWorkRequestsWatcher) WaitFor(context.Context, string) error {
	return nil
}

type ociCertificatesManagementClient interface {
	CreateCaBundle(ctx context.Context, request certificatesmanagement.CreateCaBundleRequest) (
		response certificatesmanagement.CreateCaBundleResponse, err error)

	GetCaBundle(ctx context.Context, request certificatesmanagement.GetCaBundleRequest) (
		response certificatesmanagement.GetCaBundleResponse, err error)

	ListCaBundles(ctx context.Context, request certificatesmanagement.ListCaBundlesRequest) (
		response certificatesmanagement.ListCaBundlesResponse, err error)

	UpdateCaBundle(ctx context.Context, request certificatesmanagement.UpdateCaBundleRequest) (
		response certificatesmanagement.UpdateCaBundleResponse, err error)

	DeleteCaBundle(ctx context.Context, request certificatesmanagement.DeleteCaBundleRequest) (
		response certificatesmanagement.DeleteCaBundleResponse, err error)
}

// ociNetworkLoadBalancerClient defines the interface for OCI Network Load Balancer operations.
type ociNetworkLoadBalancerClient interface {
	GetNetworkLoadBalancer(ctx context.Context, request networkloadbalancer.GetNetworkLoadBalancerRequest) (
		response networkloadbalancer.GetNetworkLoadBalancerResponse, err error)

	CreateListener(ctx context.Context, request networkloadbalancer.CreateListenerRequest) (
		response networkloadbalancer.CreateListenerResponse, err error)

	UpdateListener(ctx context.Context, request networkloadbalancer.UpdateListenerRequest) (
		response networkloadbalancer.UpdateListenerResponse, err error)

	DeleteListener(ctx context.Context, request networkloadbalancer.DeleteListenerRequest) (
		response networkloadbalancer.DeleteListenerResponse, err error)

	CreateBackendSet(ctx context.Context, request networkloadbalancer.CreateBackendSetRequest) (
		response networkloadbalancer.CreateBackendSetResponse, err error)

	UpdateBackendSet(ctx context.Context, request networkloadbalancer.UpdateBackendSetRequest) (
		response networkloadbalancer.UpdateBackendSetResponse, err error)

	DeleteBackendSet(ctx context.Context, request networkloadbalancer.DeleteBackendSetRequest) (
		response networkloadbalancer.DeleteBackendSetResponse, err error)

	CreateBackend(ctx context.Context, request networkloadbalancer.CreateBackendRequest) (
		response networkloadbalancer.CreateBackendResponse, err error)

	UpdateBackend(ctx context.Context, request networkloadbalancer.UpdateBackendRequest) (
		response networkloadbalancer.UpdateBackendResponse, err error)

	DeleteBackend(ctx context.Context, request networkloadbalancer.DeleteBackendRequest) (
		response networkloadbalancer.DeleteBackendResponse, err error)
}
