# ntun-ng

System-level SSH tunnel. Routes selected traffic through a remote forward
server over a TUN device. Client (`ntun`) + remote agent (`ntun-agent`).

## Requirements

- Linux, or macOS (arm64)
- `CAP_NET_ADMIN` on the client (use `sudo`, or let the installer run `setcap`)
- Go 1.23+ only if you build from source (prebuilt binaries don't need it)

## Install

### Prebuilt binaries

Grab the `dist/` binaries (committed to this repo) and install them:

```sh
./install.sh install
```

This copies `ntun` + `ntun-agent` to `~/.local/bin`, optionally grants
`CAP_NET_ADMIN` so `sudo` isn't needed, and writes a placeholder config to
`~/.tun2org/config.json`.

Make sure `~/.local/bin` is on your `PATH`.

### Build from source

```sh
go mod tidy
./install.sh build    # compiles static binaries into ./dist
./install.sh install  # then installs them
```

### Uninstall

```sh
./install.sh uninstall
```

Leaves `~/.tun2org` (config + state) behind — remove it manually if you want it gone.

## Quick start

```sh
ntun config edit   # set username/forward_server/password/routes
ntun up            # bring the tunnel up (sudo if no setcap)
ntun down          # tear down
```

## Configuration

Config at `~/.tun2org/config.json`. See `config.dummy.json` for every option and
their defaults.

## License

See [LICENSE](LICENSE).
