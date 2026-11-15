#!/bin/sh
# VMT-23 supervisor startup script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       Run in foreground (Ctrl-C to stop)
#   nohup ./start.sh >vmt-23.log 2>&1 &   Run persistently in the background
#   ./start.sh -addr :9000           Pass additional arguments through to vmt-23
#
# Its only job is to start vmt-23 and decide whether to restart it based on its exit code.
#
#   0      Normal user stop        → exit loop
#   75     Program requests restart → restart immediately (one-click update or rollback in the UI)
#   Other  Crash                    → restart after backoff (1→2→4…up to 60 seconds)
#
# Downloads, SHA256 checks, and replacements are deliberately handled in Go (selfupdate package),
# rather than duplicated between the sh and bat scripts. A bad binary could otherwise be started
# repeatedly, leaving the user to recover the machine manually. vmt-23 performs checks/replacement
# on startup, keeping this script simple.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./vmt-23
if [ ! -x "$BIN" ] && [ -x ./vmt-23 ]; then BIN=./vmt-23; fi
[ -x "$BIN" ] || { echo "[vmt-23] Executable not found: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to the vmt-23 process.
#
# This is required in Docker: docker stop sends SIGTERM only to PID 1 (this script),
# not to child processes. Without forwarding, vmt-23 cannot shut down gracefully and is
# killed by SIGKILL after 10 seconds, interrupting any running task.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# A signal interrupts wait and makes it return >128. The child may still be shutting down
	# gracefully, so wait again to get its actual exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[vmt-23] Stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[vmt-23] Exited normally"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback is ready: on restart, vmt-23 applies it during startup (see selfupdate.Bootstrap).
			echo "[vmt-23] Restart requested (apply the new version)…"
			delay=1
			;;
		*)
			echo "[vmt-23] Unexpected exit (code=$code); restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done