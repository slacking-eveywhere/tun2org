#!/usr/bin/env bash
# tun2org installer.
#
# One-line install (from any machine, no Go needed):
#   curl -fsSL https://raw.githubusercontent.com/slacking-eveywhere/tun2org/main/install.sh | sh
#
# Installing and updating are the same thing: re-run the one-liner and the
# latest binaries published in the repo are (re)installed into ~/.local/bin.
# ~/.tun2org (config, known_hosts, state) is never touched.
#
# Subcommands (from a checkout):
#   build     cross-compile all release targets into ./dist + SHA256SUMS
#             (linux amd64+arm64, darwin arm64; needs Go 1.23+)
#   install   install the local platform's binaries from ./dist (dev mode)
#   uninstall remove binaries from ~/.local/bin
#
# No args (the curl|sh path) downloads the binaries from the GitHub repo —
# the repo is the package source; there are no GitHub Releases and no CI.
set -euo pipefail

REPO="slacking-eveywhere/tun2org"
BRANCH="main"
BASE="${NTUN_BASE:-https://raw.githubusercontent.com/${REPO}/${BRANCH}/dist}"
BIN_DIR="$HOME/.local/bin"
SHARE_DIR="$HOME/.tun2org"
SETCAP="${SETCAP:-1}"
DIST="${DIST:-dist}"

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

# ---------------------------------------------------------------- remote mode

remote_install() {
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

# ---------------------------------------------------------------- dev mode

build() {
    here="$(cd "$(dirname "$0")" && pwd)"
    if [ ! -f "$here/go.mod" ]; then
        err "run './install.sh build' from the repo root (no go.mod at $here)"; exit 1
    fi
    if ! command -v go >/dev/null 2>&1; then
        err "go not found in PATH; need Go 1.23+ to build"; exit 1
    fi

    commit="$(git -C "$here" rev-parse --short HEAD 2>/dev/null || echo unknown)"
    [ -n "$(git -C "$here" status --porcelain 2>/dev/null)" ] && commit="${commit}-dirty"
    ldflags="-s -w -X 'ntun-ng/internal/version.Commit=$commit'"

    # CGO_ENABLED=0: pure-Go static binaries (the agent is exec'd on remote
    # hosts with unknown libc; dynamic linking breaks there).
    echo "ntun: building static binaries (commit $commit) into $DIST/..."
    mkdir -p "$here/$DIST"
    rm -f "$here/$DIST"/ntun_* "$here/$DIST"/ntun-agent_* "$here/$DIST/SHA256SUMS"
    for target in linux_amd64 linux_arm64 darwin_arm64; do
        os="${target%%_*}"; arch="${target##*_}"
        (cd "$here" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
            go build -trimpath -ldflags="$ldflags" -o "$DIST/ntun_$target" ./cmd/ntun)
        (cd "$here" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
            go build -trimpath -ldflags="$ldflags" -o "$DIST/ntun-agent_$target" ./cmd/ntun-agent)
    done
    (cd "$here/$DIST" && sha256sum ntun_* ntun-agent_* > SHA256SUMS)

    echo "ntun: built:"
    (cd "$here/$DIST" && ls -l ntun_* ntun-agent_* SHA256SUMS)
    echo "ntun: commit $DIST/ (binaries + SHA256SUMS), push, then users install via the one-liner."
}

local_install() {
    here="$(cd "$(dirname "$0")" && pwd)"
    p="$(plat)"
    if [ ! -x "$here/$DIST/ntun_$p" ] || [ ! -x "$here/$DIST/ntun-agent_$p" ]; then
        err "no $DIST/ntun_$p (or agent) — run './install.sh build' first"; exit 1
    fi
    mkdir -p "$BIN_DIR"
    cp "$here/$DIST/ntun_$p"        "$BIN_DIR/ntun"
    cp "$here/$DIST/ntun-agent_$p" "$BIN_DIR/ntun-agent"
    chmod +x "$BIN_DIR/ntun" "$BIN_DIR/ntun-agent"
    if [ "$(uname -s)" = "Linux" ] && [ "$SETCAP" = "1" ] && command -v setcap >/dev/null 2>&1; then
        setcap cap_net_admin+ep "$BIN_DIR/ntun" 2>/dev/null \
            || err "setcap failed; use 'sudo ntun up'"
    fi
    mkdir -p "$SHARE_DIR"
    if [ ! -f "$SHARE_DIR/config.json" ]; then
        echo '{}' > "$SHARE_DIR/config.json"
    fi
    echo "ntun: installed into $BIN_DIR (from local $DIST):"
    "$BIN_DIR/ntun" version
}

uninstall() {
    rm -f "$BIN_DIR/ntun" "$BIN_DIR/ntun-agent"
    echo "ntun: removed binaries from $BIN_DIR"
    echo "ntun: leaving $SHARE_DIR intact (config, known_hosts, state); remove manually:"
    echo "  rm -rf $SHARE_DIR"
}

usage() { err "usage: install.sh [build|install|uninstall]   (no args = remote install/update)"; exit 1; }

case "${1:-}" in
    "")        remote_install ;;
    install)   local_install ;;
    build)     build ;;
    uninstall) uninstall ;;
    *)         usage ;;
esac
