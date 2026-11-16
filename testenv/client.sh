#!/bin/sh
# Client-side e2e scenario runner. Executes INSIDE the client container.
#
#   client.sh direct   — ssh to the forward server, password auth
#   client.sh bastion  — ssh to the forward server through an ssh -W bastion
#
# Brings the tunnel up with the real ntun + real TUN, probes TCP/UDP/HTTP
# through it, tears down cleanly. Every external command goes through the
# ctl() command controller (bounded, TERM then KILL).

set -eu

MODE="${1:-}"
case "$MODE" in
    direct | bastion) ;;
    *) echo "usage: client.sh direct|bastion" >&2; exit 2 ;;
esac

TARGET_IP="${TARGET_IP:-172.31.0.10}"
PASSWORD="${PASSWORD:-testpass}"
CFG=/tmp/config.json
LOG=/tmp/ntun.log
NTUN_PID=""

ctl() { # ctl <budget_s> <cmd...>
    timeout -k 5 "$1" "$@"
}

write_config() {
    proxy=""
    if [ "$MODE" = bastion ]; then
        proxy="\"proxy_command\": \"ssh -W %h:%p -i /run/keys/id_ed25519 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ClearAllForwardings=yes -o ConnectTimeout=10 root@bastion\","
    fi
    cat > "$CFG" <<EOF
{
  "username": "root",
  "password": "$PASSWORD",
  "forward_server": "forward",
  "forward_port": 22,
  "auth": "password",
  $proxy
  "routes": ["172.31.0.0/24"],
  "dns_hijack": false
}
EOF
}

wait_tunnel() { # poll the TCP endpoint through the tunnel until it answers
    i=0
    while [ "$i" -lt 150 ]; do
        if ctl 5 python3 /test/probe.py tcp "$TARGET_IP" 9000 >/dev/null 2>&1; then
            return 0
        fi
        i=$((i + 1))
        sleep 0.2
    done
    return 1
}

teardown() {
    if [ -n "$NTUN_PID" ] && kill -0 "$NTUN_PID" 2>/dev/null; then
        kill -INT "$NTUN_PID" 2>/dev/null || true
        i=0
        while kill -0 "$NTUN_PID" 2>/dev/null && [ "$i" -lt 100 ]; do
            i=$((i + 1))
            sleep 0.2
        done
        kill -KILL "$NTUN_PID" 2>/dev/null || true
    fi
}
trap teardown EXIT

write_config
echo "client: ntun up (mode=$MODE)"
ntun up --config "$CFG" > "$LOG" 2>&1 &
NTUN_PID=$!

if ! wait_tunnel; then
    echo "FAIL ($MODE): tunnel never came up — ntun log:" >&2
    cat "$LOG" >&2
    exit 1
fi
echo "client: tunnel is up"

fail=0
if ctl 15 python3 /test/probe.py tcp "$TARGET_IP" 9000; then
    echo "ok: tcp echo through tunnel"
else
    echo "FAIL ($MODE): tcp echo" >&2; fail=1
fi
if ctl 15 python3 /test/probe.py udp "$TARGET_IP" 9001; then
    echo "ok: udp echo through tunnel"
else
    echo "FAIL ($MODE): udp echo" >&2; fail=1
fi
if ctl 15 python3 /test/probe.py http "http://$TARGET_IP:8080/ping"; then
    echo "ok: http get through tunnel"
else
    echo "FAIL ($MODE): http get" >&2; fail=1
fi

if [ "$fail" -eq 0 ]; then
    echo "PASS: $MODE"
fi
exit "$fail"
