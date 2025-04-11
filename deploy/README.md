# Deployments

Install deployment related tools:
```sh
make tools
```

## Kubernetes

Interacting with kubernetes directly is usually done in a local environment. Non local scenarios should usually go via CI/CD pipeline. Use commands below to interact with kubernetes cluster:

```sh
# Create a namespace for the application
kubectl create namespace golang-backend-boilerplate
```

Secret to pull images from a private registry may have to be created. The secret must be created in every namespace where the registry is used. Make sure the token has at least `read:packages` scope.
```sh
# Generic format of the command
kubectl create secret docker-registry ghcr-registry \
  --docker-server=https://ghcr.io \
  --docker-username=<user-name> \
  --docker-password="${GITHUB_TOKEN}" \
  --namespace default

# Use below if you have gh cli configured
kubectl create secret docker-registry ghcr-registry \
  --docker-server=https://ghcr.io \
  --docker-username="$(gh auth status | grep -o "account [^ ]*" | cut -d ' ' -f 2)" \
  --docker-password="$(gh auth token)" \
  --namespace golang-backend-boilerplate

# If you want to delete the secret
kubectl delete secret ghcr-registry --namespace golang-backend-boilerplate
```

## Helm 

Interacting with helm directly is usually done in a local environment. Non local scenarios should usually go via CI/CD pipeline.

Below are the most typical commands you would need to iterate on the charts. Run them from [deploy](.) directory:
```sh
# Render all templates with specific value files to review the output
helm template helm/api-service --debug --name-template api-service -f ./helm/api-service/values.yaml

# Install the chart. Most often you would do it in a local scenario
# Non local scenario should usually go via CI/CD pipeline
helm upgrade api-service helm/api-service \
  --install \
  --namespace golang-backend-boilerplate \
  -f ./helm/api-service/values.yaml \
  --create-namespace \
   --dry-run

# Uninstall the chart
helm uninstall api-service --namespace golang-backend-boilerplate \
  --dry-run
```