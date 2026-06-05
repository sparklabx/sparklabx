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
| `secrets.create` | `true` | Set to `false` and point `secrets.existingSecret` at a Secret you manage out-of-band. |
| `image.backend.tag` | `latest` | Pin to a semver tag in production. |
| `postgres.persistence.size` | `10Gi` | Grow before you fill it; PVC resize requires CSI support. |
| `minio.persistence.size` | `50Gi` | Bump for big datasets. |
| `ingress.host` | `sparklabx.example.com` | Required if `ingress.enabled=true`. |

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
