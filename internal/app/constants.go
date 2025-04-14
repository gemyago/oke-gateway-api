package app

const (
	// AcceptedConditionType is the condition type for resource acceptance.
	AcceptedConditionType = "Accepted"

	// AcceptedConditionReason is the reason for the Accepted condition when true.
	AcceptedConditionReason = "Accepted"

	// InvalidResourceConfigurationReason is the reason for the InvalidResourceConfiguration condition.
	InvalidResourceConfigurationReason = "InvalidResourceConfiguration"

	// ControllerClassName is the name of the controller managing resources.
	ControllerClassName = "oke-gateway-api.oraclecloud.com/oke-gateway-controller"
)

// ManagedByAnnotation indicates which controller manages the resource.
const ManagedByAnnotation = "oke-gateway-api.oraclecloud.com/managed-by"
