package types

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GatewayConfig is the Schema for the gatewayconfigs API.
type GatewayConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GatewayConfigSpec   `json:"spec"`
	Status GatewayConfigStatus `json:"status,omitempty"`
}

// GatewayConfigSpec defines the desired state of GatewayConfig.
type GatewayConfigSpec struct {
	// LoadBalancerID is the OCID of the OCI Load Balancer to be used by the gateway
	// +required
	LoadBalancerID string `json:"loadBalancerId"`
}

// GatewayConfigStatus defines the observed state of GatewayConfig.
type GatewayConfigStatus struct {
	// Conditions represent the latest available observations of the gateway config's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GatewayConfigList contains a list of GatewayConfig.
type GatewayConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GatewayConfig `json:"items"`
}
