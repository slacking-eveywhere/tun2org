#!/usr/bin/env bash
# tun2org installer — for end users.
#
# One-line install / update (no Go needed; installing and updating are the
# same thing — re-run the line):
#   curl -fsSL https://raw.githubusercontent.com/slacking-eveywhere/tun2org/main/install.sh | sh
#
# Downloads the platform binaries + SHA256SUMS from the repo, verifies them,
# installs into ~/.local/bin. ~/.tun2org (config, known_hosts, state) is
# never touched.
#
# Developers: build/test/local-install live in the Makefile (`make help`).
set -euo pipefail

REPO="slacking-eveywhere/tun2org"
BRANCH="main"
BASE="${NTUN_BASE:-https://raw.githubusercontent.com/${REPO}/${BRANCH}/dist}"
BIN_DIR="$HOME/.local/bin"
SHARE_DIR="$HOME/.tun2org"
SETCAP="${SETCAP:-1}"

err() { echo "ntun: $*" >&2; }

# ---------------------------------------------------------------- platform

plat() {
    case "$(uname -s)/$(uname -m)" in
        Linux/x86_64)  echo linux_amd64 ;;
        Linux/aarch64|Linux/arm64) echo linux_arm64 ;;
        Darwin/arm64)  echo darwin_arm64 ;;
        *)
            err "unsupported platform: $(uname -s)/$(uname -m)"
            err "supported: linux/amd64, linux/arm64, darwin/arm64"
            exit 1
            ;;
    esac
}

fetch() { # fetch URL -> stdout
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" || { err "download failed: $1"; exit 1; }
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "$1" || { err "download failed: $1"; exit 1; }
    else
        err "need curl or wget to download"; exit 1
    fi
}

sha() { # sha FILE -> hex digest
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        err "need sha256sum or shasum to verify binaries"; exit 1
    fi
}

# ---------------------------------------------------------------- install

install() {
    local p
    p="$(plat)"

    echo "ntun: downloading tun2org for ${p} from github.com/${REPO} (branch ${BRANCH})..."
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    fetch "$BASE/SHA256SUMS" > "$tmp/SHA256SUMS"

    local bin want got
    for bin in ntun ntun-agent; do
        local f="${bin}_${p}"
        fetch "$BASE/$f" > "$tmp/$f"
        want="$(grep " $f\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)"
        if [ -z "$want" ]; then
            err "no checksum for $f in SHA256SUMS — refusing to install"; exit 1
        fi
        got="$(sha "$tmp/$f")"
        if [ "$got" != "$want" ]; then
            err "checksum mismatch for $f (want $want, got $got) — refusing to install"; exit 1
        fi
    done
    echo "ntun: checksums ok"

    mkdir -p "$BIN_DIR"
    cp "$tmp/ntun_${p}"        "$BIN_DIR/ntun"
    cp "$tmp/ntun-agent_${p}" "$BIN_DIR/ntun-agent"
    chmod +x "$BIN_DIR/ntun" "$BIN_DIR/ntun-agent"

    if [ "$(uname -s)" = "Linux" ] && [ "$SETCAP" = "1" ] && command -v setcap >/dev/null 2>&1; then
        setcap cap_net_admin+ep "$BIN_DIR/ntun" 2>/dev/null \
            || err "setcap failed; use 'sudo ntun up' (or re-run installer with sudo)"
    fi

    mkdir -p "$SHARE_DIR"
    if [ ! -f "$SHARE_DIR/config.json" ]; then
        echo '{}' > "$SHARE_DIR/config.json"
        echo "ntun: wrote placeholder $SHARE_DIR/config.json (edit with: ntun config edit)"
    fi

    echo
    echo "ntun: installed into $BIN_DIR:"
    "$BIN_DIR/ntun" version
    case ":$PATH:" in
        *":$BIN_DIR:"*) ;;
        *) echo "ntun: NOTE: $BIN_DIR is not on your PATH — add it." ;;
    esac
}

uninstall() {
    rm -f "$BIN_DIR/ntun" "$BIN_DIR/ntun-agent"
    echo "ntun: removed binaries from $BIN_DIR"
    echo "ntun: leaving $SHARE_DIR intact (config, known_hosts, state); remove manually:"
    echo "  rm -rf $SHARE_DIR"
}

usage() { err "usage: install.sh [uninstall]   (no args = install/update)"; exit 1; }

case "${1:-}" in
    "")        install ;;
    uninstall) uninstall ;;
    *)         usage ;;
esac
