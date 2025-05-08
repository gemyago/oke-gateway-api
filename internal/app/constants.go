package app

const (

	// ControllerClassName is the name of the controller managing resources.
	ControllerClassName = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"

	// GatewayProgrammingRevisionAnnotation is the annotation for the gateway programming revision.
	// The revision may be incremented if additional programming steps are introduced by the controller.
	GatewayProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/gateway-programming-revision"

	// HttpRouteProgrammingRevisionAnnotation is the annotation for the http route programming revision.
	// The revision may be incremented if additional programming steps are introduced by the controller.
	HttpRouteProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programming-revision"

	// GatewayProgrammingRevisionValue is the value for the gateway programming revision.
	// Incremented when the controller programming steps are changed.
	GatewayProgrammingRevisionValue = "1"

	// HttpRouteProgrammingRevisionValue is the value for the http route programming revision.
	// Incremented when the controller programming steps are changed.
	HttpRouteProgrammingRevisionValue = "1"
)

const ConfigRefGroup = "oke-gateway-api.gemyago.github.io"
const ConfigRefKind = "GatewayConfig"
