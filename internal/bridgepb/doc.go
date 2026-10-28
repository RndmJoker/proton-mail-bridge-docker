// Package bridgepb is the gRPC client for the Proton Mail Bridge, generated
// from proto/bridge.proto by scripts/generate-proto.sh.
//
// Only this file is checked in; the generated code is not, for the reasons in
// proto/README.md. This file keeps the package path resolvable for tools that
// read the module without generating anything first. Dependabot is one of
// them: it runs go mod tidy after every update, and tidy fails on an import of
// a package that does not exist.
package bridgepb

// If a build stops here with "undefined", the generated code is missing.
// Run: bash scripts/generate-proto.sh
var (
	_ = File_bridge_proto // bridge.pb.go
	_ = NewBridgeClient   // bridge_grpc.pb.go
)
