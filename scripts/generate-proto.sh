#!/usr/bin/env bash
#
# Generates the gRPC client from proto/bridge.proto into internal/bridgepb/.
#
# The result is deliberately not checked in. Generated code in a repository
# drifts from its source and nobody notices; a build step cannot.
#
# Everything is pinned. An unpinned protoc would produce different output on
# every machine, and the first sign of that would be a diff nobody asked for.
#
# Usage:
#   bash scripts/generate-proto.sh
#
# Environment:
#   TOOLS_DIR  where to keep the downloaded tools (default .tools, git-ignored)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly REPO_ROOT

readonly PROTOC_VERSION=35.1
readonly PROTOC_GEN_GO_VERSION=v1.36.11
readonly PROTOC_GEN_GO_GRPC_VERSION=v1.6.2

readonly TOOLS_DIR="${TOOLS_DIR:-$REPO_ROOT/.tools}"
readonly OUT_DIR="internal/bridgepb"

# The module path the generated package belongs to. bridge.proto carries
# Proton's own `option go_package`, and the file is kept byte-for-byte
# identical to upstream, so the target is redirected here instead of by
# editing it. See proto/README.md.
readonly MODULE="github.com/RndmJoker/proton-mail-bridge-docker"

log() { printf '=== %s\n' "$*"; }

# --------------------------------------------------------------------------
# protoc
# --------------------------------------------------------------------------

# The archive also carries the well-known types (empty.proto, wrappers.proto)
# that bridge.proto imports, which is why the include directory below comes
# from it rather than from the system.
install_protoc() {
    local dir="$TOOLS_DIR/protoc-$PROTOC_VERSION"

    if [ -x "$dir/bin/protoc" ]; then
        return 0
    fi

    local arch
    case "$(uname -m)" in
        x86_64)  arch=x86_64 ;;
        aarch64) arch=aarch_64 ;;
        *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
    esac

    local url="https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${arch}.zip"

    log "Fetching protoc $PROTOC_VERSION"
    mkdir -p "$dir"
    curl -sSfLo "$dir/protoc.zip" "$url"
    unzip -qo "$dir/protoc.zip" -d "$dir"
    rm -f "$dir/protoc.zip"
    chmod +x "$dir/bin/protoc"
}

# --------------------------------------------------------------------------
# Plugins
# --------------------------------------------------------------------------

# GOFLAGS is cleared because a vendor directory in the caller's environment
# would otherwise make `go install` resolve against it and fail.
install_plugins() {
    log "Installing protoc plugins"
    GOBIN="$TOOLS_DIR/bin" GOFLAGS='' \
        go install "google.golang.org/protobuf/cmd/protoc-gen-go@$PROTOC_GEN_GO_VERSION"
    GOBIN="$TOOLS_DIR/bin" GOFLAGS='' \
        go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@$PROTOC_GEN_GO_GRPC_VERSION"
}

# --------------------------------------------------------------------------

main() {
    cd "$REPO_ROOT"

    install_protoc
    install_plugins

    local protoc_dir="$TOOLS_DIR/protoc-$PROTOC_VERSION"

    log "Generating $OUT_DIR from proto/bridge.proto"

    rm -rf "$OUT_DIR"
    mkdir -p "$OUT_DIR"

    # M<file>=<path>;<name> overrides the go_package option inside
    # bridge.proto. Both halves are needed: the path decides where the files
    # land, the name after the semicolon decides what the Go package is
    # called. Without the second half the package would be named after the
    # proto namespace, which is "grpc" and collides with the grpc library in
    # every file that imports both.
    #
    # module=<path> strips the module prefix from the output path, so the
    # files land in internal/bridgepb/ rather than in a directory tree
    # mirroring the full import path.
    PATH="$TOOLS_DIR/bin:$PATH" "$protoc_dir/bin/protoc" \
        --proto_path=proto \
        --proto_path="$protoc_dir/include" \
        --go_out=. \
        --go_opt="module=$MODULE" \
        --go_opt="Mbridge.proto=$MODULE/$OUT_DIR;bridgepb" \
        --go-grpc_out=. \
        --go-grpc_opt="module=$MODULE" \
        --go-grpc_opt="Mbridge.proto=$MODULE/$OUT_DIR;bridgepb" \
        bridge.proto

    log "Done:"
    printf '  %s\n' "$OUT_DIR"/*
}

main "$@"
