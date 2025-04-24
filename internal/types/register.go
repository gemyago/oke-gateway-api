package types

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// GroupName is the group name used in this package.
	GroupName = "oke-gateway-api.gemyago.github.io"
	// Version is the API version.
	Version = "v1"
)

// Adds the list of known types to Scheme.
func AddKnownTypes(scheme *runtime.Scheme) error {
	groupVersion := schema.GroupVersion{Group: GroupName, Version: Version}
	scheme.AddKnownTypes(groupVersion,
		&GatewayConfig{},
		&GatewayConfigList{},
	)
	metav1.AddToGroupVersion(scheme, groupVersion)
	return nil
}
