# oke-gateway-api

[![Build](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml/badge.svg)](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml)
[![Coverage](https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.svg)](https://htmlpreview.github.io/?https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.html)

[Gateway API](https://gateway-api.sigs.k8s.io/) implementation for [Oracle Kubernetes (OKE)](https://www.oracle.com/cloud/cloud-native/kubernetes-engine/).

Project status: **Early Alpha**

## Getting Started

Install Gateway API CRDs:
```sh
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
```

Install the OKE Gateway API controller:
```sh
kubectl apply -f https://raw.githubusercontent.com/gemyago/oke-gateway-api/main/deploy/gateway-api-controller.yaml
```

Install the OKE Gateway API controller using Helm:
```sh
# Create namespace
kubectl create namespace oke-gw

# Install controller
helm install oke-gateway-api-controller oke-gateway-api/controller --namespace oke-gw
```

Create a GatewayClass resource:
```bash
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: oke-gateway-api
spec:
  controllerName: oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller
EOF
```

Create a Gateway resource:
```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: oke-gateway
  annotations:
    # Specify the OCID of an existing OCI Load Balancer
    oke-gateway-api.gemyago.github.io/oci-load-balancer-id: "ocid1.loadbalancer.oc1..exampleuniqueID"
spec:
  gatewayClassName: oke-gateway-api
  listeners:
    - name: http
      port: 80
      protocol: HTTP
EOF
```



## Contributing

### Project Setup

Please have the following tools installed: 
* [direnv](https://github.com/direnv/direnv) 
* [gobrew](https://github.com/kevincobain2000/gobrew#install-or-update)

Install/Update dependencies: 
```sh
# Install
go mod download
go install tool

# Update:
go get -u ./... && go mod tidy
```

### Build dependencies

This step is required if you plan to work on the build tooling. In this case please make sure to install:
* [pyenv](https://github.com/pyenv/pyenv?tab=readme-ov-file#installation).

```sh
# Install required python version
pyenv install -s

# Setup python environment
python -m venv .venv

# Reload env
direnv reload

# Install python dependencies
pip install -r requirements.txt
```

If updating python dependencies, please lock them:
```sh
pip freeze > requirements.txt
```

### Lint and Tests

Run all lint and tests:
```bash
make lint
make test
```

Run specific tests:
```bash
# Run once
go test -v ./internal/api/http/v1controllers/ --run TestHealthCheck

# Run same test multiple times
# This is useful to catch flaky tests
go test -v -count=5 ./internal/api/http/v1controllers/ --run TestHealthCheck

# Run and watch. Useful when iterating on tests
gow test -v ./internal/api/http/v1controllers/ --run TestHealthCheck
```

### Running in a local cluster

For local development purposes you can run the controller in a local cluster but provision the resources in a real OCI tenancy.

You may want to apply just the CRDs:
```sh
kubectl apply -f deploy/helm/controller/templates/gateway-config-crd.yaml
```



