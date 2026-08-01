# proton-mail-bridge-docker

The official [Proton Mail Bridge](https://github.com/ProtonMail/proton-bridge), repackaged as a Docker image for servers: no graphical interface, configured through environment variables, with a one-time login through a temporary web page or the command line.

This is an unofficial project. It is not built, endorsed or supported by Proton.

## Status

Early development. This section always states what actually works, not what is planned.

| Stage | State |
| :--- | :--- |
| Project skeleton and CI | done |
| Image with a running bridge core | done |
| `bridge-control`: gRPC control, environment variables | done |
| Setup web page and `proton-login` | done |
| Published images, signed provenance, nightly rebuild | done |
| Compose example and a walkthrough for a stranger | planned |

The image builds, the bridge starts, and an account can be signed in. **No test in this repository uses a real Proton account**, so the sign-in itself is exercised by hand rather than by CI. Treat this as early software and keep a copy of anything you care about elsewhere.

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

## Where the images are

Two registries, same image, same digest:

```
ghcr.io/rndmjoker/proton-mail-bridge-docker
docker.io/rndmjoker/proton-mail-bridge-docker
```

| Tag | What it means |
| :--- | :--- |
| `3.25.0-0.5.0` | bridge version and image version |
| `3.25.0`, `3.25` | newest image for that bridge version |
| `edge` | the newest build, whatever it contains |

**There is no `latest` tag.** Every release so far is marked as a pre-release, and a `latest` tag would tell every tool that looks for one the opposite. It will exist once a version is ready to carry that name.

**Only a digest is immutable.** All of the tags above move: the image is rebuilt every night against the same pinned bridge commit on a newer Debian base, which is how security updates reach it between two releases. Pin `@sha256:...` if you need a fixed target.

```bash
docker run -d --name proton-bridge \
  -v proton-bridge-data:/data \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1025:1025 \
  ghcr.io/rndmjoker/proton-mail-bridge-docker:edge
```

Signing in and everything after it works the same as below; only the image name differs.

### What an image says about itself

Every image carries the standard OCI labels, plus two that name the upstream build it contains:

```bash
docker inspect --format '{{json .Config.Labels}}' ghcr.io/rndmjoker/proton-mail-bridge-docker:edge
```

The same is readable from inside a running container, where `docker inspect` is not available:

```bash
docker exec proton-bridge cat /etc/image-provenance
```

```
image-version=0.5.1
image-revision=186d1f603e356a8c3d5b11cb6106953398229ce4
bridge-version=3.25.0
bridge-commit=f1f599e97167265cb0d10ad3d169269c324d9cc7
```

`bridge-commit` is the one worth keeping: it is the exact upstream commit the bridge inside was compiled from, so an image can be traced back to Proton's source without trusting anything this repository says about it.

### Checking where an image came from

Every published image carries signed build provenance. It records which workflow built it, from which repository, at which commit, and it is signed keyless through Sigstore, so there is no signing key anywhere for someone to steal.

```bash
gh attestation verify oci://ghcr.io/rndmjoker/proton-mail-bridge-docker:edge \
  --repo RndmJoker/proton-mail-bridge-docker
```

The same works for the Docker Hub copy:

```bash
gh attestation verify oci://docker.io/rndmjoker/proton-mail-bridge-docker:edge \
  --repo RndmJoker/proton-mail-bridge-docker
```

This is worth doing once for something that will hold the keys to your mailbox.

## Building it yourself

The image is built from Proton's source rather than from a binary, and you can do the same:

```bash
source docker/bridge-version
docker build \
  --build-arg "BRIDGE_COMMIT=$BRIDGE_COMMIT" \
  --build-arg "BRIDGE_VERSION=$BRIDGE_VERSION" \
  --build-arg "IMAGE_VERSION=$(cat VERSION)" \
  --build-arg "IMAGE_REVISION=$(git rev-parse HEAD)" \
  -f docker/Dockerfile -t proton-mail-bridge:local .
```

All four are required and the build stops without them. `scripts/ci/smoke-test.sh` works them out itself, so it is the shorter way to the same image plus the checks that go with it.

The build fetches the bridge source from Proton at the commit recorded in [`docker/bridge-version`](docker/bridge-version) and compiles it with `make build-nogui`. It takes a few minutes; nothing is downloaded prebuilt. In a second stage it builds `bridge-control` and `proton-info` from this repository, including the gRPC client, which is generated during the build rather than kept in the repository. See [`proto/README.md`](proto/README.md).

Then start it:

```bash
docker run -d --name proton-bridge \
  -v proton-bridge-data:/data \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1025:1025 \
  proton-mail-bridge:local
```

Then sign in:

```bash
docker exec -it proton-bridge proton-login
```

It asks for your Proton username and password, then for whatever Proton asks for next: a two-factor code, a separate mailbox password, or a link to open for human verification. Nothing you type is echoed, logged or stored.

Afterwards, `docker exec proton-bridge proton-info` shows the bridge password your mail client needs.

### Signing in through a browser instead

The sign-in page runs whenever no account is signed in, and shuts down as soon as one is. By default it is bound inside the container, which means no browser can reach it: the same reason the mail ports need `socat`. That is deliberate, because the page accepts your Proton password.

To open it anyway:

```bash
docker run -d --name proton-bridge \
  -v proton-bridge-data:/data \
  -e BRIDGE_SETUP_EXPOSE=true \
  -p 127.0.0.1:8443:8443 \
  proton-mail-bridge:local

docker logs proton-bridge     # the access token and the certificate fingerprint
```

The log prints an access token, generated fresh at every start, which the page then demands. It also prints the fingerprint of the certificate, which is created once and kept in the volume so it stays the same across restarts. Compare it against what your browser shows before you type a password into that page.

**Even with a token, this belongs behind a tunnel or a VPN.** It is a page that takes the password to your entire mailbox.

### What runs inside

`bridge-control` is what turns "a bridge that runs" into "a container that can be configured". It starts the bridge core, connects to the same gRPC interface Proton's own window uses, applies the settings from the environment, turns off the bridge's self-updater, and forwards the mail ports. If any of that fails the container stops rather than running in a state its log does not describe.

`proton-info` prints what you need once an account is signed in:

```bash
docker exec proton-bridge proton-info
```

It shows the addresses, the ports, the fingerprint of the self-signed certificate your mail client will ask about, and whether the bridge's own updater is off.

**It does not print the bridge password unless you ask:**

```bash
docker exec proton-bridge proton-info --secrets
```

The default output is safe to paste into a bug report, and revealing a credential is a thing you did on purpose rather than a side effect of asking about ports.

**The ports it prints are the container's own.** Which ports they were published as on your host is decided outside the container and cannot be seen from inside it, so `proton-info` says so and shows the form to fill in. Set `BRIDGE_PUBLIC_IMAP_PORT` and `BRIDGE_PUBLIC_SMTP_PORT` and it names them instead, marked as something you said rather than something it measured.

That matters immediately if you already run Proton's desktop bridge, which holds 1143 and 1025 on the host, so this container has to be published elsewhere.

**Read the account mode it reports.** In combined mode, which is Proton's default, all addresses of an account share one login and the bridge password belongs to the account rather than to any one address:

```
rndmjoker  (connected, combined mode)
  Addresses        a@example.com, b@example.com, c@example.com
  All 3 addresses share one login and one mailbox. The password below
  opens the whole account, whichever address is used as the username.
```

Typing one address as the username and receiving the whole mailbox is correct behaviour and the last thing most people expect. **A configuration handed to a script or another person in the belief that it opens one address opens all of them.** Split mode, where each address is its own login, is set in Proton's own bridge settings and is not yet reachable from this container.

Arguments after the image name replace `bridge-control`, after the volume and the keychain have been prepared either way:

```bash
docker run --rm proton-mail-bridge:local bridge --version
docker run --rm -it -v proton-bridge-data:/data proton-mail-bridge:local bash
```

That is a way in for troubleshooting. It is also how the smoke test gets an unconfigured bridge to compare against.

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
| `BRIDGE_IMAP_PORT` | `1143` | Port the bridge serves IMAP on, inside the container and outside |
| `BRIDGE_SMTP_PORT` | `1025` | Port the bridge serves SMTP on, inside the container and outside |
| `BRIDGE_IMAP_SSL` | `false` | Direct TLS instead of STARTTLS for IMAP |
| `BRIDGE_SMTP_SSL` | `false` | Direct TLS instead of STARTTLS for SMTP |
| `BRIDGE_SETUP_PORT` | `8443` | Port for the sign-in page |
| `BRIDGE_SETUP_EXPOSE` | `false` | Make the sign-in page reachable from outside the container, with an access token |
| `BRIDGE_START_TIMEOUT` | `120` | Seconds to wait for the bridge's gRPC service before giving up |
| `BRIDGE_FORWARD_TIMEOUT` | `60` | Seconds to wait for a mail port before giving up on forwarding it |

A commented copy is in [`.env.example`](.env.example). An unreadable value is refused at startup rather than silently replaced by a default: a container that quietly listens somewhere other than it was told to is worse than one that does not start.

Your Proton credentials are not in that table and never will be. See [Security](#security-before-anything-else).

### Why socat is in there

The bridge binds IMAP and SMTP on `127.0.0.1` only. Inside a container that means nothing outside can reach them, no matter how the ports are published. `socat` listens on the container's own address and forwards to the loopback one, so the bridge still sees a local connection and the port number stays the same on both sides.

`bridge-control` starts it only after the bridge is listening. The other way round, the bridge would find its port taken and quietly move to the next one.

## Limitations

These come from the bridge itself or from running inside a container. They will not be fixed here.

- **No passkeys, no FIDO2.** Those need hardware attached to the machine. TOTP is supported.
- **Human verification needs a human.** If Proton asks for it, someone has to open a link in a browser.
- **`amd64` only.** Proton publishes no package for ARM.
- **A paid Proton plan is required.** The bridge is not part of the free tier.

## Relation to proton-mcp

[proton-mcp](https://github.com/RndmJoker/proton-mcp) is an MCP server that speaks IMAP and SMTP to a running bridge and exposes the mailbox to an assistant. The two projects fit together, but neither needs the other:

- **This image** runs the bridge where there is no desktop to run the official application on.
- **proton-mcp** connects to a bridge, whether that bridge runs in this container or on a desktop.

Map the container ports to `127.0.0.1` on the host and proton-mcp connects to them the way it would to a bridge on a desktop.

If the two run on different machines, the mail ports have to be reachable across that gap, which is exactly the situation this readme warns about. Use a tunnel or a VPN, never an open port. Decrypted mail crosses that connection.

## Releases

Every version that reaches `main` gets an annotated, signed tag. Verify one before you trust a build:

```bash
git verify-tag v0.1.0
```

Tags are created locally rather than by CI, because the GitHub API can create tags but cannot sign them. A repository rule rejects any tag without a valid signature.

**A tag publishes nothing.** It records that a version existed, and that is all it does. Publishing is started deliberately, naming what to build, or it happens as the nightly rebuild of whatever is on `main`. Nothing reaches a registry that did not come through a pull request, pass the smoke test in the same run, and get its provenance signed. See [Where the images are](#where-the-images-are).

It used to work the other way, and that is worth saying plainly: pushing a tag published an image. On 2026-07-31 two tags were added to older commits to complete the record, and both published, because a workflow run uses the workflow file as it was at the tagged commit - so the `PUBLISH=no` that had since been merged did not apply to them. Two unfinished builds were publicly pullable for about 45 minutes before they were deleted. Nothing was leaked; the image holds no secrets. What was wrong is that nobody decided it. See [#40](https://github.com/RndmJoker/proton-mail-bridge-docker/issues/40).

**Whether anything may be published is decided in the [`PUBLISH`](PUBLISH) file**, not in a settings page. While it says `no` the publish job is skipped. The file says why it is where it is.

Publishing a particular version, once `PUBLISH` allows it:

```bash
gh workflow run publish.yml --ref main -f ref=v0.6.0
```

Started on `main` so the workflow file comes from there, building whatever `ref` names. The run refuses to start on any other branch, because a run started on a tag would read the workflow - guards included - from that tag.

A new upstream bridge release does **not** publish anything by itself. A scheduled check notices it and files an issue; what Proton changed is looked at by a person before it goes out under this name.

## Licence

GNU General Public License v3.0, see [LICENSE](LICENSE).

The interface definition in `proto/bridge.proto` is taken from the Proton Mail Bridge and carries the same licence, which extends to the gRPC code generated from it. Licensing the whole project under the GPL v3 keeps that straightforward.

The bridge itself is not modified here. It is built from Proton's own source at a commit recorded in this repository, so anyone can reproduce the binary that ends up in the image.
