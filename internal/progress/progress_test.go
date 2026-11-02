package progress

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer lets a test read what the spinner wrote while it is still
// writing. A plain bytes.Buffer would be a data race there.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// eventually waits for something the spinner draws on its own schedule,
// rather than sleeping a fixed time and hoping the scheduler kept up.
func eventually(t *testing.T, out *syncBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("never drew %q, got %q", want, out.String())
		}

		time.Sleep(time.Millisecond)
	}
}

// Without a terminal nothing at all is written. This is what keeps scripts,
// logs and the smoke test looking exactly as they did before.
func TestWithoutATerminalNothingIsWritten(t *testing.T) {
	var out syncBuffer

	s := start(&out, false, "Waiting for Proton", time.Millisecond)
	s.Say("Setting the address mode")
	time.Sleep(10 * time.Millisecond)
	s.Stop()
	s.Stop()

	if written := out.String(); written != "" {
		t.Fatalf("wrote %q without a terminal", written)
	}
}

func TestTheMarkTurnsAndTheLineIsClearedAtTheEnd(t *testing.T) {
	var out syncBuffer

	s := start(&out, true, "Waiting for Proton", time.Millisecond)

	eventually(t, &out, "| Waiting for Proton...")
	eventually(t, &out, "/ Waiting for Proton...")

	s.Stop()

	if written := out.String(); !strings.HasSuffix(written, clearLine) {
		t.Fatalf("the line was not erased at the end: %q", written)
	}
}

// Whatever the program prints after Stop must not be overwritten by a frame
// that was still on its way.
func TestNothingIsDrawnAfterStop(t *testing.T) {
	var out syncBuffer

	s := start(&out, true, "Waiting for Proton", time.Millisecond)
	eventually(t, &out, "Waiting for Proton")
	s.Stop()

	stopped := out.String()

	time.Sleep(20 * time.Millisecond)

	if now := out.String(); now != stopped {
		t.Fatalf("drew after Stop: %q", strings.TrimPrefix(now, stopped))
	}
}

func TestSayReplacesTheLabel(t *testing.T) {
	var out syncBuffer

	s := start(&out, true, "Waiting for Proton", time.Millisecond)
	eventually(t, &out, "Waiting for Proton...")

	s.Say("Setting the address mode")
	eventually(t, &out, "Setting the address mode...")

	s.Stop()
}

func TestStopTwiceIsHarmless(t *testing.T) {
	var out syncBuffer

	s := start(&out, true, "Waiting", time.Millisecond)
	s.Stop()
	s.Stop()

	if strings.Count(out.String(), clearLine) < 1 {
		t.Fatalf("the line was never erased: %q", out.String())
	}
}

func TestTheZeroSpinnerDoesNothing(t *testing.T) {
	var s Spinner

	s.Say("anything")
	s.Stop()
}

func TestElapsed(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{999 * time.Millisecond, ""},
		{1500 * time.Millisecond, " 1s"},
		{75 * time.Second, " 1m15s"},
	}

	for _, c := range cases {
		if got := elapsed(c.in); got != c.want {
			t.Errorf("elapsed(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
