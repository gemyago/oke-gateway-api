# Provisioning HTTPS Listeners

In order to provision HTTPS listeners, you need to have a certificate pre-provisioned in advance.
The certificate should be stored in a [TLS secret](https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets) and referenced in the listener configuration.

The controller will watch for the secret updates and will automatically update the underlying load balancer listener with new certificate when it's renewed.

## Using cert-manager

[cert-manager](https://cert-manager.io/) can be used to automate certificate provisioning. Below is a quick guide on how to use it.

Please keep in mind the following points if you plan to use `http01` solver:
* certmanager needs to have gateway api enabled
* loadbalancer needs to allow inbound HTTP traffic and gateway needs to have http listener enabled.
* your domain should resolve to load balancer IP address
* the gateway needs to be provisioned in advance and have http listener enabled.

Above points are only required if you plan to use `http01` solver. The `dns01` solver does not require any of the above.

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

### Provisioning Issuer

Configure issuer as per the [cert-manager documentation](https://cert-manager.io/docs/configuration/). For example using `http01` solver with letsencrypt:

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

### Provisioning Certificate

Create a certificate resource as per the [cert-manager documentation](https://cert-manager.io/docs/configuration/). For example:

```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: oke-gw-example-https-cert
  namespace: oke-gw
spec:
  secretName: oke-gw-example-https-cert
  issuerRef:
    name: oke-gw-example-issuer

  # Make sure to specify your domain here
  # If using http01 challenge, the domain should be accessible
  # from the internet and should point to the load balancer ip
  dnsNames:
  - example.com
```

Make sure to use your domains. Once created, make sure the certificate is in `Ready` state. You wait for the ready state as follows:

```bash
kubectl -n oke-gw wait --for=condition=Ready certificate oke-gw-example-https-cert
```

### Using the certificate

Once the certificate is ready, you can use it to provision HTTPS listener. Full gateway manifest example:

```yaml
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
    # If you want to use http01 challenge, you have to have the http listener
    # enabled. The certmanager will use it to attach verification routes.
    - name: http
      port: 80
      protocol: HTTP

    # Certificate must be provisioned prior to creating the gateway with
    # https config. You may need to comment this section out while provisioning
    # the certificate with http01 verification challenge and then re-enable it.
    - name: https
      port: 443
      protocol: HTTPS
      tls:
        certificateRefs:
          - name: oke-gw-example-https-cert
```

## Manually Creating TLS Secret

A TLS secret secret can be created manually. For example:

```yaml
cat <<EOF | kubectl -n oke-gw apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: oke-gw-example-https-cert
  namespace: oke-gw
type: kubernetes.io/tls
data:
  tls.crt: <base64 encoded certificate>
  tls.key: <base64 encoded private key>
```

Once created, you can reference the secret in the gateway manifest as per earlier examples.