#!/usr/bin/env bash
# =============================================================================
# VMT-23 Administrator password reset script
#
# The login username is fixed as VMT-23; the password is stored as a bcrypt hash in the database settings table under
# auth.password_hash. After connecting to the database, this script uses pgcrypto to generate the bcrypt hash in the database
# and writes it back to that key—fully compatible with backend login verification (golang.org/x/crypto/bcrypt).
#
# Deployment modes:
#    local (default) — connect directly to the database from the host with psql. Connection details are resolved in this order:
#                    command-line arguments > --dsn/$VMT23_PG_DSN > database.* in config.json
#   docker        — use `docker compose exec` (or `docker exec`) inside the postgres
#                    container to run psql (Compose does not expose port 5432 to the host by default, so psql runs inside the container).
#
# Usage examples:
#   ./reset-password.sh                          # Local: read config.json/environment automatically and prompt for the new password
#   ./reset-password.sh -p 'NewPass!'            # Local: provide the new password directly
#   ./reset-password.sh --dsn postgres://u:p@h:5432/vmt23
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d vmt23
#   ./reset-password.sh -m docker                # Docker deployment (read POSTGRES_* from .env)
#   ./reset-password.sh -m docker -c pgcontainer name --exec docker
#
# Security: pass the new password through an environment variable and psql \getenv (not in process argv), then use :'var'
# escapes it automatically (preventing SQL injection); the database password is also passed through PGPASSWORD, not argv.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            #  local | docker (empty = auto-detect)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # Postgres service/container name (default postgres)
EXEC_KIND=""       # compose | docker (which exec method to use in Docker mode; empty = auto-detect)
NEWPASS=""
ASSUME_YES=0

die() { echo "Error:$*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- Parse arguments -------------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "Unknown argument:$1(see usage with -h)" ;;
  esac
done

# ---- Read database.* from config.json ( local mode only, and only if no connection was explicitly provided)-----
# Prefer parsing with python3 (more robust); fall back to grep if python3 is unavailable (config.json uses regular field formatting).
read_config_json() {
   local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# Supports either a direct DSN or individual fields
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # Minimal fallback:grep each key (values may be strings or numbers)
     local k
    for k in host port user password dbname sslmode; do
       local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
   local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# ---- Automatically determine the mode ---------------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${VMT23_PG_DSN:-${VMT23_PG_DSN:-}}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "Deployment mode:$MODE"

# ---- Collect the new password -----------------------------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "Enter the new password (username is fixed as VMT-23):" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "Password cannot be empty"
  read -r -s -p "Re-enter to confirm:" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "The entries do not match"
fi
[[ -n "$NEWPASS" ]] || die "Password cannot be empty"

# Pass the password to psql through an environment variable (read by \getenv; not included in argv/ps)
export VMT23_RESET_NEWPASS="$NEWPASS"

# Generate bcrypt in the database and upsert it; :'newpw' escapes the password automatically. CREATE EXTENSION is idempotent,
# but this will fail if the database role cannot create extensions (see the run failure branch below).
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw VMT23_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ---- Execute -----------------------------------------------------------------
if [[ "$MODE" == "local" ]]; then
  # Connection priority: command line > --dsn/$VMT23_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${VMT23_PG_DSN:-${VMT23_PG_DSN:-}}" ]] && DSN="$VMT23_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "Read database configuration from $cfg"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "psql not found locally (install postgresql-client or use -m docker)"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "Database user (-U) or valid config.json/DSN is required"
    [[ -n "$DBNAME" ]] || die "Database name (-d) or valid config.json/DSN is required"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "Target database:$target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Reset the VMT-23 password in this database？[y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "Cancelled"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "Write failed. If the error mentions missing pgcrypto or insufficient permissions, use a role that can create extensions or run CREATE EXTENSION pgcrypto manually first."
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "Not found docker"
  CONTAINER="${CONTAINER:-postgres}"

  # Choose the exec method: prefer docker compose exec (service name), otherwise docker exec (container name)
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # Credentials for psql inside the container: command line first, then POSTGRES_* from .env, then the Compose default (artex)
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-vmt23}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-vmt23}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "Target: psql -U $DUSER -d $DNAME inside container $CONTAINER (exec=$EXEC_KIND)"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Reset the VMT-23 password in the database inside this container?[y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "Cancelled"
  fi

  # -e Only the variable name is passed, not its value → inherited from the current environment; the password does not appear in docker command argv.
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e VMT23_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e VMT23_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "Write failed. Check the container name (-c), database username (POSTGRES_* in .env), and that the role has pgcrypto permissions."
  fi
fi

unset VMT23_RESET_NEWPASS
echo "✓ VMT-23 administrator password reset. Sign in with username VMT-23 and the new password (no service restart required)."
