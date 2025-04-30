package app

const (

	// ControllerClassName is the name of the controller managing resources.
	ControllerClassName = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"

	// GatewayProgrammingRevisionAnnotation is the annotation for the gateway programming revision.
	// The revision may be incremented if additional programming steps are introduced by the controller.
	GatewayProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/gateway-programming-revision"

	// GatewayProgrammingRevisionValue is the value for the gateway programming revision.
	// Incremented when the controller programming steps are changed.
	GatewayProgrammingRevisionValue = "1"
)

const ConfigRefGroup = "oke-gateway-api.gemyago.github.io"
const ConfigRefKind = "GatewayConfig"
