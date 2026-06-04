#!/usr/bin/env bash
# SparkLabX Notebook — one-shot local bootstrap.
#
# What it does:
#   1. Generates a fresh .env if one doesn't exist (random JWT key + admin password).
#   2. Pulls public Docker Hub images.
#   3. Starts the stack with KERNEL_MODE=docker_per_user (true IAM isolation locally).
#   4. Prints the URL and login credentials.
#
# Re-running is safe: existing .env is preserved.
#
# Requirements: Docker + Docker Compose v2.

set -euo pipefail

cd "$(dirname "$0")"

# ---------- helpers ----------
log()  { printf "\033[1;34m▶\033[0m %s\n" "$*"; }
ok()   { printf "\033[1;32m✓\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m!\033[0m %s\n" "$*" >&2; }

require() {
  command -v "$1" >/dev/null 2>&1 || { warn "Missing dependency: $1"; exit 1; }
}

rand_b64() {
  openssl rand -base64 "$1" 2>/dev/null | tr -d '\n=+/' | cut -c1-"$1"
}

# ---------- preflight ----------
log "Checking dependencies..."
require docker
require openssl

if ! docker compose version >/dev/null 2>&1; then
  warn "Docker Compose v2 not found. Install Docker Desktop or 'docker compose' plugin."
  exit 1
fi

# ---------- generate .env if missing ----------
if [ -f .env ]; then
  log ".env already exists — keeping your settings."
else
  log "Generating .env with random secrets..."
  cp .env.example .env

  JWT_KEY=$(rand_b64 48)
  ADMIN_PW=$(rand_b64 16)

  # macOS / BSD sed needs the empty-string -i argument; GNU sed doesn't.
  # Use a temp file + mv to stay portable.
  sed -e "s|^JWT_SECRET_KEY=.*|JWT_SECRET_KEY=${JWT_KEY}|" \
      -e "s|^SEED_ADMIN_PASSWORD=.*|SEED_ADMIN_PASSWORD=${ADMIN_PW}|" \
      .env > .env.tmp && mv .env.tmp .env

  ok "Secrets generated. (Stored in .env — gitignored.)"
fi

# ---------- start ----------
log "Pulling images..."
docker compose pull --quiet || warn "Pull failed for some images (will build / use cached)."

log "Starting stack..."
docker compose up -d

# ---------- wait for backend ----------
log "Waiting for backend..."
for i in {1..30}; do
  if curl -sf http://localhost:10000/healthz >/dev/null 2>&1; then
    ok "Backend is up."
    break
  fi
  sleep 2
done

# ---------- summary ----------
SEED_USER=$(grep '^SEED_ADMIN_USERNAME=' .env | cut -d= -f2-)
SEED_PASS=$(grep '^SEED_ADMIN_PASSWORD=' .env | cut -d= -f2-)
KERNEL_MODE_VAL=$(grep '^KERNEL_MODE=' .env | cut -d= -f2- || echo "shared")

cat <<EOF

────────────────────────────────────────────
SparkLabX Notebook is running.

  URL:        http://localhost:3000
  Admin:      ${SEED_USER:-admin}
  Password:   ${SEED_PASS:-(see .env)}
  Kernel:     ${KERNEL_MODE_VAL}

Tail logs:    docker compose logs -f backend
Stop:         docker compose down
────────────────────────────────────────────
EOF
