# SparkLabX Notebook

[![Build](https://github.com/sparklabx/sparklabx/actions/workflows/release.yml/badge.svg)](https://github.com/sparklabx/sparklabx/actions/workflows/release.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](./LICENSE)
[![Backend image](https://img.shields.io/badge/ghcr.io-backend-blue?logo=github)](https://github.com/sparklabx/sparklabx/pkgs/container/backend)
[![Kernel image](https://img.shields.io/badge/ghcr.io-kernel-blue?logo=github)](https://github.com/sparklabx/sparklabx/pkgs/container/kernel)

A self-hosted Jupyter-style notebook for **Apache Spark** with built-in S3 storage,
per-user isolation, and OAuth login. Designed to be small, opinionated, and
production-grade enough to drop into a small team or classroom.

Each authenticated user gets their own private Spark kernel (Python / PySpark /
Scala) and their own S3 storage prefix, enforced by real MinIO IAM (not just
app-layer checks).

---

## 30-second quickstart

```bash
git clone https://github.com/sparklabx/sparklabx.git
cd sparklabx
./quickstart.sh
```

The script generates random secrets, pulls public Docker images from
`ghcr.io/sparklabx/`, and starts the stack. It prints the admin password
when done.

Open <http://localhost:3000>, log in as `admin` with the printed password,
and you're in. OAuth is optional — leave the client IDs blank in `.env`
to login via username/password only.

---

## Highlights

- **Spark notebooks in the browser** — PySpark, Scala (Almond), with Monaco
  editor, Markdown cells, kernel restart, package management via
  `spark.jars.packages` / `import $ivy`.
- **Per-user isolation, end-to-end**
  - Single MinIO bucket; each user owns a private prefix `users/<username>/`.
  - On first login, the backend provisions a dedicated MinIO IAM account
    with a scoped policy. The kernel pod runs with that user's credentials —
    so `spark.read.csv("s3a://workspace/users/<someone-else>/...")` returns
    **AccessDenied** at the storage layer, not from app code.
  - User secrets are AES-GCM encrypted at rest.
- **Three kernel deployment modes** — pick the right cost / isolation point
  for your stage. See **[KERNEL_MODE](#kernel_mode)** below.
- **OAuth (Google / Microsoft) + email allowlist** — only invited domains or
  exact emails can sign in.
- **Shared "Public" workspace** — drop datasets everyone can read.
- **Postgres-backed state** — kernel ↔ notebook mappings, idle reaper,
  spawn-phase progress. Restartable backend, no in-memory loss.

---

## Manual setup (if you'd rather skip the script)

```bash
git clone https://github.com/sparklabx/sparklabx.git
cd sparklabx
cp .env.example .env
# Edit .env: replace JWT_SECRET_KEY with `openssl rand -base64 48` output,
# pick a strong SEED_ADMIN_PASSWORD, and optionally fill in OAuth creds.
docker compose up -d
```

The first user ever created is automatically promoted to `superadmin` and
can manage the email allowlist via **Settings → Allowed Domains**.

---

## Architecture

```
┌──────────┐      ┌──────────────┐      ┌──────────────────┐
│ Frontend │─────►│   Backend    │─────►│ Per-user kernel  │
│  (React) │ REST │     (Go)     │ HTTP │  container/pod   │
│          │  WS  │              │ proxy│  (Jupyter+Spark) │
└──────────┘      └──────┬───────┘      └────────┬─────────┘
                         │                       │
                         │ admin API             │ S3A
                         │   IAM provisioning    │ per-user creds
                         ▼                       ▼
                  ┌────────────────────────────────┐
                  │             MinIO              │
                  │  workspace/users/<user>/...    │
                  │  workspace/public/...          │
                  └────────────────────────────────┘
                         │
                         ▼
                  ┌──────────────┐
                  │   Postgres   │
                  └──────────────┘
```

Backend (Go + Gin) responsibilities:

- OAuth verification, JWT issuance, email allowlist enforcement.
- Storage proxy (file browser) — scoped to caller's prefix at the API layer
  as a second line of defense.
- Per-user MinIO IAM provisioning via
  [`madmin-go`](https://github.com/minio/madmin-go).
- Kernel pod / container lifecycle: spawn on demand, reap on idle,
  buffered "last used" touches.
- Notebook persistence, cell execution proxy to Jupyter Kernel Gateway.

Frontend (React + Vite + Monaco):

- Notebook UI with code cells, Markdown, output rendering (DataFrames as
  HTML tables via the kernel-side `display()` / patched `df.show()`).
- File browser sidebar with two scopes: **My Space** (private) and
  **Public** (shared).
- Spark cluster connection dialog (driver/executor memory, package list).

---

## KERNEL_MODE

The single most important configuration. Sets the trust / isolation model.

| Mode | Container layout | Isolation | When to use |
|---|---|---|---|
| `shared` (default) | **One** Jupyter container for everyone | None — cross-user Spark reads are possible | Quick demos, single-user dev |
| `docker_per_user` | **One** container per user on the host's Docker daemon | True MinIO IAM isolation per kernel | Local dev with prod-parity, trusted single-host deployments |
| `k8s_per_user` | **One** pod per user, backend runs in-cluster | True MinIO IAM + K8s NetworkPolicy / ResourceQuota | Production |

Set in `.env`:

```bash
KERNEL_MODE=docker_per_user
KERNEL_POD_IMAGE=ghcr.io/sparklabx/kernel:latest
KERNEL_POD_IDLE_MINUTES=30      # auto-reap after idle
KERNEL_POD_MAX_TOTAL=50
KERNEL_DOCKER_NETWORK=sparklabx_default
```

The shared `jupyter` service in both compose files is gated behind the
`shared` Docker Compose profile, so it stays off unless you ask for it.
Only bring it up when `KERNEL_MODE=shared`:

```bash
docker compose --profile shared up -d
```

For `docker_per_user` and `k8s_per_user`, leave the profile off and the
backend will spawn per-user kernels on demand — the shared container
would just sit idle eating RAM.

`docker_per_user` requires the backend container to have access to the
host Docker socket. `docker-compose.yml` already wires this with
`/var/run/docker.sock:/var/run/docker.sock`.

> **Security note**: mounting `docker.sock` grants the backend process full
> root-equivalent access to the host. Use `docker_per_user` only on hosts
> you trust. For production, use `k8s_per_user` instead.

For `k8s_per_user`, see [`kubernetes/`](./kubernetes/) for the namespace + RBAC + Secret
manifests. The backend pod's ServiceAccount needs `pods`, `pods/log`, and
`pods/exec` in its own namespace — already defined in
`kubernetes/29-backend-rbac.yaml`.

---

## Storage model

A single MinIO bucket (default: `workspace`) holds two top-level prefixes:

```
s3a://workspace/
├── users/
│   ├── alice/      ← alice's private space (R/W for alice only)
│   ├── bob/        ← bob's private space  (R/W for bob only)
│   └── …
└── public/         ← read/write for everyone authenticated
```

Each user's MinIO IAM policy grants:

- `s3:GetObject/PutObject/DeleteObject/...` on `workspace/users/<self>/*`
- Same on `workspace/public/*`
- `s3:ListBucket` on `workspace` with `s3:prefix` scoped to those two trees

Everything else is implicitly denied. There is no way to `s3:CreateBucket`
or read another user's prefix via the Spark kernel — it's enforced at the
S3 protocol layer.

The username slug is derived from the email's local-part (`alice@x.com →
alice`), with a 4-char random suffix on collision.

---

## Configuration

All knobs live in `.env`. See `.env.example` for the full annotated list.
The essentials:

| Variable | Purpose |
|---|---|
| `JWT_SECRET_KEY` | Sign JWTs **and** derive the AES-GCM key for stored MinIO secrets. ≥32 chars recommended. |
| `DATABASE_URL` | Postgres connection string. |
| `SEED_ADMIN_*` | Bootstrap admin (skip if you rely entirely on OAuth). |
| `GOOGLE_CLIENT_ID/SECRET`, `MICROSOFT_CLIENT_ID/SECRET` | OAuth credentials (omit either to disable that provider). |
| `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY` | Root creds the backend uses to provision per-user IAM accounts. |
| `MINIO_WORKSPACE_BUCKET` | Single shared bucket; default `workspace`. |
| `KERNEL_MODE` | See above. |
| `CORS_ORIGINS` | Comma-separated list of allowed frontend origins. |

Frontend OAuth client IDs are baked at build time via `VITE_GOOGLE_CLIENT_ID`
and `VITE_MICROSOFT_CLIENT_ID/TENANT_ID`.

---

## Deploying to Kubernetes

Manifests in [`kubernetes/`](./kubernetes/):

```bash
# Copy + fill in the secret values.
cp kubernetes/10-secrets.example.yaml kubernetes/10-secrets.yaml
# Edit kubernetes/10-secrets.yaml — JWT key, MinIO root creds, OAuth, etc.

kubectl apply -f kubernetes/00-namespace.yaml
kubectl apply -f kubernetes/10-secrets.yaml
kubectl apply -f kubernetes/11-configmap.yaml
kubectl apply -f kubernetes/20-postgres.yaml
kubectl apply -f kubernetes/21-minio.yaml
kubectl apply -f kubernetes/29-backend-rbac.yaml
kubectl apply -f kubernetes/30-backend.yaml
kubectl apply -f kubernetes/31-frontend.yaml
kubectl apply -f kubernetes/40-ingress.yaml
```

Then set `KERNEL_MODE=k8s_per_user` in the backend ConfigMap and roll the
backend. Per-user pods will be spawned on demand in the same namespace.

---

## Development

```bash
# Backend (Go 1.26+)
cd backend
go run ./cmd/server

# Frontend (Node 22+, pnpm or npm)
cd frontend
npm install
npm run dev
```

`docker-compose.test.yml` builds both images locally from the working tree;
use it when iterating on backend changes:

```bash
docker compose -f docker-compose.test.yml up -d --build backend
```

### Project layout

```
backend/
  cmd/server/            # main.go, HTTP server entry
  internal/
    config/              # env-driven config
    database/            # migrations + connection pool
    handlers/            # HTTP route handlers (auth, storage, notebooks, kernel proxy)
    middleware/          # JWT auth, request logger
    services/            # MinIO IAM, kernel gateways (shared / docker / k8s)

frontend/
  src/
    components/Admin/    # User management, settings, allowed domains
    components/Notebooks/  # Notebook page, cell editor, sidebar, etc.
    hooks/               # useJupyterKernel, useNotebook, …
    services/            # Backend API clients

kubernetes/                     # Production manifests
kernel/                  # Notebook kernel image (Dockerfile, entrypoint, Spark config)
                         # Used by all three KERNEL_MODE options
docker-compose.yml       # Reference deployment (uses prebuilt images)
docker-compose.test.yml  # Same but with build: directives for local dev
```

---

## OAuth (optional)

OAuth is off by default — leave the client IDs blank in `.env` to use
username/password login only. To enable Google or Microsoft SSO:

### Google

1. Create an OAuth 2.0 Client ID in
   [Google Cloud Console → Credentials](https://console.cloud.google.com/apis/credentials).
   Application type: **Web application**. Add `http://localhost:3000`
   (or your public URL) to both *Authorized JavaScript origins* and
   *Authorized redirect URIs*.
2. Set in `.env`:
   ```bash
   GOOGLE_CLIENT_ID=<client-id>.apps.googleusercontent.com
   GOOGLE_CLIENT_SECRET=<client-secret>
   VITE_GOOGLE_CLIENT_ID=<client-id>.apps.googleusercontent.com
   ```

### Microsoft

1. Register an app in
   [Microsoft Entra ID → App registrations](https://entra.microsoft.com/).
   Platform: **Single-page application (SPA)**. Redirect URI:
   `http://localhost:3000`. Copy *Application (client) ID* and
   *Directory (tenant) ID*.
2. Set in `.env`:
   ```bash
   MICROSOFT_CLIENT_ID=<application-client-id>
   VITE_MICROSOFT_CLIENT_ID=<application-client-id>
   VITE_MICROSOFT_TENANT_ID=<tenant-id>   # use "common" for multi-tenant
   ```

### Apply the changes

> **Gotcha**: Vite bakes the `VITE_*` variables into the frontend bundle at
> **build time**. Changing them requires a frontend image rebuild — a
> plain `docker compose up -d` won't pick them up.

```bash
docker compose -f docker-compose.test.yml build frontend
docker compose -f docker-compose.test.yml up -d
```

### Email allowlist

The backend rejects OAuth logins from addresses outside the allowlist
(`email_not_allowed`). After your first admin login (username/password),
go to **Settings → Allowed Domains** and add either a domain
(`example.com` — whole org allowed) or an individual email.

---

## Security

- All admin endpoints require a valid JWT bearer token. Tokens are signed
  with `JWT_SECRET_KEY`; rotate it to invalidate all existing sessions.
- The first admin ever created is auto-promoted to `superadmin`. Only
  superadmins can edit settings (email allowlist, etc.).
- Stored MinIO secrets are AES-GCM encrypted with a key derived from
  `JWT_SECRET_KEY` (SHA-256 → 32 bytes). Rotating `JWT_SECRET_KEY` makes
  existing per-user secrets unreadable — users will be re-provisioned on
  next login.
- The `docker_per_user` kernel mode mounts `/var/run/docker.sock`. **This
  is root-equivalent on the host** — do not use it in shared / untrusted
  environments. Use `k8s_per_user` for production.
- Path traversal is rejected at the storage handler boundary (`..` and
  leading `/` are stripped before joining with the user's prefix).
- Email allowlist is enforced before any DB write — unallowed addresses
  cannot even create an admin row.

---

## Community & support

- **Website**: <https://sparklabx.com>
- **Issues / discussions**: <https://github.com/sparklabx/sparklabx>
- **Security reports**: see [SECURITY.md](./SECURITY.md)

## License

Apache License 2.0 — see [LICENSE](./LICENSE).

Third-party dependencies retain their respective licenses; some (notably
[MinIO server](https://github.com/minio/minio) and
[`madmin-go`](https://github.com/minio/madmin-go)) are licensed under
**AGPL-3.0**. If you distribute a compiled binary that statically links
those packages, that binary's combined license is AGPL-3.0 — comply with
its source-availability terms when redistributing.
