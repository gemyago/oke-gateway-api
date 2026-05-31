// +k8s:deepcopy-gen=package
// +groupName=oke-gateway-api.gemyago.github.io

// Package types contains the OKE Gateway API custom resource types.
package types

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen paths="./..." object:headerFile="./header.go.txt"
