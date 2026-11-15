#!/usr/bin/env bash
# Development mode: run the backend (:8787), traffic proxy (:8788), and Next.js dev server (:5173) together.
# The frontend proxies /api to the backend; Ctrl-C stops them all.
#
# For the single-binary mode (with embedded frontend), see the "Single binary" section in README; this script does not use it.
set -euo pipefail
cd "$(dirname "$0")"

# Stop all child processes in this process group on exit (backend + frontend).
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (regular go run, without embedded frontend); configure concurrent work agents in System Settings.
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# Frontend hot reload (Vite/Next dev server, /api proxies to :8787).
( cd web && npm run dev ) &

echo "[dev] backend :8787 / proxy :8788 / frontend http://localhost:5173 (Ctrl-C to exit)"
wait