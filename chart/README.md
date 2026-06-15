# sparklabx

Self-hosted Apache Spark notebooks with per-user isolation. See the
[main project README](../README.md) for background.

## Install

```bash
helm install sparklabx ./chart \
  --namespace sparklabx --create-namespace \
  --set secrets.jwtSecretKey="$(openssl rand -base64 48)" \
  --set secrets.seedAdmin.password="$(openssl rand -base64 16)" \
  --set secrets.minio.rootPassword="$(openssl rand -base64 24)" \
  --set ingress.host=notebook.example.com
```

For more than two secrets, write a `values.yaml`:

```yaml
# my-values.yaml
secrets:
  jwtSecretKey: "<openssl rand -base64 48>"
  seedAdmin:
    password: "<strong-password>"
  minio:
    rootPassword: "<openssl rand -base64 24>"
  google:
    clientId: "..."
    clientSecret: "..."

kernelMode: k8s_per_user
ingress:
  host: notebook.example.com

postgres:
  persistence: { size: 20Gi }
minio:
  persistence: { size: 200Gi }
```

```bash
helm install sparklabx ./chart -n sparklabx --create-namespace -f my-values.yaml
```

## Cluster requirements

| Requirement | Why | Workaround |
|---|---|---|
| Default StorageClass with dynamic provisioning | Postgres + MinIO PVCs | Set `postgres.persistence.storageClassName` / `minio.persistence.storageClassName` explicitly |
| Ingress controller (nginx, traefik) | Public access | Set `ingress.enabled=false` and port-forward |
| cert-manager + `letsencrypt-prod` | Automatic TLS | Set `ingress.tls.certManagerClusterIssuer=""` and provision the Secret yourself, or `ingress.tls.enabled=false` for HTTP-only |

## Common values

| Key | Default | Notes |
|---|---|---|
| `kernelMode` | `k8s_per_user` | One of `shared`, `docker_per_user`, `k8s_per_user`. RBAC for the backend is only created when `k8s_per_user`. |
| `kernel.idleMinutes` | `30` | Idle reaper cuts kernels after N minutes. |
| `kernel.resources.*` | `500m/1Gi req · 2000m/4Gi limit` | Per-user kernel pod CPU/memory ceiling. |
| `secrets.create` | `true` | Set to `false` and point `secrets.existingSecret` at a Secret you manage out-of-band. |
| `image.backend.tag` | `latest` | Pin to a semver tag in production. |
| `postgres.enabled` | `true` | `false` → use managed Postgres; fill `postgres.external.host`. |
| `postgres.persistence.enabled` | `true` | `false` → emptyDir (data lost on pod restart). For evaluation / CI only. |
| `postgres.persistence.storageClass` | `""` | `""` = cluster default. Pick a class (e.g., `gp3-iops`) for performance tiers. |
| `postgres.persistence.size` | `10Gi` | Grow before you fill it; PVC resize requires CSI support. |
| `postgres.persistence.accessModes` | `[ReadWriteOnce]` | Override for shared-storage clusters (`ReadWriteMany`). |
| `postgres.persistence.annotations` | `{}` | Tag the PVC — e.g., `{velero.io/backup-volumes: data}` for backup tooling. |
| `minio.enabled` | `true` | `false` → use existing S3-compatible endpoint; fill `minio.external.endpoint`. |
| `minio.persistence.enabled` | `true` | Same semantics as `postgres.persistence.enabled`. |
| `minio.persistence.size` | `50Gi` | Bump for big datasets. |
| `ingress.host` | `sparklabx.example.com` | Required if `ingress.enabled=true`. |
| `frontend.service.type` | `ClusterIP` | `ClusterIP` / `NodePort` / `LoadBalancer`. Use `NodePort` when there's no ingress controller; `LoadBalancer` on cloud providers. |
| `frontend.service.nodePort` | `""` | Static port (30000-32767) when `type=NodePort`. Empty → K8s picks a random port. |
| `frontend.service.loadBalancerIP` | `""` | Pin a specific public IP when `type=LoadBalancer`. |
| `sso.issuerUrl` / `sso.clientId` / `sso.clientSecret` | `""` | Enterprise OIDC SSO (Keycloak/Okta/Auth0/Azure AD/…). Set all three to enable the "Sign in with SSO" button. |
| `sso.providerName` | `SSO` | Label on the SSO login button. |
| `sso.redirectUrl` / `sso.postLoginRedirect` | `""` | Default to the ingress host (`https://<ingress.host>/api/v1/auth/oidc/callback` and `https://<ingress.host>`). Set explicitly if not using the chart ingress. |
| `trino.url` | `""` | Default Trino JDBC URL for the `trino()` notebook helper + Trino catalog sidebar. With SSO on, the user's token is passed through. |

### Enterprise SSO (OIDC) + Trino

Enable SSO by setting the issuer, client ID, and client secret — register
`https://<ingress.host>/api/v1/auth/oidc/callback` as the redirect URI at your IdP:

```bash
helm upgrade --install sparklabx ./chart \
  --set sso.issuerUrl=https://keycloak.corp/realms/company \
  --set sso.clientId=sparklabx \
  --set sso.clientSecret="$OIDC_CLIENT_SECRET" \
  --set trino.url='jdbc:trino://trino.corp:443?SSL=true'
```

The SSO button is runtime-driven (no frontend rebuild). With `trino.url` set, every
kernel gets the `trino()` helper and the notebook shows a Trino catalog browser;
when SSO is on, queries run as the logged-in user via OIDC token passthrough
(`KERNEL_CALLBACK_URL` is wired to the in-cluster backend Service automatically).

### Using an external Postgres or S3

Production deployments often already have managed Postgres (RDS, Cloud SQL,
Aurora) or S3 storage. Skip the in-cluster StatefulSet and point the backend
at your existing services:

```yaml
postgres:
  enabled: false
  external:
    host: my-database.us-east-1.rds.amazonaws.com
    port: 5432
  database:
    name: sparklabx
    user: sparklabx
    password: "<from secrets manager>"
  sslMode: require  # auto-defaults to "require" when enabled=false

minio:
  enabled: false
  external:
    endpoint: https://minio.internal.corp:9000
  bucket: workspace

secrets:
  minio:
    rootUser: "<S3 access key>"
    rootPassword: "<S3 secret key>"
```

**Caveats for external S3:**

- The bucket (`minio.bucket`) must exist before installing — the chart only
  references it, doesn't create it.
- Per-user IAM provisioning uses MinIO's admin API (`madmin-go`). Fully
  supported against MinIO. Against AWS S3 / GCS / R2 the backend falls
  back to root credentials shared across all kernels (no IAM isolation).
- For real per-user isolation on AWS, use IAM Roles for Service Accounts
  (IRSA) and policy-scoped roles — outside the chart's scope today.

Full list: see [`values.yaml`](./values.yaml).

## Render without installing

Inspect what the chart would apply:

```bash
helm template sparklabx ./chart -f my-values.yaml > rendered.yaml
```

Useful for review, GitOps (commit the output to Argo/Flux), or for users
who don't want to run `helm install` directly.

## Upgrade

```bash
helm upgrade sparklabx ./chart -n sparklabx -f my-values.yaml
```

PVCs are preserved across upgrades. Backend handles its own migrations
at startup, so no separate migration job is needed.

## Uninstall

```bash
helm uninstall sparklabx -n sparklabx
```

**PVCs are NOT deleted automatically** — your notebook data + Postgres
state survive. To wipe everything:

```bash
kubectl -n sparklabx delete pvc -l app.kubernetes.io/instance=sparklabx
```
