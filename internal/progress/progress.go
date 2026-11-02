// Package progress shows, in a terminal, that a program is still waiting.
//
// The terminal tools spend most of their time waiting: for Proton during a
// sign-in, for the bridge while it switches an account's mode or empties its
// vault. Without a sign of life a wait of a minute looks exactly like a hang,
// and the obvious answer to a hang is Ctrl-C, which in the middle of a sign-in
// or a reset is the one thing that should not happen. Noticed during a sign-in
// by hand on 2026-11-02, #73.
//
// Only in a terminal. In a pipe or a log a line that redraws itself turns into
// a run of carriage returns, so there a spinner does nothing at all and the
// output stays what it was before this package existed. The smoke test relies
// on that.
package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// interval is how often the mark turns: fast enough to look alive, slow enough
// not to flicker.
const interval = 150 * time.Millisecond

// clearLine moves to the start of the line and erases it.
const clearLine = "\r\033[2K"

// frames is the turning mark. Plain ASCII, so it looks the same in every
// terminal and every font.
var frames = []string{"|", "/", "-", `\`}

// Spinner redraws one line of a terminal until it is stopped.
//
// The zero value is a spinner that never started, and every method on it does
// nothing. That is what Start hands out when there is no terminal, so callers
// never have to ask which one they got.
type Spinner struct {
	out  io.Writer
	done chan struct{}
	ran  sync.WaitGroup
	stop sync.Once

	mu    sync.Mutex
	label string
	since time.Time
}

// Start shows label on standard output, behind a turning mark and followed by
// the time it has been showing, if standard output is a terminal.
//
// The spinner owns the line until Stop. Anything else the program prints, it
// prints after that.
func Start(label string) *Spinner {
	return start(os.Stdout, term.IsTerminal(int(os.Stdout.Fd())), label, interval)
}

func start(out io.Writer, terminal bool, label string, every time.Duration) *Spinner {
	if !terminal {
		return &Spinner{}
	}

	s := &Spinner{out: out, done: make(chan struct{}), label: label, since: time.Now()}

	s.ran.Add(1)

	go s.run(every)

	return s
}

func (s *Spinner) run(every time.Duration) {
	defer s.ran.Done()

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for frame := 0; ; frame++ {
		s.draw(frames[frame%len(frames)])

		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
	}
}

func (s *Spinner) draw(mark string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Fprintf(s.out, "%s%s %s...%s", clearLine, mark, s.label, elapsed(time.Since(s.since)))
}

// Say replaces what the line says and starts its count again, for a wait that
// has moved on to its next stage.
func (s *Spinner) Say(label string) {
	if s.out == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if label != s.label {
		s.label = label
		s.since = time.Now()
	}
}

// Stop ends the spinner and erases its line, so that whatever comes next starts
// on a clean one. It returns only once nothing more will be drawn, and it is
// safe to call more than once.
func (s *Spinner) Stop() {
	if s.out == nil {
		return
	}

	s.stop.Do(func() {
		close(s.done)
		s.ran.Wait()

		fmt.Fprint(s.out, clearLine)
	})
}

// elapsed is the count behind the label. Nothing in the first second, where a
// number would only flicker past.
func elapsed(d time.Duration) string {
	if d < time.Second {
		return ""
	}

	return " " + d.Truncate(time.Second).String()
}
