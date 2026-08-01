// Command proton-repair makes the bridge reload every account from scratch.
//
// It runs on request, inside the container:
//
//	docker exec -it proton-bridge proton-repair
//
// The bridge throws its cached data away and downloads all mail again. That is
// the way out of a mailbox that has drifted out of step with the server:
// messages that are gone on the server but still in the client, folders that
// do not match, a search that finds nothing it should.
//
// # Why this is a command and not a variable
//
// A variable that repairs on start repairs on every restart, and a restart is
// the first thing anyone tries when something is wrong. That turns a
// fifteen-minute annoyance into an hours-long download, every time, and the
// person doing it would have no idea why.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"golang.org/x/term"
)

// The call itself returns as soon as the bridge has accepted the job; the
// download happens afterwards and takes as long as the mailbox takes.
const callTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "proton-repair: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Same rule as proton-login. Not because this reads a secret, but because
	// it starts something long and expensive, and a command like that should
	// not be reachable by a stray line in a script that meant to run something
	// else. `docker exec` without -it is that stray line.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("no terminal; run this with `docker exec -it`")
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	configPath, err := bridgeclient.ServerConfigPath()
	if err != nil {
		return err
	}

	serverConfig, err := bridgeclient.LoadServerConfig(configPath)
	if err != nil {
		return fmt.Errorf("could not read %s: %w\nIs the bridge running in this container?", configPath, err)
	}

	client, err := bridgeclient.Dial(serverConfig)
	if err != nil {
		return err
	}

	defer func() { _ = client.Close() }()

	fmt.Println("Asking the bridge to reload every account.")
	fmt.Println()

	users, err := control.Repair(ctx, client.BridgeClient)
	if err != nil {
		return err
	}

	// What the bridge says afterwards, not what was asked of it. "Repair
	// requested" tells the reader nothing they did not already know.
	if len(users) == 0 {
		fmt.Println("No account is signed in, so there was nothing to reload.")
		return nil
	}

	fmt.Printf("The bridge now reports %d account(s):\n\n", len(users))

	for _, user := range users {
		fmt.Printf("  %-40s %s\n", user.GetUsername(), user.GetState())
	}

	fmt.Println()
	fmt.Println("The download runs in the background and takes as long as the mailbox")
	fmt.Println("takes. Watch the container log, or run proton-info to see the state.")
	fmt.Println()
	fmt.Println("Your mail client will see messages disappear and come back while this")
	fmt.Println("happens. Nothing is being deleted on the server.")

	return nil
}
