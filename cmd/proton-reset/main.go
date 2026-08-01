// Command proton-reset signs every account out and empties the vault.
//
// It runs on request, inside the container:
//
//	docker exec -it proton-bridge proton-reset
//
// Afterwards the container is what it was before anyone used it: no accounts,
// no cached mail, nothing to sign anyone in with. The next start brings up the
// sign-in page again.
//
// # Why it asks, and why the answer is typed
//
// This is the only thing in this project that destroys something on purpose.
// The confirmation is part of the feature rather than a nicety: a flag would
// be something a script can carry by accident, and a y/n prompt is answered by
// reflex. Typing the word means having read the line above it.
//
// It is the honest counterpart to a volume that holds the keys to a mailbox.
// There has to be a way to make a container forget an account that does not
// come down to hoping the volume was really deleted.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"golang.org/x/term"
	"google.golang.org/protobuf/types/known/emptypb"
)

const callTimeout = 30 * time.Second

// confirmation is what has to be typed. Not "yes": that is the answer to every
// prompt anyone has ever skimmed.
const confirmation = "reset"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "proton-reset: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Without a terminal there is nobody to ask, and a command that empties a
	// vault must not proceed unasked. Refusing is the only correct behaviour
	// here - it cannot fall back to "assume yes", and falling back to
	// "assume no" while reporting success would be worse.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("no terminal, so there is nobody to confirm this with; run it with `docker exec -it`")
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

	// Read before asking, so the question names what is actually about to be
	// lost rather than describing it in general terms.
	users, err := client.GetUserList(ctx, &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("could not read the account list, so it is not known what this would delete: %w", err)
	}

	accounts := users.GetUsers()

	if len(accounts) == 0 {
		fmt.Println("No account is signed in. There is nothing to reset.")
		return nil
	}

	fmt.Println("This signs the following account(s) out and empties the vault:")
	fmt.Println()

	for _, user := range accounts {
		fmt.Printf("  %-40s %s\n", user.GetUsername(), user.GetState())

		for _, address := range user.GetAddresses() {
			fmt.Printf("    %s\n", address)
		}
	}

	fmt.Println()
	fmt.Println("Afterwards:")
	fmt.Println("  - every mail client configured against this container stops working,")
	fmt.Println("    because the bridge password is generated fresh on the next sign-in")
	fmt.Println("  - all mail is downloaded again from scratch, which for a large mailbox")
	fmt.Println("    takes hours")
	fmt.Println("  - the sign-in page comes back on the next start")
	fmt.Println()
	fmt.Println("Nothing is deleted on Proton's servers. This only affects this container.")
	fmt.Println()
	fmt.Printf("Type %s to continue, anything else to abort: ", confirmation)

	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("could not read the answer: %w", err)
	}

	if strings.TrimSpace(answer) != confirmation {
		fmt.Println("Aborted. Nothing was changed.")
		return nil
	}

	fmt.Println()
	fmt.Println("Resetting.")

	if err := control.Reset(ctx, client.BridgeClient); err != nil {
		return err
	}

	// Confirmed against the bridge inside control.Reset, so this says what is,
	// not what was requested.
	fmt.Println()
	fmt.Println("Done. No account is signed in and the vault is empty.")
	fmt.Println("Sign in again with proton-login, or through the sign-in page.")

	return nil
}
