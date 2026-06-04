# SparkLabX — Kubernetes deployment

Reference manifests for running SparkLabX on a real cluster with
`KERNEL_MODE=k8s_per_user` (one Jupyter pod per user, true MinIO IAM isolation).

For local development, use `docker compose` from the repo root instead.

## Prerequisites

- Kubernetes cluster, v1.25 or newer
- `kubectl` configured against the cluster
- A `StorageClass` that supports `ReadWriteOnce` (default in most clusters)
- An ingress controller (e.g. [ingress-nginx](https://kubernetes.github.io/ingress-nginx/))
- Optional: [cert-manager](https://cert-manager.io/) with a `ClusterIssuer` named
  `letsencrypt-prod` for automatic TLS

## Setup

### 1. Namespace + secrets

```bash
kubectl apply -f 00-namespace.yaml

cp 10-secrets.example.yaml 10-secrets.yaml
$EDITOR 10-secrets.yaml      # fill JWT key, admin password, MinIO root, OAuth
kubectl apply -f 10-secrets.yaml
```

The images at `ghcr.io/sparklabx/*` are public — no image-pull secret is
needed. For private forks, create one and uncomment the `imagePullSecrets`
block in `30-backend.yaml` / `31-frontend.yaml`:

```bash
kubectl -n sparklabx create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=<your-github-user> \
  --docker-password=<github-pat-with-read:packages>
```

### 2. Configure your domain + OAuth IDs

```bash
$EDITOR 11-configmap.yaml    # CORS_ORIGINS, OAuth client IDs, KERNEL_MODE
$EDITOR 40-ingress.yaml      # public hostname + TLS
```

### 3. Deploy

```bash
kubectl apply -f .
```

Or apply individually in order:

```bash
kubectl apply -f 11-configmap.yaml
kubectl apply -f 20-postgres.yaml
kubectl apply -f 21-minio.yaml
kubectl apply -f 29-backend-rbac.yaml
kubectl apply -f 30-backend.yaml
kubectl apply -f 31-frontend.yaml
kubectl apply -f 40-ingress.yaml
kubectl apply -f 50-postgres-backup.yaml
```

### 4. Bootstrap the backups bucket

```bash
kubectl exec -n sparklabx -it statefulset/minio -- \
  mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
kubectl exec -n sparklabx -it statefulset/minio -- \
  mc mb local/sparklabx-backups
```

### 5. Verify

```bash
kubectl -n sparklabx get pods
kubectl -n sparklabx logs deploy/backend --tail=50
curl -k https://your-domain.com/health
```

## Known limitations

- **Backend is single-replica** (`Recreate` strategy). The WebSocket kernel
  proxy keeps state in memory; scaling out requires moving state to Redis or
  accepting reconnects on shard switch.
- **MinIO is single-replica StatefulSet**. For real HA, switch to distributed
  MinIO (4+ pods) or use external object storage.
- **Postgres is single-replica**. Use a managed Postgres (RDS / CloudSQL) for
  production HA. The hourly backup CronJob is a stopgap, not HA.

## Common ops

Tail logs:
```bash
kubectl -n sparklabx logs -f deploy/backend
```

Restart backend (after editing secrets / config):
```bash
kubectl -n sparklabx rollout restart deploy/backend
```

Trigger a backup manually:
```bash
kubectl -n sparklabx create job --from=cronjob/postgres-backup postgres-backup-manual-$(date +%s)
```

Restore from a backup:
```bash
# 1. Pull a dump out of MinIO into the pod
kubectl exec -n sparklabx -it statefulset/minio -- \
  mc cp local/sparklabx-backups/db/dump-YYYYMMDD-HHMMSS.sql.gz /tmp/restore.sql.gz

# 2. Pipe it into Postgres
kubectl exec -n sparklabx -it statefulset/postgres -- \
  sh -c "gunzip -c /tmp/restore.sql.gz | psql -U sparklabx sparklabx"
```

Scale the frontend horizontally (safe — stateless):
```bash
kubectl -n sparklabx scale deploy/frontend --replicas=4
```

Pin to a specific backend image tag (avoid `:latest` in prod):
```bash
kubectl -n sparklabx set image deploy/backend backend=ghcr.io/sparklabx/backend:v1.0.0
```
