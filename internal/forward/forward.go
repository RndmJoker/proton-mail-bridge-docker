// Package forward makes the bridge's mail ports reachable from outside the
// container.
//
// The bridge binds IMAP and SMTP on 127.0.0.1 only and offers no way to change
// that. Inside a container that means nothing outside can reach them, however
// the ports are published. socat listens on the container's own address and
// forwards to the loopback one, so the bridge still sees a local connection
// and the port number stays the same on both sides.
//
// Order matters. The bridge picks its ports by asking the kernel which are
// free, and it checks the wildcard address as well as loopback. If socat bound
// the port first, the bridge would move to the next one. So the bridge always
// goes first, and forwarding only follows once the port is actually listening.
package forward

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// procNetTCP files list every TCP socket the kernel knows about. Reading them
// is how a port is checked here rather than by connecting: a probe against the
// IMAP port would show up as a real client session in the bridge's log.
var procNetTCPFiles = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// tcpListen is the state field value for a listening socket.
const tcpListen = "0A"

// IsListening reports whether any socket in the given /proc/net/tcp-style
// table is listening on port.
//
// The address is deliberately ignored. What matters is whether the port is
// taken, not by which address, because that is also how the bridge decides.
func IsListening(r io.Reader, port int) bool {
	suffix := fmt.Sprintf(":%04X", port)

	scanner := bufio.NewScanner(r)

	// The first line is the column header.
	if !scanner.Scan() {
		return false
	}

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())

		// sl, local_address, rem_address, st, ...
		if len(fields) < 4 {
			continue
		}

		if strings.HasSuffix(fields[1], suffix) && fields[3] == tcpListen {
			return true
		}
	}

	return false
}

// portIsListening checks every /proc table that exists on this system.
func portIsListening(port int) bool {
	for _, path := range procNetTCPFiles {
		f, err := os.Open(path) //nolint:gosec // fixed paths, not user input
		if err != nil {
			// tcp6 is absent on a kernel built without IPv6. That is not a
			// failure, there is simply nothing to read there.
			continue
		}

		listening := IsListening(f, port)
		_ = f.Close()

		if listening {
			return true
		}
	}

	return false
}

// WaitForPort blocks until port is listening, ctx is done, or timeout passes.
func WaitForPort(ctx context.Context, port int, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if portIsListening(port) {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("port %d was not listening after %v", port, timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ContainerAddress returns the first non-loopback IPv4 address of this host.
//
// That is the address the published ports are mapped to. Binding the wildcard
// address instead would work as well, but this keeps the listening socket off
// the loopback address, where the bridge would see it as its port being taken.
func ContainerAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			if ip := ipNet.IP.To4(); ip != nil {
				return ip.String(), nil
			}
		}
	}

	return "", errors.New("no non-loopback IPv4 address on any interface")
}

// Forwarder runs one socat process per port.
type Forwarder struct {
	address string
	log     func(format string, args ...any)

	processes []*exec.Cmd
}

// New returns a Forwarder bound to address. log is called for progress and for
// anything that goes wrong in a way that is not fatal.
func New(address string, log func(format string, args ...any)) *Forwarder {
	return &Forwarder{address: address, log: log}
}

// Start waits for port to be listening and then forwards address:port to
// 127.0.0.1:port.
//
// A port that never opens is reported and skipped rather than treated as
// fatal. The bridge may have moved to a different one, in which case it is
// still running and still usable from inside the container, and stopping
// everything would take away the log that explains why.
func (f *Forwarder) Start(ctx context.Context, port int, label string, timeout, interval time.Duration) error {
	if err := WaitForPort(ctx, port, timeout, interval); err != nil {
		f.log("WARNING: %s port %d stays unreachable from outside the container: %v", label, port, err)
		return nil
	}

	listen := fmt.Sprintf("TCP-LISTEN:%d,bind=%s,fork,reuseaddr", port, f.address)
	target := fmt.Sprintf("TCP:127.0.0.1:%d", port)

	cmd := exec.CommandContext(ctx, "socat", listen, target) //nolint:gosec // arguments are built from validated config
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start socat for %s: %w", label, err)
	}

	f.processes = append(f.processes, cmd)
	f.log("Forwarding %s: %s:%d to 127.0.0.1:%d", label, f.address, port, port)

	return nil
}

// Stop terminates every socat started by this Forwarder.
func (f *Forwarder) Stop() {
	for _, cmd := range f.processes {
		if cmd.Process == nil {
			continue
		}

		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}

	f.processes = nil
}
