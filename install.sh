#!/usr/bin/env bash
# VMT-23 installation script: 1) Docker  2) build and run locally
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── Check Docker environment / install automatically ───────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "Docker and Docker Compose detected"; return
  fi
  warn "Docker / Docker Compose not detected"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask 'Install Docker automatically? (y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Docker installation complete (log in again for the group change to take effect and avoid sudo)"
      else
        die "Please install Docker manually and try again"
      fi ;;
    Darwin) die "On macOS, install Docker Desktop: https://www.docker.com/products/docker-desktop/" ;;
    *)      die "Please install Docker manually and try again" ;;
  esac
}

# ── 1) Docker deployment ───────────────────────────────
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Postgres password (press Enter to generate a random one)' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY (optional; can be configured later in the UI)' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok "Generated .env (POSTGRES_PASSWORD is set)"
  else
    info "Using the existing .env"
  fi
  info "Pulling images and starting services…"
  docker compose pull || true
  docker compose up -d
  ok "Startup complete → http://localhost:8787"
  info "View logs: docker compose logs -f vmt-23"
}

# ── 2) Build and run locally ──────────────────────────────
install_local(){
  echo "Database installation method:"
  echo "  1) Connect to an existing PostgreSQL instance"
  echo "  2) Start PostgreSQL with Docker (Docker required)"
  case "$(ask 'Choose' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres password (press Enter for a random one)' "$(rand)")"
      docker run -d --name vmt23-pg -p 5432:5432 \
        -e POSTGRES_USER=vmt-23 -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=vmt-23 \
        -v vmt23-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=vmt-23 DB_PASS="$pw" DB_NAME=vmt-23 DB_SSL=disable ;;
    *)
      DB_HOST="$(ask 'Database host' 127.0.0.1)"
      DB_PORT="$(ask 'Port' 5432)"
      DB_USER="$(ask 'Username' vmt-23)"
      DB_PASS="$(ask 'Password' '')"
      DB_NAME="$(ask 'Database name' vmt-23)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # Generate config.json
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST}",
    "port": ${DB_PORT},
    "user": "${DB_USER}",
    "password": "${DB_PASS}",
    "dbname": "${DB_NAME}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  ok "Generated config.json"

  # Check Go environment
  command -v go >/dev/null 2>&1 || die "Go not found; install Go (>=1.26) first: https://go.dev/dl/"
  ok "Go: $(go version)"

  # The embedded frontend requires Node to generate static assets
  if command -v npm >/dev/null 2>&1; then
    info "Building static frontend assets…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Building the self-contained binary…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o vmt-23 ./cmd/vmt-23
  else
    warn "npm not found: building the backend **without the embedded frontend** (run npm run dev separately for the frontend)"
    CGO_ENABLED=0 go build -o vmt-23 ./cmd/vmt-23
  fi
  ok "Build complete → ./vmt-23"

  info "Starting… (Ctrl-C to exit)"
  ./vmt-23
}

echo "=============================="
echo "  VMT-23 Installation"
echo "  1) Docker deployment"
echo "  2) Run locally (build with Go)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "Invalid selection" ;;
esac