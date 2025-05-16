# Deployments

This folder contains deployment related tools and scripts. You may need to update files in this folder if you are working on the deployment related tasks.

Install deployment related tools:
```sh
make tools
```

## Publish helm chart

Helm chart is built and published automatically with each release. Steps below are for local testing. Run the following from deploy directory:

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

