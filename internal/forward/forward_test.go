package forward

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// A real excerpt in shape: header line, then one socket per line. Port 1143 is
// 0477 in hex, 1025 is 0401. State 0A is LISTEN, 01 is ESTABLISHED.
const procNetTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0477 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 24680 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0401 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 24681 1 0000000000000000 100 0 0 10 0
   2: 0100007F:C350 0100007F:0477 01 00000000:00000000 00:00000000 00000000  1000        0 24682 1 0000000000000000 20 4 30 10 -1
`

func TestIsListening(t *testing.T) {
	tests := []struct {
		name string
		port int
		want bool
	}{
		{name: "IMAP is listening", port: 1143, want: true},
		{name: "SMTP is listening", port: 1025, want: true},
		{name: "a port nobody holds", port: 1144, want: false},
		{
			// 0xC350 is 50000, the client end of the established connection in
			// the sample. It appears in the table but is not a listener, and
			// treating it as one would start socat against nothing.
			name: "an established connection is not a listener",
			port: 50000,
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsListening(strings.NewReader(procNetTCP), test.port); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

// The port is matched as a hex suffix of the address field. Without anchoring
// it to the colon, port 0x0477 would also match an address ending in the same
// digits, and the bridge would appear to be listening when it is not.
func TestIsListeningDoesNotMatchPartOfAnAddress(t *testing.T) {
	// Address 00000477, port 0001: the digits of 1143 appear, as part of the
	// address rather than as the port.
	const table = `  sl  local_address rem_address   st
   0: 00000477:0001 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1
`

	if IsListening(strings.NewReader(table), 1143) {
		t.Fatal("matched the address instead of the port")
	}
}

func TestIsListeningOnEmptyInput(t *testing.T) {
	if IsListening(strings.NewReader(""), 1143) {
		t.Fatal("found a listener in nothing")
	}

	// Header only, which is what the table looks like with no sockets at all.
	if IsListening(strings.NewReader("  sl  local_address rem_address   st\n"), 1143) {
		t.Fatal("found a listener in an empty table")
	}
}

func TestWaitForPort(t *testing.T) {
	t.Run("returns once something listens", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("could not listen: %v", err)
		}

		defer func() { _ = listener.Close() }()

		port := listener.Addr().(*net.TCPAddr).Port

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := WaitForPort(ctx, port, 5*time.Second, 10*time.Millisecond); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("gives up on a port nobody opens", func(t *testing.T) {
		// Port 1 is privileged and nothing in a test environment binds it.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := WaitForPort(ctx, 1, 50*time.Millisecond, 10*time.Millisecond); err == nil {
			t.Fatal("expected an error, got none")
		}
	})
}
