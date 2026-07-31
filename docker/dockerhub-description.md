# proton-mail-bridge-docker

The official [Proton Mail Bridge](https://github.com/ProtonMail/proton-bridge), repackaged for servers: no graphical interface, configured through environment variables, with a one-time login through a temporary web page or the command line.

**Unofficial.** Not built, endorsed or supported by Proton.

Source, issues and full documentation: **https://github.com/RndmJoker/proton-mail-bridge-docker**

## Read this before you start it

**The volume holds the keys to your entire mailbox.** It contains a GPG key without a passphrase, the `pass` keychain and the bridge vault. Anyone who copies that volume reads all your mail, without knowing a password and without leaving a trace.

Keep it on encrypted storage. Keep it out of backups other people can read. Keep it out of synced folders.

**Never expose IMAP or SMTP to the open internet.** Bind them to `127.0.0.1` on the host, as below. Reach them from another machine through a tunnel or a VPN, not through an open port. The bridge serves them unencrypted or with a self-signed certificate, and whoever reaches them needs only the bridge password.

Running the bridge on a server means decrypted mail lives on that server. That is the trade you are making. Make it knowingly.

## Early software

No test in this repository uses a real Proton account, so the sign-in itself is exercised by hand rather than by CI. Keep a copy of anything you care about elsewhere.

## Running it

```bash
docker run -d --name proton-bridge \
  -v proton-bridge-data:/data \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1025:1025 \
  rndmjoker/proton-mail-bridge-docker:edge

docker exec -it proton-bridge proton-login
```

`proton-login` asks for your Proton credentials and whatever Proton asks for next. Nothing you type is echoed, logged or stored. Afterwards `docker exec proton-bridge proton-info` shows the bridge password your mail client needs.

## Tags

| Tag | What it means |
| :--- | :--- |
| `3.25.0-0.5.0` | bridge version and image version. Rebuilt nightly for base image updates |
| `3.25.0`, `3.25` | newest image for that bridge version |
| `edge` | the newest build, whatever it contains |

There is no `latest` tag. Every release so far is a pre-release, and a `latest` tag would claim otherwise.

**Only a digest is immutable.** Every tag above moves, because the image is rebuilt nightly so that Debian's security updates reach it. Pin `@sha256:...` if you need a fixed target.

## Provenance

Every published image carries signed build provenance, keyless through Sigstore. Check where it came from before you trust it with a mailbox:

```bash
gh attestation verify oci://docker.io/rndmjoker/proton-mail-bridge-docker:edge \
  --repo RndmJoker/proton-mail-bridge-docker
```

## Licence

MIT. The bridge itself is GPL-3.0 and is built from Proton's published source at a pinned commit.
