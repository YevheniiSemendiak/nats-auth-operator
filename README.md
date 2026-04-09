# NATS Authentication Operator

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/jradikk/nats-auth-operator)](https://goreportcard.com/report/github.com/jradikk/nats-auth-operator)

A Kubernetes operator for managing NATS authentication using JWT and token-based authentication. This operator provides a modern, declarative way to manage NATS users and accounts as Kubernetes custom resources.

> **Note:** This operator manages only authentication - it does NOT manage NATS clusters, streams, or consumers. Use the [NATS Helm chart](https://github.com/nats-io/k8s/tree/main/helm/charts/nats) for cluster management and [NACK](https://github.com/nats-io/nack) for JetStream resource management.

## Features

### Authentication Modes
- **JWT-based authentication** - Full NATS Operator → Accounts → Users hierarchy with automatic JWT generation
- **Token-based authentication** - Simple username/password credentials
- **JetStream support** - Built-in JetStream limits configuration for accounts

### Key Capabilities
- 🔐 **Automatic credential generation** - Operator keypairs, account JWTs, user JWTs, and `.creds` files
- 🎯 **Fine-grained permissions** - Publish/subscribe controls with allow/deny lists
- 🔄 **Seamless NATS Helm integration** - Works alongside official NATS Helm chart without conflicts
- 📦 **Kubernetes-native** - Manage authentication using `kubectl` and GitOps workflows
- 🛡️ **Production-ready** - Idempotent reconciliation, finalizers, status conditions, secure secret storage
- ⚡ **Automatic change propagation** - Spec changes (permissions, limits) are detected and re-applied on the next reconcile
- 🔁 **Force-sync annotation** - Trigger an immediate reconcile on any resource without waiting

## Quick Start

### Installation

#### Using Helm (Recommended)

```bash
helm install nats-auth-operator oci://ghcr.io/jradikk/charts/nats-auth-operator \
  --version 0.1.0 \
  --namespace nats-system \
  --create-namespace
```

#### Using kubectl

```bash
kubectl apply -f https://raw.githubusercontent.com/jradikk/nats-auth-operator/main/dist/install.yaml
```

### Basic Usage

1. **Create an auth configuration:**

```yaml
apiVersion: nats.jradikk/v1alpha1
kind: NatsAuthConfig
metadata:
  name: main
  namespace: default
spec:
  natsURL: "nats://nats.default.svc.cluster.local:4222"
  mode: jwt
  serverAuthConfig:
    name: "nats-auth-jwts"
    namespace: "default"
    type: "Secret"
  jwt:
    operatorName: "MyOperator"
```

2. **Create accounts:**

```yaml
apiVersion: nats.jradikk/v1alpha1
kind: NatsAccount
metadata:
  name: myapp-account
  namespace: default
spec:
  authConfigRef:
    name: main
  description: "Application account with JetStream"
  limits:
    conn: 100
    subs: 1000
    jetstream:
      memoryStorage: -1  # Unlimited
      diskStorage: -1
      streams: -1
      consumer: -1
```

3. **Create users:**

```yaml
apiVersion: nats.jradikk/v1alpha1
kind: NatsUser
metadata:
  name: app-user
  namespace: default
spec:
  authConfigRef:
    name: main
  authType: jwt
  accountRef:
    name: myapp-account
  username: "app-user"
  permissions:
    publishAllow:
      - "app.>"
      - "$JS.ACK.>"
      - "_INBOX.>"
    subscribeAllow:
      - "app.>"
      - "$JS.API.>"
      - "_INBOX.>"
```

4. **Use the generated credentials:**

The operator creates a Secret `app-user-user-creds` containing:
- `user.creds` - NATS credentials file (JWT + seed)
- `user.jwt` - User JWT
- `NATS_URL` - NATS server URL

Mount this secret in your pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: myapp
spec:
  containers:
  - name: app
    image: myapp:latest
    volumeMounts:
    - name: nats-creds
      mountPath: /var/run/nats
      readOnly: true
    env:
    - name: NATS_CREDS_FILE
      value: /var/run/nats/user.creds
  volumes:
  - name: nats-creds
    secret:
      secretName: app-user-user-creds
```

## Architecture

### Custom Resource Definitions (CRDs)

1. **NatsAuthConfig** - Global authentication configuration
   - Defines authentication mode (JWT/token)
   - Specifies NATS server URL
   - References where to store generated credentials

2. **NatsAccount** - NATS account (JWT mode only)
   - Defines account limits (connections, subscriptions, payload size)
   - Configures JetStream limits (storage, streams, consumers)
   - Generates account JWT signed by operator

3. **NatsUser** - NATS user
   - Defines user permissions (publish/subscribe allow/deny lists)
   - Generates user JWT or username/password credentials
   - Creates Kubernetes Secret with credentials

### How It Works

```
┌─────────────────────┐
│  NatsAuthConfig     │ Creates operator keypair
│  (JWT mode)         │ Stores in Secret
└──────────┬──────────┘
           │
           ├───────────────────┐
           │                   │
   ┌───────▼────────┐  ┌──────▼───────┐
   │  NatsAccount   │  │ NatsAccount  │ Generate account
   │  (system)      │  │ (myapp)      │ JWTs, store in
   └───────┬────────┘  └──────┬───────┘ individual Secrets
           │                   │
           │         ┌─────────┴──────────┐
           │         │                    │
       ┌───▼────┐ ┌──▼─────┐       ┌─────▼────┐
       │  User  │ │  User  │  ...  │  User    │ Generate user
       │  (u1)  │ │  (u2)  │       │  (un)    │ JWTs + creds
       └────────┘ └────────┘       └──────────┘ files
           │         │                   │
           └─────────┴───────────────────┘
                     │
          ┌──────────▼───────────┐
          │  nats-auth-jwts      │ Aggregated Secret
          │  Secret              │ referenced by NATS
          │  - operator JWT      │ Helm chart
          │  - system-account    │
          │  - myapp-account     │
          └──────────────────────┘
```

## Integration with NATS Helm Chart

The operator is designed to work seamlessly with the official NATS Helm chart.

### Deploy NATS

After the operator has reconciled and `nats-auth-jwts` Secret exists:

```bash
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm upgrade --install nats nats/nats -f examples/nats-values.yaml
```

See [`examples/nats-values.yaml`](./examples/nats-values.yaml) for a complete working Helm values file.

Key fields to configure in your values:

```yaml
config:
  merge:
    operator: << $NATS_OPERATOR_JWT >>
    system_account: "<main-system-account public key>"
    resolver_preload:
      "<main-system-account public key>": << $NATS_SYSTEM_ACCOUNT_JWT >>

container:
  env:
    NATS_OPERATOR_JWT:
      valueFrom:
        secretKeyRef:
          name: nats-auth-jwts
          key: operator
    NATS_SYSTEM_ACCOUNT_JWT:
      valueFrom:
        secretKeyRef:
          name: nats-auth-jwts
          key: main-system-account  # key = <authconfig-name>-system-account
```

Get the system account public key after the operator reconciles:

```bash
kubectl get natsaccount main-system-account -o jsonpath='{.status.accountId}'
```

**No conflicts:** the operator manages credentials (Secrets), the NATS Helm chart manages the server (ConfigMaps, StatefulSet). Each owns distinct resources.

## Examples

See the [`examples/`](./examples) directory for complete examples:

- [`jwt-auth/`](./examples/jwt-auth) - JWT-based authentication with JetStream
- [`token-auth/`](./examples/token-auth) - Token-based authentication

## JetStream Permissions

When using JetStream, users need specific permissions:

### For JetStream Consumers:

```yaml
publishAllow:
  - "$JS.ACK.<stream-name>.>"    # Acknowledge messages
  - "$JS.API.>"                  # JetStream API calls
  - "_INBOX.>"                   # Request-response

subscribeAllow:
  - "your.subjects.>"            # Stream subjects
  - "$JS.API.>"                  # JetStream API
  - "_INBOX.>"                   # Message delivery
```

### For Stream-Specific Access:

```yaml
publishAllow:
  - "$JS.ACK.mystream.>"
  - "$JS.API.CONSUMER.*.mystream.>"

subscribeAllow:
  - "$JS.API.CONSUMER.*.mystream.>"
  - "$JS.API.STREAM.INFO.mystream"
  - "_INBOX.>"
```

## Force-Sync

Annotate any resource with `nats.jradikk/force-sync` to trigger an immediate full reconcile, bypassing the idempotency guards. The annotation is removed automatically after processing.

```bash
# Force re-generate a user JWT (e.g., after manually editing permissions)
kubectl annotate NatsUser my-user nats.jradikk/force-sync=$(date +%s) --overwrite

# Force re-generate an account JWT (e.g., after changing limits)
kubectl annotate NatsAccount my-account nats.jradikk/force-sync=$(date +%s) --overwrite

# Force NatsAuthConfig to re-collect and re-publish all account JWTs
kubectl annotate NatsAuthConfig my-config nats.jradikk/force-sync=$(date +%s) --overwrite
```

> Under normal operation this is not needed — spec changes are detected automatically via the resource `generation`. Use force-sync when you have edited secrets manually or need to verify the operator is in sync.

## Troubleshooting

### Account IDs Keep Changing

**Problem:** Account IDs rotate constantly, causing authentication failures.

**Cause:** Infinite reconciliation loop due to account status/secret mismatch.

**Solution:** The operator now verifies that the account ID in status matches the seed in the secret. Rebuild and redeploy the operator.

### "JetStream not enabled for account" Error

**Problem:** JetStream operations fail with error code 10039.

**Solution:** Add JetStream limits to your NatsAccount:

```yaml
limits:
  jetstream:
    memoryStorage: -1
    diskStorage: -1
    streams: -1
    consumer: -1
```

Even with all values set to `-1` (unlimited), the presence of the `jetstream` section enables JetStream for the account.

### "Authorization Violation" with JetStream

**Problem:** Client gets "Authorization Violation" when using JetStream consumers.

**Possible Causes:**

1. **Missing publish permissions for acknowledgments:**
   ```yaml
   publishAllow:
     - "$JS.ACK.>"      # Required!
     - "_INBOX.>"       # Required!
   ```

2. **Missing subscribe permissions for JetStream API:**
   ```yaml
   subscribeAllow:
     - "$JS.API.>"      # Required!
     - "_INBOX.>"       # Required!
   ```

3. **Credentials not mounted correctly:**
   - Ensure secret is mounted as a file
   - Application must use `user.creds` file, not `user.jwt`
   - Check NATS server logs for "authentication error"

### Secret Missing Account JWT

**Problem:** The main Secret (e.g., `nats-auth-jwts`) is missing an account JWT.

**Solution:**

1. Check if NatsAccount is Ready:
   ```bash
   kubectl get natsaccounts
   ```

2. Check if individual account Secret exists:
   ```bash
   kubectl get secret <account-name>-account-jwt
   ```

3. Trigger reconciliation with the force-sync annotation:
   ```bash
   kubectl annotate natsauthconfig main nats.jradikk/force-sync=$(date +%s) --overwrite
   ```
   The operator will re-collect all account JWTs and rewrite the Secret.

### Account ID Mismatch Between JWT and Status

**Problem:** The account public key in the JWT doesn't match the status.

**Solution:** Delete the account JWT secret to force regeneration:
```bash
kubectl delete secret <account-name>-account-jwt
```

The operator will regenerate the JWT using the existing seed.

### User Credentials Keep Regenerating

**Problem:** User credentials change on every reconciliation.

**Cause:** `status.observedGeneration` is out of sync with the actual resource generation, causing the controller to treat every reconcile as a spec change.

**Solution:** Check whether the status is being updated correctly. If the problem persists, delete and recreate the user resource to reset the generation counter.

### NATS Server Shows "authentication error"

**Problem:** NATS logs show authentication errors despite correct JWT.

**Possible Causes:**

1. **JWT signed by wrong account** - Check issuer in JWT matches account ID
2. **Stale credentials** - Application using old credentials file
3. **Account not in operator's resolver** - Check main Secret has the account JWT

**Debug:**
```bash
# Check what's in the main Secret
kubectl get secret nats-auth-jwts -o jsonpath='{.data}' | jq 'keys'

# Decode and inspect a JWT
kubectl get secret <user>-user-creds -o jsonpath='{.data.user\.jwt}' | base64 -d
```

## Development

### Prerequisites

- Go 1.24+
- Kubernetes cluster (kind, minikube, or real cluster)
- kubectl
- Docker or Podman

### Building

```bash
# Build the operator
make build

# Build Docker image
make docker-build IMG=myregistry/nats-auth-operator:latest

# Push Docker image
make docker-push IMG=myregistry/nats-auth-operator:latest
```

### Running Locally

```bash
# Install CRDs
make install

# Run operator locally (against your current kubectl context)
make run
```

### Testing

```bash
# Run tests
make test

# Run with coverage
make test-coverage
```

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## License

Apache 2.0 License. See [LICENSE](LICENSE) for details.

## Acknowledgments

- Built with [Kubebuilder](https://github.com/kubernetes-sigs/kubebuilder)
- NATS JWT library: [nats-io/jwt](https://github.com/nats-io/jwt)
- NATS nkeys library: [nats-io/nkeys](https://github.com/nats-io/nkeys)
