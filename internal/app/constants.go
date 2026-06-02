package app

import gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

const (
	// ControllerClassName is the name of the controller managing resources.
	ControllerClassName = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"

	// NetworkLoadBalancerControllerClassName is the name of the controller managing L4 NLB resources.
	NetworkLoadBalancerControllerClassName = "oke-gateway-api.gemyago.github.io/oke-nlb-gateway-controller"

	// GatewayProgrammingRevisionAnnotation is the annotation for the gateway programming revision.
	// The revision may be incremented if additional programming steps are introduced by the controller.
	GatewayProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/gateway-programming-revision"

	// GatewayUsedSecretsAnnotationPrefix is extended with each secret full name and stores the secret revision.
	GatewayUsedSecretsAnnotationPrefix = "secrets.oke-gateway-api.gemyago.github.io"

	// ListenerTLSOptionOCICertificateOCID configures an existing OCI Certificates Service certificate for a listener.
	ListenerTLSOptionOCICertificateOCID = "oci.oraclecloud.com/certificate-ocid"

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

	// NetworkLoadBalancerGatewayProgrammingRevisionAnnotation is the annotation for the L4 gateway programming revision.
	// The revision may be incremented if additional NLB programming steps are introduced by the controller.
	NetworkLoadBalancerGatewayProgrammingRevisionAnnotation = "oke-gateway-api.gemyago.github.io/" +
		"nlb-gateway-programming-revision"

	// NetworkLoadBalancerGatewayProgrammingRevisionValue is the value for the L4 gateway programming revision.
	// Incremented when the NLB controller programming steps are changed.
	NetworkLoadBalancerGatewayProgrammingRevisionValue = "1"

	// NetworkLoadBalancerGatewayProgrammedFinalizer indicates the L4 Gateway has provisioned OCI NLB resources.
	NetworkLoadBalancerGatewayProgrammedFinalizer = "oke-gateway-api.gemyago.github.io/nlb-gateway-programmed"

	// NetworkLoadBalancerGatewayIDAnnotation stores the OCI NLB OCID programmed by the controller.
	NetworkLoadBalancerGatewayIDAnnotation = "oke-gateway-api.gemyago.github.io/nlb-id"

	// NetworkLoadBalancerTCPRouteProgrammedFinalizer indicates a TCPRoute has programmed OCI NLB resources.
	NetworkLoadBalancerTCPRouteProgrammedFinalizer = "oke-gateway-api.gemyago.github.io/nlb-tcproute-programmed"

	// NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation tracks NLB backend sets programmed by a TCPRoute.
	NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation = "oke-gateway-api.gemyago.github.io/" +
		"nlb-tcproute-backendsets"

	// NetworkLoadBalancerUDPRouteProgrammedFinalizer indicates a UDPRoute has programmed OCI NLB resources.
	NetworkLoadBalancerUDPRouteProgrammedFinalizer = "oke-gateway-api.gemyago.github.io/nlb-udproute-programmed"

	// NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation tracks NLB backend sets programmed by a UDPRoute.
	NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation = "oke-gateway-api.gemyago.github.io/" +
		"nlb-udproute-backendsets"

	// NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation overrides the TCP health check port for UDPRoute backends.
	NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation = "oke-gateway-api.gemyago.github.io/" +
		"nlb-udp-health-check-port"

	// HTTPRouteProgrammingRevisionValue is the value for the http route programming revision.
	// Incremented when the controller programming steps are changed.
	HTTPRouteProgrammingRevisionValue = "3"
)

const ConfigRefGroup = "oke-gateway-api.gemyago.github.io"
const ConfigRefKind = "GatewayConfig"

func isSupportedControllerClassName(controllerName gatewayv1.GatewayController) bool {
	return controllerName == ControllerClassName ||
		controllerName == NetworkLoadBalancerControllerClassName
}
