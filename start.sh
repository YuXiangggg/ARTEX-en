#!/bin/sh
# ARTEX supervisor launch script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       run in foreground (Ctrl-C to stop)
#   nohup ./start.sh >artex.log 2>&1 &   run persistently in background
#   ./start.sh -addr :9000           extra arguments are passed through to artex as-is
#
# It does one thing only: run artex, and after the process exits decide by exit code whether to restart it.
#
#   0      normal stop by user      -> exit the loop
#   75     program requested restart -> rerun immediately ("One-click update" or "Rollback" was clicked in the UI)
#   other  crash                    -> rerun after backoff (1->2->4... up to 60 seconds)
#
# Downloading, SHA256 verification and binary swapping are deliberately NOT done here: that logic would need two versions (sh and bat),
# and it is exactly the part that must never fail -- once a binary that can't run is swapped in, this script will faithfully
# restart it over and over, and the user has to log into the machine to fix it by hand. So verification/swapping all stays in Go (the selfupdate package),
# done by artex itself at startup, keeping the script dead simple.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] executable not found: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to the artex process itself.
#
# This is required under Docker: docker stop only sends SIGTERM to PID 1 (i.e. this script),
# not to child processes. Without forwarding, artex never gets the signal, can't shut down gracefully, and is SIGKILLed
# after 10 seconds, cutting running tasks off midway.
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

	# A signal interrupts wait and makes it return >128. The child is actually still shutting down gracefully,
	# so we must wait again to get its real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] exited normally"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback is ready: on rerun, artex completes the swap at startup (see selfupdate.Bootstrap).
			echo "[artex] restart requested (applying new version)..."
			delay=1
			;;
		*)
			echo "[artex] exited abnormally (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
