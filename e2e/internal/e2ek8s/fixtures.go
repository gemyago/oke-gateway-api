package e2ek8s

import (
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	DefaultGatewayControllerName gatewayv1.GatewayController = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"
	DefaultGatewayConfigGroup                                = "oke-gateway-api.gemyago.github.io"
	DefaultGatewayConfigVersion                              = "v1"
	DefaultGatewayConfigKind                                 = "GatewayConfig"
	DefaultHTTPListenerName      gatewayv1.SectionName       = "http"
	DefaultHTTPPort              gatewayv1.PortNumber        = 80
	DefaultHTTPSListenerName     gatewayv1.SectionName       = "https"
	DefaultHTTPSPort             gatewayv1.PortNumber        = 443
	DefaultEchoImage                                         = "ghcr.io/gemyago/oke-gateway-api-server:main"
	DefaultEchoPort                                          = int32(8080)
	DefaultEchoReplicas                                      = int32(1)
	DefaultStaticHTTPImage                                   = "busybox:1.36.1"
)

type GatewayClassOptions struct {
	Name           string
	ControllerName gatewayv1.GatewayController
	Labels         map[string]string
	Annotations    map[string]string
}

type GatewayConfigOptions struct {
	Namespace      string
	Name           string
	LoadBalancerID string
	Labels         map[string]string
	Annotations    map[string]string
}

type HTTPGatewayOptions struct {
	Namespace         string
	Name              string
	GatewayClassName  string
	GatewayConfigName string
	ListenerName      gatewayv1.SectionName
	Port              gatewayv1.PortNumber
	Labels            map[string]string
	Annotations       map[string]string
}

type GatewayOptions struct {
	Namespace         string
	Name              string
	GatewayClassName  string
	GatewayConfigName string
	Listeners         []gatewayv1.Listener
	Labels            map[string]string
	Annotations       map[string]string
}

type EchoDeploymentOptions struct {
	Namespace   string
	Name        string
	Image       string
	Replicas    int32
	Port        int32
	Labels      map[string]string
	Annotations map[string]string
}

type StaticHTTPDeploymentOptions struct {
	Namespace    string
	Name         string
	Image        string
	Replicas     int32
	Port         int32
	ResponseText string
	Labels       map[string]string
	Annotations  map[string]string
}

type EchoServiceOptions struct {
	Namespace   string
	Name        string
	Port        int32
	Labels      map[string]string
	Annotations map[string]string
}

type TLSSecretOptions struct {
	Namespace   string
	Name        string
	Certificate []byte
	PrivateKey  []byte
	Labels      map[string]string
	Annotations map[string]string
}

type HTTPRouteOptions struct {
	Namespace     string
	Name          string
	GatewayName   string
	ListenerName  gatewayv1.SectionName
	ServiceName   string
	ServicePort   int32
	Hostnames     []gatewayv1.Hostname
	PathPrefix    string
	PathMatch     *gatewayv1.HTTPPathMatch
	OmitPathMatch bool
	HeaderMatches []gatewayv1.HTTPHeaderMatch
	Labels        map[string]string
	Annotations   map[string]string
}

func NewGatewayClass(opts GatewayClassOptions) *gatewayv1.GatewayClass {
	controllerName := opts.ControllerName
	if controllerName == "" {
		controllerName = DefaultGatewayControllerName
	}

	return &gatewayv1.GatewayClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1.GroupVersion.String(),
			Kind:       "GatewayClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Labels:      fixtureLabels(opts.Name, "gateway-class", opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: controllerName,
		},
	}
}

func NewGatewayConfig(opts GatewayConfigOptions) *unstructured.Unstructured {
	resource := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":        opts.Name,
				"namespace":   opts.Namespace,
				"labels":      cloneStringMap(opts.Labels),
				"annotations": cloneStringMap(opts.Annotations),
			},
			"spec": map[string]any{
				"loadBalancerId": opts.LoadBalancerID,
			},
		},
	}

	resource.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   DefaultGatewayConfigGroup,
		Version: DefaultGatewayConfigVersion,
		Kind:    DefaultGatewayConfigKind,
	})
	resource.SetLabels(fixtureLabels(opts.Name, "gateway-config", opts.Labels))
	resource.SetAnnotations(cloneStringMap(opts.Annotations))

	return resource
}

func NewGateway(opts GatewayOptions) *gatewayv1.Gateway {
	listeners := append([]gatewayv1.Listener(nil), opts.Listeners...)
	return &gatewayv1.Gateway{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1.GroupVersion.String(),
			Kind:       "Gateway",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Namespace:   opts.Namespace,
			Labels:      fixtureLabels(opts.Name, "gateway", opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(opts.GatewayClassName),
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: gatewayv1.Group(DefaultGatewayConfigGroup),
					Kind:  gatewayv1.Kind(DefaultGatewayConfigKind),
					Name:  opts.GatewayConfigName,
				},
			},
			Listeners: listeners,
		},
	}
}

func NewHTTPGateway(opts HTTPGatewayOptions) *gatewayv1.Gateway {
	listenerName := opts.ListenerName
	if listenerName == "" {
		listenerName = DefaultHTTPListenerName
	}

	port := opts.Port
	if port == 0 {
		port = DefaultHTTPPort
	}

	return NewGateway(GatewayOptions{
		Namespace:         opts.Namespace,
		Name:              opts.Name,
		GatewayClassName:  opts.GatewayClassName,
		GatewayConfigName: opts.GatewayConfigName,
		Labels:            opts.Labels,
		Annotations:       opts.Annotations,
		Listeners: []gatewayv1.Listener{
			{
				Name:     listenerName,
				Port:     port,
				Protocol: gatewayv1.HTTPProtocolType,
			},
		},
	})
}

