# Deployments

This folder contains deployment related tools and scripts. You may need to update files in this folder if you are working on the deployment related tasks.

Install deployment related tools:
```sh
make tools
```

## Install example resources

```sh
# Install CRDs directly from the helm chart
kubectl apply -f helm/controller/templates/gateway-config-crd.yaml

# Actualize load balancer OCID in the gatewayconfig prior to applying
kubectl apply -n oke-gw -f manifests/examples/gatewayconfig.yaml

kubectl apply -n oke-gw -f manifests/examples/gatewayclass.yaml
kubectl apply -n oke-gw -f manifests/examples/gateway.yaml
kubectl apply -n oke-gw -f manifests/examples/serverdeployment.yaml
kubectl apply -n oke-gw -f manifests/examples/serverroutes.yaml
```

## Helm install options

The chart installs both the CRD and the controller resources by default.

```sh
# Install everything (default behavior)
helm install oke-gateway-api-controller ./helm/controller

# Install only the CRD
helm install oke-gateway-api-controller ./helm/controller \
  --set deployment.enabled=false

# Install only the controller resources
helm install oke-gateway-api-controller ./helm/controller \
  --set crds.install=false
```

## OCI certificate example

Use these manifests when the HTTPS listener should reference an OCI Certificates Service
certificate created outside Kubernetes, such as by Terraform. The `https` listener intentionally
does not set `tls.certificateRefs`; the certificate OCID is set with the listener TLS option
`oci.oraclecloud.com/certificate-ocid`.

```sh
kubectl apply -n oke-gw -f manifests/examples/gatewayconfig.yaml
kubectl apply -n oke-gw -f manifests/examples/gatewayclass.yaml
kubectl apply -n oke-gw -f manifests/examples/gateway-https-oci-certificate.yaml
kubectl apply -n oke-gw -f manifests/examples/serverdeployment.yaml
kubectl apply -n oke-gw -f manifests/examples/serverroutes.yaml
```

## Publish helm chart

Helm chart is built and published automatically with each release. Steps below are for local testing. Run the following from deploy directory.

Release tooling keeps the chart `version` in sync with `appVersion`, without the leading `v`.

```sh
# Login to ghcr registry (assuming you have gh cli configured)
gh auth token | helm registry login ghcr.io -u $(gh auth status | grep -o "account [^ ]*" | cut -d ' ' -f 2) --password-stdin

# Package the chart
helm package helm/controller/ -d tmp/

# Get the chart version
CHART_VERSION=$(helm show chart helm/controller/ | grep 'version:' | cut -d' ' -f2)

# Push the chart to the registry
helm push tmp/oke-gateway-api-controller-${CHART_VERSION}.tgz oci://ghcr.io/gemyago/helm-charts
```
