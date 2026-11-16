# tun2org

System-level SSH tunnel. Routes selected traffic through a remote forward
server over a TUN device. Client (`ntun`) + remote agent (`ntun-agent`).

Supported platforms: **linux/amd64, linux/arm64, macOS/Apple Silicon (arm64)**.
The client needs `CAP_NET_ADMIN` (the installer grants it via `setcap`, or use
`sudo`).

## Install / update

One line — installs into `~/.local/bin`, no Go required:

```sh
curl -fsSL https://raw.githubusercontent.com/slacking-eveywhere/tun2org/main/install.sh | sh
```

**Installing and updating are the same thing**: re-run the one-liner to
update. The binaries and their SHA256 checksums are downloaded from this
repository (`dist/` on branch `main`), verified, and copied into
`~/.local/bin`. Your `~/.tun2org/` directory (config, known_hosts, state) is
never touched.

If `~/.local/bin` is not on your `PATH`, add it:

```sh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.profile   # or ~/.bashrc / ~/.zshrc
```

To remove:

```sh
install.sh uninstall   # if you kept the script; otherwise:
rm -f ~/.local/bin/ntun ~/.local/bin/ntun-agent
rm -rf ~/.tun2org      # optional: config, known_hosts, state
```

### Build from source (developers)

Needs Go 1.23+. Dev tasks live in the [Makefile](Makefile) — `make help`
for the full list:

```sh
make build     # cross-compile all targets into dist/ + SHA256SUMS
make install   # build + install the local platform into ~/.local/bin
make test      # go test ./...
make uninstall # remove ~/.local/bin/{ntun,ntun-agent}
```

`make build` is the release flow: commit and push `dist/`, and the one-liner
installs the new version for everyone.

## Quick start

```sh
ntun config edit                        # set username/forward_server/password/routes
ntun test                               # connectivity probe (auth, agent upload, mux ping)
ntun up                                 # bring the tunnel up (sudo if no setcap)
ntun down                               # tear down
```

## Configuration

Config lives at `~/.tun2org/config.json`. See
[config.dummy.json](config.dummy.json) for every option and its defaults.

### Host key verification (TOFU)

The server's host key is verified trust-on-first-use, OpenSSH style: the
first connection captures the key into `~/.tun2org/known_hosts`; any later
mismatch aborts the connection (possible MITM). To pre-pin instead, set
`host_key` in the config to an authorized-keys-format line; `ntun test`
reports the current pinning status.

## Security note on curl | sh

The one-liner executes a script fetched over TLS from this repository. The
SHA256 checksums it verifies protect the download against transport
corruption; they are stored in the same repository, so a compromised repo
compromises both. If that matters to you, clone the repo, audit, and use
`./install.sh build` + `./install.sh install`.

## License

See [LICENSE](LICENSE).
