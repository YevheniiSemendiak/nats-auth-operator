# JWT-Based Authentication Example

This example demonstrates full NATS JWT authentication with operator/account/user hierarchy and fine-grained permissions.

## What's Included

1. **01-auth-config.yaml** — `NatsAuthConfig` in JWT mode. Creates the operator keypair and system account automatically.
2. **02-accounts.yaml** — `myapp-account` with JetStream limits enabled.
3. **03-users.yaml** — Four users: `app-user`, `consumer-user`, `admin-user` (all under `myapp-account`), and `system-admin` (under the auto-managed system account).
4. **stream-based/** — More granular JetStream permission examples.

## Architecture

```
NatsAuthConfig/main
  └── NatsAccount/main-system-account  (auto-created)
        └── NatsUser/system-admin

  └── NatsAccount/myapp-account
        ├── NatsUser/app-user           (general app access)
        ├── NatsUser/consumer-user      (JetStream consumer)
        └── NatsUser/admin-user         (full access)
```

The operator writes all JWTs to the `nats-auth-jwts` Secret with the following keys:
- `operator` — operator JWT
- `main-system-account` — system account JWT
- `myapp-account` — application account JWT

## Quick Start

### 1. Apply resources

```bash
kubectl apply -f 01-auth-config.yaml
kubectl apply -f 02-accounts.yaml
kubectl apply -f 03-users.yaml
```

Wait for everything to be ready:

```bash
kubectl wait --for=condition=Ready natsauthconfig/main --timeout=60s
kubectl get natsaccounts
kubectl get natsusers
```

### 2. Deploy NATS

Reference `nats-auth-jwts` from your NATS Helm values. See [nats-values.yaml](../nats-values.yaml) for a complete working example.

Key fields in your NATS Helm values:

```yaml
config:
  merge:
    operator: << $NATS_OPERATOR_JWT >>
    system_account: "<main-system-account public key>"
    resolver_preload:
      "<main-system-account public key>": << $NATS_SYSTEM_ACCOUNT_JWT >>
      "<myapp-account public key>": << $NATS_MYAPP_ACCOUNT_JWT >>

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
          key: main-system-account
    NATS_MYAPP_ACCOUNT_JWT:
      valueFrom:
        secretKeyRef:
          name: nats-auth-jwts
          key: myapp-account
```

Get the public keys after the operator has reconciled:

```bash
# System account public key (for system_account and resolver_preload)
kubectl get natsaccount main-system-account -o jsonpath='{.status.accountId}'

# myapp account public key (for resolver_preload)
kubectl get natsaccount myapp-account -o jsonpath='{.status.accountId}'
```

### 3. Use credentials

Credentials are stored in `<user-name>-user-creds` Secrets:

```bash
# Get the credentials file for app-user
kubectl get secret app-user-user-creds -o jsonpath='{.data.user\.creds}' | base64 -d > /tmp/app-user.creds

# Test connection
nats -s nats://nats:4222 --creds=/tmp/app-user.creds pub app.hello world
```

Mount credentials in a pod:

```yaml
spec:
  containers:
  - name: app
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

## Updating Permissions or Limits

Edit the resource — the operator detects the generation change and re-issues the JWT automatically:

```bash
kubectl edit natsuser app-user
kubectl edit natsaccount myapp-account
```

To force an immediate reconcile without changing the spec:

```bash
kubectl annotate natsuser app-user nats.jradikk/force-sync=$(date +%s) --overwrite
```

## System Account

`main-system-account` is created automatically by the operator. Use `system-admin` user credentials for server management commands:

```bash
kubectl get secret system-admin-user-creds -o jsonpath='{.data.user\.creds}' | base64 -d > /tmp/system-admin.creds
nats -s nats://nats:4222 --creds=/tmp/system-admin.creds server report accounts
nats -s nats://nats:4222 --creds=/tmp/system-admin.creds server ping
```

## Cleanup

```bash
kubectl delete -f 03-users.yaml
kubectl delete -f 02-accounts.yaml
kubectl delete -f 01-auth-config.yaml
```

> Deleting `NatsAuthConfig` cascades to the system account it owns. All other `NatsAccount` and `NatsUser` resources are independent and must be deleted separately.
