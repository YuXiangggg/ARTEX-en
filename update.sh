#!/usr/bin/env bash
# ARTEX update script: (1) Docker update (pull new image and recreate)  (2) local build update (rebuild binary)
# Counterpart to install.sh: install handles first-time setup, update handles upgrading to a new version.
# DB migrations need no manual step -- artex idempotently re-runs schema.sql on every start (including ADD COLUMN/CREATE
# INDEX IF NOT EXISTS), so "restart = migrate". Data (pgdata volume, ./data, ./skills) is not affected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# -- Optional: sync the repo to the latest code (compose/scripts/local build sources all update through this) -------
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "Not a git working copy, skipping git pull"; return; }
  [ "$(ask 'Pull latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or diverged branch) -- resolve manually and retry; continuing with the current code this time"
  fi
}

# -- (1) Docker update ---------------------------------------
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose not found, please deploy with ./install.sh first"
  [ -f .env ] || die ".env not found, please run ./install.sh first to complete the initial deployment"

  # Optional: upgrade to a specific version tag (if empty, keep ARTEX_TAG from .env, default latest)
  local tag; tag="$(ask 'Target image tag (press Enter to keep .env / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "Set ARTEX_TAG to ${tag}"
  fi

  # Only touch artex: postgres is pinned to 16-alpine and doesn't need upgrading (pulling it just wastes bandwidth,
  # and major version changes carry compatibility risks). artex declares depends_on postgres, so running up with the service name
  # starts pg automatically if it's not running, and leaves it untouched (not recreated) if it already is.
  info "Pulling new image (artex only)..."
  docker compose pull artex
  info "Recreating and starting (artex migrates the schema automatically on restart)..."
  docker compose up -d artex
  ok "Update complete -> http://localhost:8787"
  info "View logs: docker compose logs -f artex"
  info "Clean up old images (optional): docker image prune -f"
}

# -- (2) Local build update ---------------------------------
update_local(){
  command -v go >/dev/null 2>&1 || die "Go not found (>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found -- for a first-time deployment use ./install.sh instead"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "Rebuilding static frontend..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Rebuilding single binary with embedded frontend..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm not found: building the backend **without the embedded frontend** (run the frontend separately with npm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "Build complete -> ./artex"
  warn "Restart the running artex process for the update to take effect (the schema is migrated automatically on restart)"
}

echo "=============================="
echo "  ARTEX update"
echo "  1) Docker update (pull new image and recreate)"
echo "  2) Local update (rebuild with go)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "Invalid choice" ;;
esac
