# Provisioning HTTPS Listeners

In order to provision HTTPS listeners, you need to have a certificate pre-provisioned in advance.
The certificate must be stored in a secret and referenced in the listener configuration.

## Using cert-manager

The [cert-manager](https://cert-manager.io/) allows provisioning certificates in a fully automated way. Please install the cert manager in your cluster.

When installing the cert manager, please keep in mind the following points:
* certmanager needs to have gateway api enabled
* loadbalancer needs to allow inbound traffic for http01 challenges (certmanager requirement)

If installing cert-manager with helm, you can use the below command:

```yaml
helm install \
  cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --version v1.17.2 \
  --set crds.enabled=true \
  --set config.enableGatewayAPI=true
```

Please refer to the [cert-manager documentation](https://cert-manager.io/docs/installation/) for more details on the installation.

## Provisioning a certificate

Configure issuer as per the [cert-manager documentation](https://cert-manager.io/docs/configuration/). For example:

```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: oke-gw-example-issuer
  namespace: oke-gw
spec:
  acme:
    # You must replace this email address with your own.
    # Let's Encrypt will use this to contact you about expiring
    # certificates, and issues related to your account.
    email: main@example.com

    # Prefer testing against staging prior to enabling production
    server: https://acme-staging-v02.api.letsencrypt.org/directory

    privateKeySecretRef:
      name: oke-gw-example-issuer
    solvers:
    - http01:
        # Make sure to enable gateway api for certmanager
        gatewayHTTPRoute:
          parentRefs:
            - name: oke-gateway
              namespace: oke-gw
              kind: Gateway
EOF
```

**Notes**:
* Make sure to replace the email address with your own.
* If using letsencrypt, prefer testing against staging
* If using http01 solver, make sure to allow inbound traffic for http01 challenges and have http listener on the gateway
