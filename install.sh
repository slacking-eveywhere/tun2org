#!/usr/bin/env bash
# ntun-ng build / install / uninstall.
#
# build: compiles static `ntun` + `ntun-agent` into ./dist. Run this in the
#   devcontainer (or any box with Go 1.23+); user PCs don't need Go.
# install: copies the prebuilt ./dist binaries to a bin dir, and (optionally)
#   grants CAP_NET_ADMIN to `ntun` so `up`/`down` work without sudo. If ./dist
#   is missing but Go is available, falls back to building (dev convenience).
#   The config share dir (~/.tun2org) is created; an existing config.json is
#   preserved.
set -euo pipefail

BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
SHARE_DIR="${SHARE_DIR:-$HOME/.tun2org}"
SETCAP="${SETCAP:-1}"
DIST="${DIST:-dist}"

err() { echo "ntun-ng: $*" >&2; }

have() { command -v "$1" >/dev/null 2>&1; }

build() {
    here="$(cd "$(dirname "$0")" && pwd)"
    if [ ! -f "$here/go.mod" ] || [ "$(basename "$here")" != "ntun-ng" ]; then
        err "run this from the ntun-ng directory (got: $here)"
        err "expected ntun-ng/go.mod"
        exit 1
    fi
    if ! have go; then
        err "go not found in PATH; build in the devcontainer (or install Go 1.23+)."
        exit 1
    fi

    echo "ntun-ng: building static binaries (CGO disabled) into $DIST/..."
    # CGO_ENABLED=0: pure-Go static binaries. The agent is exec'd on remote
    # hosts that may run older glibc (e.g. Ubuntu 20.04 = 2.31) than the
    # build machine; a dynamic binary would fail with
    # "version `GLIBC_2.xx' not found".
    mkdir -p "$here/$DIST"
    commit="$(git -C "$here" rev-parse --short HEAD 2>/dev/null || echo unknown)"
    [ -n "$(git -C "$here" status --porcelain 2>/dev/null)" ] && commit="$commit-dirty"
    ldflags="-s -w -X 'ntun-ng/internal/version.Commit=$commit'"
    (cd "$here" && CGO_ENABLED=0 go build -ldflags="$ldflags" -o "$DIST/ntun" ./cmd/ntun)
    (cd "$here" && CGO_ENABLED=0 go build -ldflags="$ldflags" -o "$DIST/ntun-agent" ./cmd/ntun-agent)

    echo "ntun-ng: built. sha256:"
    (cd "$here" && sha256sum "$DIST/ntun" "$DIST/ntun-agent")
    echo "ntun-ng: commit $DIST/ to the repo, then run './install.sh install' on the target PC."
}

install() {
    here="$(cd "$(dirname "$0")" && pwd)"
    if [ ! -f "$here/go.mod" ] || [ "$(basename "$here")" != "ntun-ng" ]; then
        err "run this from the ntun-ng directory (got: $here)"
        err "expected ntun-ng/go.mod"
        exit 1
    fi

    if [ -x "$here/$DIST/ntun" ] && [ -x "$here/$DIST/ntun-agent" ]; then
        echo "ntun-ng: installing prebuilt binaries from $DIST/ (no Go needed)..."
        cp "$here/$DIST/ntun"      "$BIN_DIR/ntun"
        cp "$here/$DIST/ntun-agent" "$BIN_DIR/ntun-agent"
    elif have go; then
        echo "ntun-ng: no prebuilt $DIST/ binaries; building (CGO disabled → static)..."
        commit="$(git -C "$here" rev-parse --short HEAD 2>/dev/null || echo unknown)"
        [ -n "$(git -C "$here" status --porcelain 2>/dev/null)" ] && commit="$commit-dirty"
        ldflags="-s -w -X 'ntun-ng/internal/version.Commit=$commit'"
        (cd "$here" && CGO_ENABLED=0 go build -ldflags="$ldflags" -o "$DIST/ntun" ./cmd/ntun)
        (cd "$here" && CGO_ENABLED=0 go build -ldflags="$ldflags" -o "$DIST/ntun-agent" ./cmd/ntun-agent)
        cp "$here/$DIST/ntun"      "$BIN_DIR/ntun"
        cp "$here/$DIST/ntun-agent" "$BIN_DIR/ntun-agent"
    else
        err "no prebuilt binaries in $here/$DIST/ and no Go in PATH."
        err "build them in the devcontainer with './install.sh build', commit $DIST/, and re-run here."
        exit 1
    fi

    mkdir -p "$BIN_DIR" "$SHARE_DIR"
    chmod +x "$BIN_DIR/ntun" "$BIN_DIR/ntun-agent"

    # Optional: grant CAP_NET_ADMIN so `ntun up` doesn't need sudo. This keeps
    # the binary non-world-writable (setcap refuses otherwise).
    if [ "$SETCAP" = "1" ] && have setcap; then
        echo "ntun-ng: granting CAP_NET_ADMIN to $BIN_DIR/ntun"
        setcap cap_net_admin+ep "$BIN_DIR/ntun" || \
            err "setcap failed (run with sudo or keep using sudo for ntun up)"
    else
        err "setcap skipped; use 'sudo ntun up' (or run this script with sudo)."
    fi

    if [ ! -f "$SHARE_DIR/config.json" ]; then
        echo '{}' > "$SHARE_DIR/config.json"
        echo "ntun-ng: wrote placeholder $SHARE_DIR/config.json (edit with: ntun config edit)"
    fi

    cat <<EOF

ntun-ng: installed.
  binaries : $BIN_DIR/ntun , $BIN_DIR/ntun-agent
  config    : $SHARE_DIR/config.json
  state     : $SHARE_DIR/state.json (written on 'ntun up', read by 'ntun down')

Make sure $BIN_DIR is on your PATH. Quick start:
  ntun config edit            # set username/forward_server/password/routes
  ntun up                     # (sudo if no setcap) bring tunnel up
  ntun down                   # tear down
EOF
}

uninstall() {
    rm -f "$BIN_DIR/ntun" "$BIN_DIR/ntun-agent"
    echo "ntun-ng: removed binaries from $BIN_DIR"
    echo "ntun-ng: leaving $SHARE_DIR intact (config + state). Remove manually if desired:"
    echo "  rm -rf $SHARE_DIR"
}

usage() { err "usage: $0 {build|install|uninstall}"; exit 1; }

if [ "$#" -ne 1 ]; then usage; fi
case "$1" in
    build)     build ;;
    install)   install ;;
    uninstall) uninstall ;;
    *) usage ;;
esac
