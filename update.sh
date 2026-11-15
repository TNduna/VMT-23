#!/usr/bin/env bash
# VMT-23 update script: 1) Docker update (pull and recreate image)  2) local rebuild (rebuild binary)
# Corresponds to install.sh: install handles initial setup; update upgrades to a new version.
# Database migrations need no manual step—vmt-23 reruns schema.sql idempotently at every startup
# (including ADD COLUMN/CREATE INDEX IF NOT EXISTS), so a restart performs the migration.
# Data (pgdata volume, ./data, ./skills) is unaffected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: sync the repository to the latest code (updates compose, scripts, and local build sources) ──
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "Not a Git working copy; skipping git pull"; return; }
  [ "$(ask 'Pull the latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or diverged branch)—resolve manually and retry; using the current code for this run"
  fi
}

# ── 1) Docker update ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "Docker / Docker Compose not detected; deploy with ./install.sh first"
  [ -f .env ] || die ".env not found; run ./install.sh for initial deployment first"

  # Optional: upgrade to a specified image tag (leave blank to use VMT23_TAG from .env, defaulting to latest)
  local tag; tag="$(ask 'Target image tag (press Enter to keep .env value / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^VMT23_TAG=' .env; then
      sed -i.bak "s|^VMT23_TAG=.*|VMT23_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nVMT23_TAG=%s\n' "$tag" >> .env
    fi
    ok "Set VMT23_TAG to ${tag}"
  fi

  # Update only vmt-23: postgres is pinned to 16-alpine and does not need an upgrade (pulling it wastes bandwidth
  # and major-version changes may cause compatibility issues). vmt-23 declares depends_on postgres, so compose
  # starts pg if it is down; if it is already running, it remains unchanged and is not recreated.
  info "Pulling the new image (vmt-23 only)…"
  docker compose pull vmt-23
  info "Recreating and starting (vmt-23 automatically migrates the schema on restart)…"
  docker compose up -d vmt-23
  ok "Update complete → http://localhost:8787"
  info "View logs: docker compose logs -f vmt-23"
  info "Clean up old images (optional): docker image prune -f"
}

# ── 2) Local rebuild ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go (>=1.26) not found: https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found—if this is a first-time deployment, use ./install.sh instead"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "Rebuilding static frontend assets…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Rebuilding the self-contained binary…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o vmt-23 ./cmd/vmt-23
  else
    warn "npm not found: building the backend **without the embedded frontend** (run npm run dev separately for the frontend)"
    CGO_ENABLED=0 go build -o vmt-23 ./cmd/vmt-23
  fi
  ok "Build complete → ./vmt-23"
  warn "Restart the running vmt-23 process for changes to take effect (schema migration runs automatically at startup)"
}

echo "=============================="
echo "  VMT-23 Update"
echo "  1) Docker update (pull and recreate image)"
echo "  2) Local update (rebuild with Go)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "Invalid selection" ;;
esac