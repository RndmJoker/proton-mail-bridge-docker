# Security policy

## Reporting a problem

**Do not open a public issue for a security problem.** Use
[private vulnerability reporting](https://github.com/RndmJoker/proton-mail-bridge-docker/security/advisories/new)
instead. It is visible only to the maintainer until a fix exists.

Please include what you would put in a bug report: what happens, how to
reproduce it, and what you expected. Never include credentials, tokens or mail
content, not even redacted.

There is no bounty and no guaranteed response time. This is a spare-time
project.

## What is in scope

This project packages the Proton Mail Bridge, it does not modify it. Relevant
here is everything this repository adds around it:

- the container image and how the bridge is started inside it
- the helper that controls the bridge over gRPC
- the setup page and the login flow
- what ends up in the volume, and how it is protected
- the build workflow and how the bridge source is pinned

Problems in the bridge itself belong to
[Proton](https://github.com/ProtonMail/proton-bridge/security/policy), not
here. If you are unsure which side a problem sits on, report it here and say so.

## Known and accepted

These are documented properties, not vulnerabilities:

- **The volume holds the keys to the mailbox.** It contains a GPG key without a
  passphrase, because unattended startup cannot ask for one. Anyone who can read
  the volume can read the mail. Protecting it is the operator's job.
- **IMAP and SMTP are not meant to face the internet.** They are reachable
  without TLS or with a self-signed certificate, guarded only by the bridge
  password. The readme says to keep them on localhost or behind a tunnel.
- **The setup page accepts the Proton password.** It runs over TLS with a
  self-signed certificate, is bound locally by default, and shuts down once an
  account is signed in.

A report that one of these exists is not a finding. A report that one of them
can be reached in a way the documentation does not describe very much is.
