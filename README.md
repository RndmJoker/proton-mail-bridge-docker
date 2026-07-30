# proton-mail-bridge-docker

The official [Proton Mail Bridge](https://github.com/ProtonMail/proton-bridge), repackaged as a Docker image for servers: no graphical interface, configured through environment variables, with a one-time login through a temporary web page or the command line.

This is an unofficial project. It is not built, endorsed or supported by Proton.

## Status

Early development. This section always states what actually works, not what is planned.

| Stage | State |
| :--- | :--- |
| Project skeleton and CI | done |
| Image with a running bridge core | planned |
| `bridge-control`: gRPC control, environment variables | planned |
| Setup web page and `proton-login` | planned |
| Workflow that rebuilds on every new bridge release | planned |

Nothing here is usable yet. Do not point a mail client at it.

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

## Limitations

These come from the bridge itself or from running inside a container. They will not be fixed here.

- **No passkeys, no FIDO2.** Those need hardware attached to the machine. TOTP is supported.
- **Human verification needs a human.** If Proton asks for it, someone has to open a link in a browser.
- **`amd64` only.** Proton publishes no package for ARM.
- **A paid Proton plan is required.** The bridge is not part of the free tier.

## Relation to proton-mcp

This image is the companion to proton-mcp, an MCP server that speaks IMAP and SMTP to a running bridge. Map the container ports to `127.0.0.1` on the host and proton-mcp connects to them the way it would to a bridge on a desktop.

## Licence

GNU General Public License v3.0, see [LICENSE](LICENSE).

The interface definition in `proto/bridge.proto` is taken from the Proton Mail Bridge and carries the same licence, which extends to the gRPC code generated from it. Licensing the whole project under the GPL v3 keeps that straightforward.

The bridge itself is not modified here. It is built from Proton's own source at a commit recorded in this repository, so anyone can reproduce the binary that ends up in the image.
