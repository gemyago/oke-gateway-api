package app

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type GatewayConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	LoadBalancerID    string `json:"loadBalancerId"`
}
