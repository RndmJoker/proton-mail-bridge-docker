package bridgeclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// serverTokenMetadataKey is the metadata key the bridge checks on every call.
// Without it the service answers Unauthenticated and nothing else.
const serverTokenMetadataKey = "server-token"

// tlsServerName is what the bridge's self-signed certificate is issued for.
//
// It is a fixed string rather than derived from the target, because the target
// is usually a Unix socket, which has no host name at all. The bridge builds
// the certificate with 127.0.0.1 as common name and as its only IP address,
// so this is the only value that verifies.
const tlsServerName = "127.0.0.1"

// Client is a connection to a running bridge.
type Client struct {
	bridgepb.BridgeClient

	conn *grpc.ClientConn
}

// Close releases the connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}

	return c.conn.Close()
}

// transportCredentials builds TLS credentials that trust exactly the
// certificate in the config and nothing else.
//
// The certificate is self-signed and regenerated on every start, so there is
// no authority to check it against. Pinning it is what makes the connection
// worth anything: it is the difference between talking to this bridge and
// talking to whatever else answers on that socket.
func (c ServerConfig) transportCredentials() (credentials.TransportCredentials, error) {
	pool := x509.NewCertPool()

	if !pool.AppendCertsFromPEM([]byte(c.Cert)) {
		return nil, errors.New("the certificate in the gRPC server config could not be parsed")
	}

	return credentials.NewTLS(&tls.Config{
		RootCAs:    pool,
		ServerName: tlsServerName,
		MinVersion: tls.VersionTLS12,
	}), nil
}

// tokenInterceptor attaches the server token to every unary call.
func tokenInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		return invoker(metadata.AppendToOutgoingContext(ctx, serverTokenMetadataKey, token), method, req, reply, cc, opts...)
	}
}

// streamTokenInterceptor attaches the server token to every streaming call.
//
// Separate from the unary one because gRPC keeps the two interceptor chains
// apart. Leaving it out would leave the event stream, and only the event
// stream, rejected as unauthenticated.
func streamTokenInterceptor(token string) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return streamer(metadata.AppendToOutgoingContext(ctx, serverTokenMetadataKey, token), desc, cc, method, opts...)
	}
}

// Dial connects to the bridge described by config.
//
// It does not wait for the connection to be usable: grpc.NewClient connects
// lazily, and the first call is what reports a failure. Callers that want to
// know earlier should make a cheap call such as Version.
func Dial(config ServerConfig) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	creds, err := config.transportCredentials()
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(
		config.Target(),
		grpc.WithTransportCredentials(creds),
		grpc.WithUnaryInterceptor(tokenInterceptor(config.Token)),
		grpc.WithStreamInterceptor(streamTokenInterceptor(config.Token)),
	)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the bridge at %s: %w", config.Target(), err)
	}

	return &Client{
		BridgeClient: bridgepb.NewBridgeClient(conn),
		conn:         conn,
	}, nil
}
