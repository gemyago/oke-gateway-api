# oke-gateway-api

[![Build](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml/badge.svg)](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml)
[![Coverage](https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.svg)](https://htmlpreview.github.io/?https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.html)

[Gateway API](https://gateway-api.sigs.k8s.io/) implementation for [Oracle Kubernetes (OKE)](https://www.oracle.com/cloud/cloud-native/kubernetes-engine/).

Project status: **Beta**

## Getting Started

Install Gateway API CRDs:
```sh
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
```

Prepare API key and config file (use actual values):
```ini
[DEFAULT]
user=<user_ocid>
fingerprint=<key_fingerprint>
tenancy=<tenancy_ocid>
region=<oci_region>
key_file=/etc/oci/oci_api_key.pem
```
Note: `key_file` corresponds to the location on **pod** that will be mounted as a secret, so leave it as is.

Create a secret with the API key and config file:
```sh
# Ensure namespace exists first
kubectl create namespace oke-gw

# config should point to the locally prepared config file as per above example
# key.pem should point to the locally prepared private key file
kubectl create secret generic oci-api-key \
  --from-file=config=/path/to/created/config \
  --from-file=key.pem=/path/to/actual/privatekey.pem \
  -n oke-gw
```

Install the OKE Gateway API controller using Helm:
```sh
helm upgrade oke-gateway-api-controller \
    oci://ghcr.io/gemyago/helm-charts/oke-gateway-api-controller \
    --install \
    -n oke-gw
```
Give it few minutes to start.

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

The controller will **not** automatically create the load balancer. Please create it first.
Prepare a GatewayConfig resource. You will need to specify the OCID of the created OCI Load Balancer.
```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: oke-gateway-api.gemyago.github.io/v1
kind: GatewayConfig
metadata:
  name: oke-gateway-config
spec:
  # Replace with your Load Balancer OCID
  loadBalancerId: ocid1.loadbalancer.oc1..exampleuniqueID
EOF
```

Create Gateway resource:
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

Assuming you have a deployment and service similar to the following:
```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: oke-gateway-example-server
  labels:
    app: oke-gateway-example-server
spec:
  replicas: 1
  selector:
    matchLabels:
      app: oke-gateway-example-server
  template:
    metadata:
      labels:
        app: oke-gateway-example-server
    spec:
      containers:
      - name: echo
        # This is simple echo server that can be used to test the gateway
        image: ghcr.io/gemyago/oke-gateway-api-server:main
        args:
          - start
          - --json-logs
        ports:
        - containerPort: 8080
          name: http
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 200m
            memory: 256Mi
---
apiVersion: v1
kind: Service
metadata:
  name: oke-gateway-example-server
  labels:
    app: oke-gateway-example-server
spec:
  ports:
  - port: 8080
    name: http
    targetPort: http
  selector:
    app: oke-gateway-example-server
EOF
```

You can now attach the HTTP route to the gateway to route traffic to the deployment:
```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: oke-gateway-example-server
spec:
  parentRefs:
    - name: oke-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /echo
      backendRefs:
        - name: oke-gateway-example-server
          port: 8080
EOF
```

Uninstall example resources:
```bash
kubectl -n oke-gw delete gateway oke-gateway
kubectl -n oke-gw delete gatewayclass oke-gateway-api
kubectl -n oke-gw delete gatewayconfig oke-gateway-config
kubectl -n oke-gw delete deployment oke-gateway-example-server
kubectl -n oke-gw delete httproute oke-gateway-example-server
```

### HTTPS

Please refer to [https](./docs/https.md) for more details.

## Contributing

Use this section to setup the development environment.

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

### Running in a local mode

For local development purposes you can run the controller fully locally pointing desired k8s cluster and provision the resources in an actual OCI tenancy.

Please follow [OCI SDK CLI Setup](https://docs.oracle.com/en-us/iaas/Content/API/SDKDocs/cliinstall.htm#configfile) to setup the OCI CLI.

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

Make sure to `kubectl` configured to point to a desired k8s cluster.

Run the controller locally:
```sh
go run ./cmd/controller/ start
```



