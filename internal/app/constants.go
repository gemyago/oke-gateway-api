package app

const (
	// AcceptedConditionType is the condition type for resource acceptance.
	AcceptedConditionType = "Accepted"

	// AcceptedConditionReason is the reason for the Accepted condition when true.
	AcceptedConditionReason = "Accepted"

	// ProgrammedGatewayConditionType is the condition type for programmed gateways.
	ProgrammedGatewayConditionType = "Programmed"

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
