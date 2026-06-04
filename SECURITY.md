# Security Policy

## Reporting a vulnerability

If you find a security issue in SparkLabX, please report it privately so we
can fix it before it becomes public:

- Email: **security@sparklabx.com**

Please **do not** open a public GitHub issue for security problems.

### What to include

- A description of the vulnerability and the impact you think it has.
- Steps to reproduce (proof-of-concept code, screenshots, or a short video).
- The version / commit SHA you tested against.
- Optionally: your suggested fix.

### What to expect

- We will acknowledge your report within 72 hours.
- We aim to ship a fix within 14 days for critical issues, longer for low-severity.
- We will credit you in the release notes unless you ask to stay anonymous.

## Supported versions

Only the `main` branch and the most recent tagged release receive security
patches. Older releases are best-effort.

## Threat model

SparkLabX assumes:

- **Authenticated users may be adversarial** — the per-user MinIO IAM isolation
  is the primary control. Spark code in one user's notebook cannot read another
  user's S3 prefix at the storage layer (not just at the app layer).
- **The backend has root MinIO credentials** — a compromised backend can read
  any user's data. Protect the backend's secret material accordingly.
- **`docker_per_user` mode is trusted-host only** — mounting `/var/run/docker.sock`
  gives the backend root-equivalent access to the host. Use `k8s_per_user` for
  multi-tenant production.
- **The email allowlist is the perimeter** — unallowed addresses cannot create
  accounts at all. Keep it tight.

Known limitations:

- The `shared` kernel mode has no kernel-level isolation. Cross-user Spark
  reads ARE possible — `shared` is intended only for single-user dev.
- The kernel container's root user (UID 0 by default) has full filesystem
  access inside the container. Run with a non-root user for stricter sandboxing.
