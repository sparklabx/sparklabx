# Contributing to SparkLabX

Thanks for your interest in improving SparkLabX. This document explains how
to get a local dev environment running, how we expect changes to be
proposed, and what we look for during review.

## Reporting bugs / requesting features

- **Bugs**: open a GitHub issue using the *Bug report* template. Include
  the deployment mode (`KERNEL_MODE`), versions, and the smallest set of
  steps that reproduces the problem.
- **Features**: open a GitHub issue using the *Feature request* template.
  Describe the use case first, then the proposed solution — it's easier
  to discuss the shape of a feature when the motivation is clear.
- **Security vulnerabilities**: do **not** open a public issue. Follow the
  process in [SECURITY.md](./SECURITY.md).

## Development setup

Prerequisites: Go 1.26+, Node 22+, Docker (for the Spark kernel image).

```bash
git clone https://github.com/sparklabx/sparklabx.git
cd sparklabx
cp .env.example .env
# generate a JWT secret and pick an admin password — see .env.example

# bring up Postgres + MinIO + backend + frontend
docker compose -f docker-compose.test.yml up -d --build
```

For iterating on a single component, run it outside Docker:

```bash
# backend
cd backend && go run ./cmd/server

# frontend
cd frontend && npm install && npm run dev
```

## Branching & commits

We use a three-branch promotion chain: **`dev` → `test` → `main`**.
External contributors only ever target `dev`; maintainers handle the
`dev → test` and `test → main` promotions on a release cadence.

- Branch off **`dev`** (not `main`). Name branches
  `<type>/<short-slug>`, e.g. `fix/kernel-reaper-race` or
  `feat/scala-syntax-highlight`.
- Keep commits focused. One logical change per commit; squash fixup
  commits before opening the PR.
- Write commit messages in the imperative mood
  (`Fix idle reaper double-decrement`, not `Fixed…` or `Fixes…`).
- Reference issues with `Fixes #123` / `Refs #123` in the body when
  applicable.

## Pull requests

1. Fork or branch off `dev`, push, open a PR **against `dev`**.
   (The `pr-promotion-order` workflow rejects PRs that try to skip the
   chain — e.g. a feature branch opened directly against `test` or
   `main` will fail CI.)
2. Fill out the PR template — what changed, why, how it was tested.
3. CI must pass (build + tests + image builds).
4. At least one maintainer review is required before merge.
5. We use **squash merge** for most PRs to keep history linear.

### Release flow (maintainers)

Once changes land on `dev`, maintainers promote in two steps:

- PR `dev → test` (squash merge). CI re-runs; the staging environment
  picks up the squashed commit.
- PR `test → main` (squash merge). The `release.yml` workflow then
  builds and publishes multi-arch images to GHCR.
- Tag `vX.Y.Z` on `main` and draft a GitHub Release.

Contributors don't need to do any of this — opening a PR against `dev`
is enough.

PRs that touch the security boundary (auth, IAM provisioning, kernel
isolation, storage path handling) get extra scrutiny — please flag those
in the PR description so reviewers know.

## Code style

- **Go**: `gofmt` (enforced) and `go vet ./...` clean. Prefer the
  standard library; new dependencies need a short justification in the PR.
- **TypeScript / React**: ESLint config in `frontend/eslint.config.js` is
  the source of truth. Run `npm run lint` before pushing.
- **YAML / Dockerfiles**: keep them readable — comments for any non-obvious
  flag, no trailing whitespace.

## Testing

- Backend: `go test ./...` from `backend/`. Add tests next to the code
  they cover (`crypto_test.go`, `handlers_test.go`, …).
- Frontend: tests live next to components (`*.test.tsx`). Run
  `npm test` from `frontend/`.
- For changes to auth, IAM provisioning, or path handling, a unit test
  that exercises the deny path is expected, not just the happy path.

## License

By contributing, you agree your work will be released under the project's
Apache-2.0 license (see [LICENSE](./LICENSE)). Do not include code you
don't own the rights to, and call out any third-party snippets with their
original attribution.