func NewEchoDeployment(opts EchoDeploymentOptions) *appsv1.Deployment {
	image := opts.Image
	if image == "" {
		image = DefaultEchoImage
	}

	replicas := opts.Replicas
	if replicas == 0 {
		replicas = DefaultEchoReplicas
	}

	port := opts.Port
	if port == 0 {
		port = DefaultEchoPort
	}

	selector := map[string]string{"app": opts.Name}
	podLabels := fixtureLabels(opts.Name, "backend", opts.Labels)
	podLabels["app"] = opts.Name

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Namespace:   opts.Namespace,
			Labels:      cloneStringMap(podLabels),
			Annotations: cloneStringMap(opts.Annotations),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      cloneStringMap(podLabels),
					Annotations: cloneStringMap(opts.Annotations),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "echo",
							Image: image,
							Args:  []string{"start", "--json-logs"},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: port,
								},
							},
						},
					},
				},
			},
		},
	}
}

func NewStaticHTTPDeployment(opts StaticHTTPDeploymentOptions) *appsv1.Deployment {
	image := opts.Image
	if image == "" {
		image = DefaultStaticHTTPImage
	}

	replicas := opts.Replicas
	if replicas == 0 {
		replicas = DefaultEchoReplicas
	}

	port := opts.Port
	if port == 0 {
		port = DefaultEchoPort
	}

	selector := map[string]string{"app": opts.Name}
	podLabels := fixtureLabels(opts.Name, "backend", opts.Labels)
	podLabels["app"] = opts.Name

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Namespace:   opts.Namespace,
			Labels:      cloneStringMap(podLabels),
			Annotations: cloneStringMap(opts.Annotations),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      cloneStringMap(podLabels),
					Annotations: cloneStringMap(opts.Annotations),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "http",
							Image:   image,
							Command: []string{"sh", "-ceu"},
							Args: []string{
								fmt.Sprintf(
									"mkdir -p /www && printf '%%s' \"$RESPONSE_TEXT\" > /www/index.html && exec busybox httpd -f -p %d -h /www",
									port,
								),
							},
							Env: []corev1.EnvVar{
								{
									Name:  "RESPONSE_TEXT",
									Value: opts.ResponseText,
								},
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: port,
								},
							},
						},
					},
				},
			},
		},
	}
}

func NewEchoService(opts EchoServiceOptions) *corev1.Service {
	port := opts.Port
	if port == 0 {
		port = DefaultEchoPort
	}

	labels := fixtureLabels(opts.Name, "backend-service", opts.Labels)
	labels["app"] = opts.Name

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Namespace:   opts.Namespace,
			Labels:      labels,
			Annotations: cloneStringMap(opts.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": opts.Name},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       port,
					TargetPort: intstr.FromString("http"),
				},
			},
		},
	}
}

func NewTLSSecret(opts TLSSecretOptions) *corev1.Secret {
	data := map[string][]byte{
		corev1.TLSCertKey:       append([]byte(nil), opts.Certificate...),
		corev1.TLSPrivateKeyKey: append([]byte(nil), opts.PrivateKey...),
	}

	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Namespace:   opts.Namespace,
			Labels:      fixtureLabels(opts.Name, "tls-secret", opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
		},
		Type: corev1.SecretTypeTLS,
		Data: data,
	}
}

func NewHTTPRoute(opts HTTPRouteOptions) *gatewayv1.HTTPRoute {
	listenerName := opts.ListenerName
	if listenerName == "" {
		listenerName = DefaultHTTPListenerName
	}

	servicePort := opts.ServicePort
	if servicePort == 0 {
		servicePort = DefaultEchoPort
	}

	portNumber := gatewayv1.PortNumber(servicePort)
	pathMatch := cloneHTTPPathMatch(opts.PathMatch)
	if pathMatch == nil && !opts.OmitPathMatch {
		pathPrefix := opts.PathPrefix
		if pathPrefix == "" {
			pathPrefix = "/"
		}

		pathMatchType := gatewayv1.PathMatchPathPrefix
		pathMatch = &gatewayv1.HTTPPathMatch{
			Type:  &pathMatchType,
			Value: &pathPrefix,
		}
	}

	return &gatewayv1.HTTPRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1.GroupVersion.String(),
			Kind:       "HTTPRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        opts.Name,
			Namespace:   opts.Namespace,
			Labels:      fixtureLabels(opts.Name, "http-route", opts.Labels),
			Annotations: cloneStringMap(opts.Annotations),
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:        gatewayv1.ObjectName(opts.GatewayName),
						SectionName: &listenerName,
					},
				},
			},
			Hostnames: append([]gatewayv1.Hostname(nil), opts.Hostnames...),
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path:    pathMatch,
							Headers: append([]gatewayv1.HTTPHeaderMatch(nil), opts.HeaderMatches...),
						},
					},
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(opts.ServiceName),
									Port: &portNumber,
								},
							},
						},
					},
				},
			},
		},
	}
}

func fixtureLabels(name string, component string, extra map[string]string) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "oke-gateway-api-e2e",
		"app.kubernetes.io/part-of":    "oke-gateway-api-e2e",
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/component":  component,
	}
	if len(extra) > 0 {
		maps.Copy(labels, extra)
	}

	return labels
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}

	clone := make(map[string]string, len(source))
	maps.Copy(clone, source)

	return clone
}

func cloneHTTPPathMatch(source *gatewayv1.HTTPPathMatch) *gatewayv1.HTTPPathMatch {
	if source == nil {
		return nil
	}

	clone := &gatewayv1.HTTPPathMatch{}
	if source.Type != nil {
		matchType := *source.Type
		clone.Type = &matchType
	}

	if source.Value != nil {
		value := *source.Value
		clone.Value = &value
	}

	return clone
}
