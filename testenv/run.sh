#!/usr/bin/env bash
# E2E test driver: dockerized SSH bastion + forward servers, endpoint
# services, and the real ntun client (built in-image from the repo).
#
# Scenarios (default: both):  direct   — ssh to forward, password auth
#                              bastion  — ssh to forward through ssh -W bastion
#
# Usage: testenv/run.sh [--keep] [direct|bastion]
#   --keep  leave the env up after a run for debugging (cleanup hint printed)
#
# Every external command goes through the ctl() command controller: bounded
# budget, TERM then KILL. The EXIT trap tears the whole env down on success,
# failure, timeout, or Ctrl-C.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
cd "$ROOT"

DC=(docker compose -f testenv/docker-compose.yml)
KEYS="$HERE/.run"

ctl() { # ctl <budget_seconds> <cmd...>
    local budget="$1"
    shift
    timeout -k 5 "${budget}s" "$@"
}

KEEP=0
SCENARIOS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --keep) KEEP=1 ;;
        direct | bastion) SCENARIOS+=("$1") ;;
        *)
            echo "usage: $0 [--keep] [direct|bastion]" >&2
            exit 2
            ;;
    esac
    shift
done
[ ${#SCENARIOS[@]} -gt 0 ] || SCENARIOS=(direct bastion)

cleanup() {
    local rc=$?
    if [ "$KEEP" = 1 ]; then
        echo "e2e: --keep: env left running"
        echo "e2e: tear down with: ${DC[*]} down -v --remove-orphans && rm -rf '$KEYS'"
        return
    fi
    echo "e2e: cleaning up"
    "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$KEYS"
    echo "e2e: clean (rc=$rc)"
}
trap cleanup EXIT

echo "e2e: building images"
ctl 600 "${DC[@]}" build

echo "e2e: generating the client ssh key (bastion hop auth)"
rm -rf "$KEYS"
mkdir -p "$KEYS/keys"
ctl 120 "${DC[@]}" run --rm --no-deps keygen

echo "e2e: starting servers"
ctl 300 "${DC[@]}" up -d --no-build bastion forward target

wait_ready() { # wait_ready <service> <pgrep-pattern>
    local svc="$1" pattern="$2" i=0
    while [ "$i" -lt 100 ]; do
        if "${DC[@]}" exec -T "$svc" pgrep -f "$pattern" >/dev/null 2>&1; then
            echo "e2e: $svc ready"
            return 0
        fi
        i=$((i + 1))
        sleep 0.3
    done
    echo "e2e: $svc ($pattern) never became ready" >&2
    return 1
}
wait_ready bastion sshd
wait_ready forward sshd
wait_ready target endpoints.py

rc=0
for scenario in "${SCENARIOS[@]}"; do
    echo "e2e: scenario $scenario"
    if ctl 240 "${DC[@]}" run --rm --no-build client "$scenario"; then
        echo "e2e: scenario $scenario PASS"
    else
        rc=1
        echo "e2e: scenario $scenario FAIL"
    fi
done

if [ "$rc" -eq 0 ]; then
    echo "e2e: ALL PASS"
fi
exit "$rc"
