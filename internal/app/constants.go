package app

const (

	// LoadBalancerReconciledReason is the reason for the LoadBalancerReconciled condition when true.
	LoadBalancerReconciledReason = "LoadBalancerReconciled"

	// InvalidResourceConfigurationReason is the reason for the InvalidResourceConfiguration condition.
	InvalidResourceConfigurationReason = "InvalidResourceConfiguration"

	// MissingConfigReason is the reason when a required annotation is missing.
	MissingConfigReason = "MissingConfig"

	// ControllerClassName is the name of the controller managing resources.
	ControllerClassName = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"
)

// LoadBalancerIDAnnotation is the annotation for the load balancer ID.
const LoadBalancerIDAnnotation = "oke-gateway-api.gemyago.github.io/oci-load-balancer-id"

const ConfigRefGroup = "oke-gateway-api.gemyago.github.io"
const ConfigRefKind = "GatewayConfig"
