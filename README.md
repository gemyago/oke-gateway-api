# oke-gateway-api

[![Build](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml/badge.svg)](https://github.com/gemyago/oke-gateway-api/actions/workflows/build-flow.yml)
[![Coverage](https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.svg)](https://htmlpreview.github.io/?https://raw.githubusercontent.com/gemyago/oke-gateway-api/test-artifacts/coverage/golang-coverage.html)

[Gateway API](https://gateway-api.sigs.k8s.io/) implementation for [Oracle Kubernetes (OKE)](https://www.oracle.com/cloud/cloud-native/kubernetes-engine/).

Project status: **Beta**

## Supported Gateway API Resources

Installing Gateway API v1.6.0 may install CRDs that this controller does not implement.
Unsupported resource kinds are ignored: the controller does not watch them, reconcile them,
update their status, or provision OCI resources for them.

| Resource | Support |
| --- | --- |
| `GatewayClass` | Supported |
| `Gateway` | Supported |
| `HTTPRoute` | Supported on OCI Load Balancer |
| `GRPCRoute` | Supported on OCI Load Balancer |
| `TLSRoute` | Supported on OCI Load Balancer and OCI Network Load Balancer where OCI capabilities allow |
| `TCPRoute` | Supported on OCI Network Load Balancer |
| `UDPRoute` | Supported on OCI Network Load Balancer |
| `ReferenceGrant` | Supported for cross-namespace references used by supported routes and policies |
| `BackendTLSPolicy` | Supported for OCI Load Balancer backend TLS; OCI Network Load Balancer uses passthrough routing instead |
| `ListenerSet` | Not supported; ignored if installed |
| `XBackend`, `XBackendTrafficPolicy`, `XMesh` | Not supported; ignored if installed |

## Getting Started

Install Gateway API CRDs:
```sh
kubectl apply --server-side=true \
  -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.0/standard-install.yaml

# Optional: use experimental CRDs only when you need experimental Gateway API resources.
kubectl apply --server-side=true \
  -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.0/experimental-install.yaml
```
The controller can run with only the standard CRDs. `HTTPRoute`, `GRPCRoute`, `TLSRoute`,
`TCPRoute`, `UDPRoute`, and `BackendTLSPolicy` are standard in Gateway API v1.6.0.

Prepare API key and config file (use actual values):
```ini
[DEFAULT]
user=<user_ocid>
fingerprint=<key_fingerprint>
tenancy=<tenancy_ocid>
region=<oci_region>
key_file=/etc/oci/key.pem
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

## HTTPRoute matching

Following match types are supported:
- path: `PathPrefix` and `Exact`
- header: `Exact` and `RegularExpression`

### Notes on **RegularExpression**

OCI doesn't support regexp matching, instead start with (sw) or end with (ew) matching are possible. Due to this limitations, the below patterns only are supported, they will be mapped to corresponding OCI conditions:
- `^foo` -> `sw 'foo'`
- `^foo.*` -> `sw 'foo'`
- `^foo\\..*` -> `sw 'foo.'`
- `foo$` -> `ew 'foo'`
- `.*foo$` -> `ew 'foo'`

Other patterns will result in an error.

### HTTPS

Please refer to [https](./docs/https.md) for more details.

## GRPCRoute With OCI Load Balancer

`GRPCRoute` uses the standard Gateway API CRDs and is reconciled on OCI Load Balancer with the other layer 7 routes. It is not implemented on OCI Network Load Balancer. Use `TCPRoute` if you only need gRPC passthrough to pods.

The controller supports gRPC host, service, method, and exact header matching. `HTTPRoute` and `GRPCRoute` can share the same HTTPS listener and hostname; `GRPCRoute` rules require a native gRPC `content-type` and are ordered before broad `HTTPRoute` matches.

See [deploy/manifests/examples/grpcroute.yaml](./deploy/manifests/examples/grpcroute.yaml) for a minimal route example.

## Backend TLS

`BackendTLSPolicy` configures TLS from OCI Load Balancer backend sets to backend Pods for ALB-backed `HTTPRoute`, `GRPCRoute`, and `TLSRoute` with `tls.mode: Terminate`. It is not supported for OCI Network Load Balancer routes.

OCI backend SSL validates the backend certificate chain but does not enforce hostname/SAN identity. Policies must explicitly set `oci.oraclecloud.com/backend-hostname-validation: Disabled`, and unsupported standard fields such as `subjectAltNames` are rejected.

See [deploy/manifests/examples/backendtlspolicy.yaml](./deploy/manifests/examples/backendtlspolicy.yaml) for a complete example with a Gateway, HTTPRoute, Service, CA ConfigMap, and BackendTLSPolicy.

## TCPRoute And UDPRoute With OCI Network Load Balancer

Layer 4 support uses an existing OCI Network Load Balancer. The controller reconciles listeners, backend sets, and backends on the referenced NLB, but does not create or delete the NLB resource itself.

Apply the NLB GatewayClass:

```sh
kubectl apply -f deploy/manifests/examples/gatewayclass-nlb.yaml
```

Create a GatewayConfig that points to the existing NLB:

```yaml
apiVersion: oke-gateway-api.gemyago.github.io/v1
kind: GatewayConfig
metadata:
  name: oke-nlb-gateway-config
spec:
  loadBalancerId: ocid1.networkloadbalancer.oc1..exampleuniqueID
```

Create a Gateway with TCP and UDP listeners, then attach matching routes:

```sh
kubectl apply -n <namespace> -f deploy/manifests/examples/gatewayconfig-nlb.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/gateway-nlb.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/l4serverdeployment-nlb.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/tcproute-nlb.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/udproute-nlb.yaml
```

OCI Network Load Balancer backend sets require health checks. For `UDPRoute`,
set `oke-gateway-api.gemyago.github.io/nlb-udp-health-check-port` on each route
to the TCP port the backend Pods expose for health checking. UDP health checks
are not configured by this controller.

`GatewayConfig.spec.loadBalancerId` is shared with ALB usage. The GatewayClass determines whether the OCID is resolved through the OCI Load Balancer API or the OCI Network Load Balancer API.

## TLSRoute

`TLSRoute` supports two OCI-backed modes:

- OCI Load Balancer with `tls.mode: Terminate`: terminates TLS at the ALB and forwards to the backend Service port. Use `BackendTLSPolicy` when the backend connection should also use TLS.
- OCI Network Load Balancer with `tls.mode: Passthrough`: forwards encrypted TCP bytes to a backend that terminates TLS itself.

Unsupported combinations are rejected: ALB passthrough and NLB termination. OCI does not support SNI fanout for ALB TCP+SSL listeners or NLB TCP passthrough listeners, so only one effective `TLSRoute` can own a TLS listener. TLSRoute health checks use TCP on the resolved backend Service port.

Apply the ALB terminate example:

```sh
kubectl apply -n <namespace> -f deploy/manifests/examples/gatewayconfig.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/gatewayclass.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/tlsroute-alb.yaml
```

Apply the NLB passthrough example:

```sh
kubectl apply -n <namespace> -f deploy/manifests/examples/gatewayconfig-nlb.yaml
kubectl apply -f deploy/manifests/examples/gatewayclass-nlb.yaml
kubectl apply -n <namespace> -f deploy/manifests/examples/tlsroute-nlb.yaml
```

## Contributing

Use this section to setup the development environment.

We optionally use OpenSpec, we don't yet commit it to the repo.
```bash
npm install -g @fission-ai/openspec@latest

openspec init
```

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

For local development purposes you can run the controller fully locally pointing on OKE cluster and provision the resources in an actual OCI tenancy.

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

Make sure to `kubectl` configured to point to a target OKE cluster.

Run the controller locally:
```sh
go run ./cmd/controller/ start
```
