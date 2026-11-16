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
ntun config edit                        # write profiles (username, forward_server, routes, ...)
ntun check                              # validate the config (schema + cross-profile collisions)
ntun test                               # connectivity probe per profile (auth, agent upload, mux ping)
ntun up                                 # bring every profile's tunnel up (sudo if no setcap)
ntun up --profile internal_server       # one profile only
ntun down                               # tear down every running profile
ntun down internal_server               # tear down one profile
```

## Configuration

Config lives at `~/.tun2org/config.json`: a JSON object of profiles, one
named tunnel per profile — `ntun up` runs them all in one process, docker
compose style, with one `[abc]`-prefixed, per-profile-colored log stream
(colors when stderr is a terminal; NO_COLOR honored).

```json
{
  "internal_server": {
    "username": "your-login",
    "forward_server": "10.0.0.5",
    "auth": "password",
    "routes": ["10.120.0.0/16"],
    "tun_device": "ntun0",
    "tun_network": "198.18.0.1/15"
  },
  "external_server": {
    "username": "your-login",
    "forward_server": "172.16.0.9",
    "forward_port": 2222,
    "auth": "agent",
    "routes": ["172.16.0.0/12"],
    "tun_device": "ntun1",
    "tun_network": "198.20.0.1/15"
  }
}
```

See [config.dummy.json](config.dummy.json) for every option and its
defaults. Profile names are 1-32 characters of `[a-zA-Z0-9_-]` and become the
log prefix: `internal_server` logs as `[int]`.

With more than one profile, cross-profile collisions are rejected at load
time: every profile needs a distinct `tun_device` and a `tun_network`
(v4/v6) disjoint from every other profile's networks and routes, and
`dns_listen` addresses must be unique. `ntun check` reports all of this
without starting anything. Several profiles may share the same
`forward_server` (they open separate sessions); `dns_hijack` may be enabled on
several profiles (it intercepts UDP/53 on its own device only). Reach a
jump host through `proxy_command` (e.g. `ssh -W %h:%p jumper`) inside a
profile.

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
`make build` + `make install`.

## E2E tests (developers, docker required)

Runs the real `ntun` (built in-image from the repo) through a dockerized
topology: an SSH forward server (password auth, tcp-forwarding disabled — the
real-world constraint), an SSH bastion, and HTTP/TCP/UDP endpoints reachable
only through the tunnel. Two scenarios: direct, and through the bastion via
`ssh -W`.

```sh
make e2e                    # both scenarios
./testenv/run.sh bastion    # one scenario only
./testenv/run.sh --keep     # leave the env up for debugging (teardown hint printed)
```

Everything runs in containers (privileged client with `/dev/net/tun` for the
real TUN path); every command is bounded by a timeout controller, and the
environment is torn down on any exit. Needs `docker compose` v2.

## License

See [LICENSE](LICENSE).
