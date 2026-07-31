package app

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func servicePortForBackendRef(
	service corev1.Service,
	backendRef gatewayv1.BackendRef,
) (*corev1.ServicePort, error) {
	if backendRef.BackendObjectReference.Port == nil {
		return nil, fmt.Errorf("backendRef %s is missing port", backendRef.BackendObjectReference.Name)
	}
	for i := range service.Spec.Ports {
		if service.Spec.Ports[i].Port == *backendRef.BackendObjectReference.Port {
			return &service.Spec.Ports[i], nil
		}
	}
	return nil, fmt.Errorf(
		"backendRef service %s has no port %d",
		backendRef.BackendObjectReference.Name,
		*backendRef.BackendObjectReference.Port,
	)
}

func endpointPortForServicePort(
	servicePort corev1.ServicePort,
	endpointSlice discoveryv1.EndpointSlice,
) (int, bool) {
	if servicePort.TargetPort.Type == 0 || servicePort.TargetPort.IntVal != 0 {
		port := servicePort.TargetPort.IntValue()
		if port == 0 {
			port = int(servicePort.Port)
		}
		return port, true
	}

	for _, endpointPort := range endpointSlice.Ports {
		if endpointPort.Port == nil {
			continue
		}
		if endpointPort.Name != nil && *endpointPort.Name == servicePort.Name {
			return int(*endpointPort.Port), true
		}
	}
	return 0, false
}
