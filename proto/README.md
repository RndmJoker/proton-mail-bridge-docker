# proto

`bridge.proto` is Proton's own interface definition, taken from the bridge
source at the commit pinned in [`../docker/bridge-version`](../docker/bridge-version),
where it lives at `internal/frontend/grpc/bridge.proto`.

**It is copied unchanged and must stay that way.** The Go package it generates
into is redirected with a `protoc` option rather than by editing the file, so
that byte-for-byte comparison against upstream stays possible. That comparison
is a CI check: [`scripts/ci/check-proto.sh`](../scripts/ci/check-proto.sh)
fetches the file from the pinned commit and refuses a mismatch.

The point of the check is not the copy. It is that raising `BRIDGE_COMMIT`
without looking at the interface would otherwise be silent, and this is the
interface the container drives the bridge through.

The file is GPL v3, which is why this whole project is. See
[`../README.md`](../README.md#licence).

## Generating the client

```bash
bash scripts/generate-proto.sh
```

The result lands in `internal/bridgepb/` and is **not** checked in. Generated
code in a repository drifts from its source without anyone noticing; a build
step cannot.

The one file there that is checked in is `internal/bridgepb/doc.go`, written
by hand. It holds no generated code; it keeps the package path resolvable
before anything has been generated. That matters to every tool that reads the
module as committed. Dependabot is the one that made it necessary: it runs
`go mod tidy` after every update, unconditionally, and tidy fails on an import
of a package that does not exist. Without the file every Go update here failed
from 2026-08-18 on. The `Go` job in CI runs `go mod tidy -diff` before
generating, which is the same view, so a change that breaks this fails there
first.

If a build stops in `doc.go` with `undefined`, the generated files are
missing: run the script above.
