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

Prepare a GatewayConfig resource. You will need to specify the OCID of an existing OCI Load Balancer.
```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: oke-gateway-api.gemyago.github.io/v1
kind: GatewayConfig
metadata:
  name: oke-gateway-config
spec:
  loadBalancerId: ocid1.loadbalancer.oc1..exampleuniqueID
EOF
```

Create a Gateway resource:
```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: oke-gateway
spec:
  gatewayClassName: oke-gateway-api
  infrastructure:
    parametersRef:
      group: oke-gateway-api.gemyago.github.io
      kind: GatewayConfig
      name: oke-gateway-config
  listeners:
    - name: http
      port: 80
      protocol: HTTP
EOF
```

Uninstall resources:
```bash
kubectl -n oke-gw delete gateway oke-gateway
kubectl -n oke-gw delete gatewayclass oke-gateway-api
kubectl -n oke-gw delete gatewayconfig oke-gateway-config
```

## Contributing

### Project Setup

Please have the following tools installed: 
* [direnv](https://github.com/direnv/direnv) 
* [gobrew](https://github.com/kevincobain2000/gobrew#install-or-update)
* [pyenv](https://github.com/pyenv/pyenv?tab=readme-ov-file#installation).

Install/Update dependencies: 
```sh
direnv allow

# Install go dependencies
go mod download
go install tool

# or update:
go get -u ./... && go mod tidy

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

### Running in a local mode

For local development purposes you can run the controller fully locally pointing on a local k8s cluster and provision the resources in a real OCI tenancy.

Please follow [OCI SDK CLI Setup](https://docs.oracle.com/en-us/iaas/Content/API/SDKDocs/cliinstall.htm#configfile)

You may want to use alternative SDK config location. In this case please create `.envrc.local` file with the contents similar to below:
```bash
# Point to the OCI CLI config file
export OCI_CLI_CONFIG_FILE=${PWD}/../.oci-cloud-cli/config
export OCI_CONFIG_FILE=${PWD}/../.oci-cloud-cli/config

# Point to the OCI CLI profile
export OCI_CLI_PROFILE=DEFAULT

# Point to the OCI CLI config profile
export OCI_CLI_CONFIG_PROFILE=eu-frankfurt-1
```

Reload the environment and check if all good:
```sh
direnv reload

# Check if the oci sdk is properly configured
oci iam user list
```

Make sure to have locally running k8s cluster and `kubectl` configured to point to it.

You may want to apply just the CRDs in the cluster for config resources:
```sh
kubectl apply -f deploy/helm/controller/templates/gateway-config-crd.yaml
```

Run the controller locally:
```sh
go run ./cmd/server/ start
```



