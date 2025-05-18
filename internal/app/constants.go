package app

const (

	// ControllerClassName is the name of the controller managing resources.
	ControllerClassName = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"

	// GatewayProgrammingRevisionAnnotation is the annotation for the gateway programming revision.
	// The revision may be incremented if additional programming steps are introduced by the controller.
	GatewayProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/gateway-programming-revision"

	// HTTPRouteProgrammingRevisionAnnotation is the annotation for the http route programming revision.
	// The revision may be incremented if additional programming steps are introduced by the controller.
	HTTPRouteProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programming-revision"

	// HTTPRouteProgrammedPolicyRulesAnnotation is a comma-separated list of load balancer listener policy rule names.
	// The value is set by the controller when the http route is programmed.
	HTTPRouteProgrammedPolicyRulesAnnotation = "oke-gateway-api.gemyago.github.io/http-route-programmed-lb-policy-rules"

	// HTTPRouteProgrammedFinalizer is the finalizer that indicates that the http route has been programmed.
	// It is used to clean up the resources when the http route is deleted.
	HTTPRouteProgrammedFinalizer = "oke-gateway-api.gemyago.github.io/http-route-programmed"

	// GatewayProgrammingRevisionValue is the value for the gateway programming revision.
	// Incremented when the controller programming steps are changed.
	GatewayProgrammingRevisionValue = "1"

	// HTTPRouteProgrammingRevisionValue is the value for the http route programming revision.
	// Incremented when the controller programming steps are changed.
	HTTPRouteProgrammingRevisionValue = "1"
)

const ConfigRefGroup = "oke-gateway-api.gemyago.github.io"
const ConfigRefKind = "GatewayConfig"
