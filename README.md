# proton-mail-bridge-docker

The official [Proton Mail Bridge](https://github.com/ProtonMail/proton-bridge), repackaged as a Docker image for servers: no graphical interface, configured through environment variables, with a one-time login through a temporary web page or the command line.

This is an unofficial project. It is not built, endorsed or supported by Proton.

## Status

Early development. This section always states what actually works, not what is planned.

| Stage | State |
| :--- | :--- |
| Project skeleton and CI | done |
| Image with a running bridge core | done |
| `bridge-control`: gRPC control, environment variables | planned |
| Setup web page and `proton-login` | planned |
| Workflow that rebuilds on every new bridge release | planned |

The image builds and the bridge starts, but **there is still no way to sign in**, so no mail passes through it yet. Do not point a mail client at it.

## How it is meant to work

The bridge has consisted of two parts since version 3: a core written in Go and a separate Qt window, talking to each other over gRPC. The core runs on its own; the window is just a client.

**This project replaces the window, not the core.** A small helper called `bridge-control` starts the bridge core, connects to the same gRPC interface the official window uses, applies your settings, and gets out of the way.

- No account signed in? A setup page comes up over HTTPS, asks for your credentials, and shuts itself down again once the login succeeded. The same thing is available in the terminal as `proton-login`.
- Account signed in? No web server, no prompts, just IMAP and SMTP.
- Your Proton password is never read from an environment variable, and it is never written to a log.

## Security, before anything else

**The volume holds the keys to your entire mailbox.** It contains a GPG key without a passphrase, the `pass` keychain and the bridge vault. Anyone who copies that volume reads all your mail, without knowing a password and without leaving a trace.

Keep it on encrypted storage. Keep it out of backups other people can read. Keep it out of synced folders.

**Never expose IMAP or SMTP to the open internet.** The example configuration binds them to `127.0.0.1` on the host. Reach them from another machine through a tunnel or a VPN, not through an open port.

Running the bridge on a server means decrypted mail lives on that server. That is the trade you are making. Make it knowingly.

## Building and running it

There is no published image yet. Build it from this repository:

```bash
source docker/bridge-version
docker build \
  --build-arg "BRIDGE_COMMIT=$BRIDGE_COMMIT" \
  --build-arg "BRIDGE_VERSION=$BRIDGE_VERSION" \
  -f docker/Dockerfile -t proton-mail-bridge:local .
```

The build fetches the bridge source from Proton at the commit recorded in [`docker/bridge-version`](docker/bridge-version) and compiles it with `make build-nogui`. It takes a few minutes; nothing is downloaded prebuilt.

Then start it:

```bash
docker run -d --name proton-bridge \
  -v proton-bridge-data:/data \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1025:1025 \
  proton-mail-bridge:local
```

At this point the bridge is running with no account, so IMAP answers but has nothing to serve. Signing in comes with the next release.

**Bind mounts need two things a named volume gives you for free.** The container runs as uid 1000 and never as root, so it cannot fix ownership itself:

```bash
chown -R 1000:1000 /your/path          # any host
docker run -v /your/path:/data:Z ...   # SELinux hosts, Fedora and RHEL among them
```

Without the `:Z` the container is denied access on an enforcing system, and the entrypoint stops with an explanation rather than failing somewhere deeper.

### Environment variables

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `BRIDGE_LOG_LEVEL` | `info` | One of `panic`, `fatal`, `error`, `warn`, `info`, `debug` |
| `BRIDGE_IMAP_PORT` | `1143` | Port forwarded to the bridge's IMAP listener |
| `BRIDGE_SMTP_PORT` | `1025` | Port forwarded to the bridge's SMTP listener |
| `BRIDGE_FORWARD_TIMEOUT` | `60` | Seconds to wait for those ports before giving up on forwarding them |

Your Proton credentials are not in that table and never will be. See [Security](#security-before-anything-else).

### Why socat is in there

The bridge binds IMAP and SMTP on `127.0.0.1` only. Inside a container that means nothing outside can reach them, no matter how the ports are published. `socat` listens on the container's own address and forwards to the loopback one, so the bridge still sees a local connection and the port number stays the same on both sides.

The entrypoint starts it only after the bridge is listening. The other way round, the bridge would find its port taken and quietly move to the next one.

## Limitations

These come from the bridge itself or from running inside a container. They will not be fixed here.

- **No passkeys, no FIDO2.** Those need hardware attached to the machine. TOTP is supported.
- **Human verification needs a human.** If Proton asks for it, someone has to open a link in a browser.
- **`amd64` only.** Proton publishes no package for ARM.
- **A paid Proton plan is required.** The bridge is not part of the free tier.

## Relation to proton-mcp

This image is the companion to proton-mcp, an MCP server that speaks IMAP and SMTP to a running bridge. Map the container ports to `127.0.0.1` on the host and proton-mcp connects to them the way it would to a bridge on a desktop.

## Releases

Every version that reaches `main` gets an annotated, signed tag. Verify one before you trust a build:

```bash
git verify-tag v0.1.0
```

Tags are created locally rather than by CI, because the GitHub API can create tags but cannot sign them. A repository rule rejects any tag without a valid signature.

## Licence

GNU General Public License v3.0, see [LICENSE](LICENSE).

The interface definition in `proto/bridge.proto` is taken from the Proton Mail Bridge and carries the same licence, which extends to the gRPC code generated from it. Licensing the whole project under the GPL v3 keeps that straightforward.

The bridge itself is not modified here. It is built from Proton's own source at a commit recorded in this repository, so anyone can reproduce the binary that ends up in the image.
