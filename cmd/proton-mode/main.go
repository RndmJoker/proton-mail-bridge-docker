// Command proton-mode shows and switches whether the addresses of an account
// share one mailbox or get one each.
//
//	docker exec proton-bridge proton-mode
//	docker exec -it proton-bridge proton-mode split [account]
//	docker exec -it proton-bridge proton-mode combined [account]
//
// Without arguments it lists the accounts and the mode each one is in. With
// one, it switches an account that is already signed in. For a new account the
// sign-in asks instead, before the first download, which is the cheaper moment.
//
// # What split mode is and is not
//
// Each address becomes its own mailbox, with the address as the username. The
// password stays the same: the bridge keeps one per account and accepts it for
// every address. Split mode decides what a mail client sees, not what the
// password opens.
//
// # Why it asks first
//
// Switching makes the bridge throw away what it has downloaded for the account
// and fetch the whole mailbox again. Like proton-repair it needs a terminal, so
// a stray line in a script cannot start that, and unlike proton-repair it asks:
// it also changes how every mail client connected to this account sees it.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/progress"
	"golang.org/x/term"
	"google.golang.org/protobuf/types/known/emptypb"
)

// callTimeout bounds reading the account list, one local call.
const callTimeout = 30 * time.Second

// switchTimeout is how long the bridge is given to report the new mode. It
// switches in the background and first has to stop the sync that is running,
// so this covers that and not the download that follows.
const switchTimeout = 2 * time.Minute

// readBackInterval is how often to ask while waiting for it.
const readBackInterval = 500 * time.Millisecond

const usage = `usage: proton-mode                     list the accounts and their mode
       proton-mode split [account]     give each address its own mailbox
       proton-mode combined [account]  put all addresses into one mailbox

account is the username or one of its addresses, and only needed when more
than one account is signed in.`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "proton-mode: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	split, wanted, err := parseArgs(args)
	if err != nil {
		return err
	}

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

	listCtx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	list, err := client.GetUserList(listCtx, &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("could not read the accounts from the bridge: %w", err)
	}

	users := list.GetUsers()

	if split == nil {
		fmt.Print(formatModes(users))
		return nil
	}

	user, err := pickAccount(users, wanted)
	if err != nil {
		return err
	}

	if user.GetSplitMode() == *split {
		fmt.Printf("%s is already in %s. Nothing to do.\n", user.GetUsername(), control.ModeName(*split))
		return nil
	}

	// After the cheap answers above and before anything that changes something.
	// Same rule as proton-repair, for the same reason, and here there is also a
	// question to answer.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("no terminal; switching the mode asks first, so run this with `docker exec -it`")
	}

	fmt.Print(explain(user, *split))

	confirmed, err := confirm(bufio.NewReader(os.Stdin), fmt.Sprintf("Switch %s to %s? [y/N] ", user.GetUsername(), control.ModeName(*split)))
	if err != nil {
		return err
	}

	if !confirmed {
		fmt.Println("Nothing was changed.")
		return nil
	}

	switchCtx, cancelSwitch := context.WithTimeout(context.Background(), switchTimeout)
	defer cancelSwitch()

	fmt.Println()
	fmt.Println("Asking the bridge to switch, and waiting until it reports the new mode...")

	// Up to switchTimeout without a word otherwise, #73.
	spinner := progress.Start("Waiting for the bridge")
	after, err := control.SetSplitMode(switchCtx, client.BridgeClient, user.GetId(), *split, readBackInterval)
	spinner.Stop()

	if err != nil {
		return err
	}

	// What the bridge says now, not what was asked of it.
	fmt.Printf("\n%s is now in %s, read back from the bridge.\n\n", after.GetUsername(), control.ModeName(after.GetSplitMode()))
	fmt.Println("The download runs in the background and takes as long as the mailbox")
	fmt.Println("takes. Watch the container log, or run proton-info to see the state.")

	return nil
}

// parseArgs reads the command line. A nil mode means "only list".
func parseArgs(args []string) (*bool, string, error) {
	if len(args) == 0 {
		return nil, "", nil
	}

	if len(args) > 2 {
		return nil, "", errors.New(usage)
	}

	var split bool

	switch args[0] {
	case "split":
		split = true
	case "combined":
		split = false
	default:
		return nil, "", errors.New(usage)
	}

	wanted := ""
	if len(args) == 2 {
		wanted = args[1]
	}

	return &split, wanted, nil
}

// pickAccount finds the account to switch.
//
// By username or by any of its addresses, because the address is what people
// have in front of them. Without a name it takes the only account there is;
// with several it refuses rather than guess, since a wrong guess costs a full
// download of somebody else's mailbox.
func pickAccount(users []*bridgepb.User, wanted string) (*bridgepb.User, error) {
	if len(users) == 0 {
		return nil, errors.New("no account is signed in, so there is no mode to switch; sign in with proton-login first")
	}

	if wanted == "" {
		if len(users) == 1 {
			return users[0], nil
		}

		return nil, fmt.Errorf("%d accounts are signed in, name one: %s", len(users), strings.Join(usernames(users), ", "))
	}

	for _, user := range users {
		if strings.EqualFold(user.GetUsername(), wanted) {
			return user, nil
		}

		for _, address := range user.GetAddresses() {
			if strings.EqualFold(address, wanted) {
				return user, nil
			}
		}
	}

	return nil, fmt.Errorf("no signed-in account is called %q or has it as an address; signed in: %s", wanted, strings.Join(usernames(users), ", "))
}

func usernames(users []*bridgepb.User) []string {
	names := make([]string, 0, len(users))

	for _, user := range users {
		names = append(names, user.GetUsername())
	}

	return names
}

// formatModes is the listing without arguments.
func formatModes(users []*bridgepb.User) string {
	if len(users) == 0 {
		return "No account is signed in, so there is no mode to show.\n"
	}

	var b strings.Builder

	for _, user := range users {
		fmt.Fprintf(&b, "%s  (%s, %s)\n", user.GetUsername(), strings.ToLower(user.GetState().String()), control.ModeName(user.GetSplitMode()))
		fmt.Fprintf(&b, "  Addresses  %s\n", strings.Join(user.GetAddresses(), ", "))
	}

	b.WriteString("\nSwitch with: proton-mode split|combined [account], in a terminal.\n")

	return b.String()
}

// explain says what the switch does before anyone is asked to agree to it.
//
// The password paragraph is there because of what people expect. "Each address
// is its own login" sounds like each address gets its own credential, and a
// configuration handed to a script in that belief opens every address of the
// account. It came up on 2026-11-02, the first time anyone here asked for split
// mode.
func explain(user *bridgepb.User, split bool) string {
	var b strings.Builder

	count := len(user.GetAddresses())

	fmt.Fprintf(&b, "%s is in %s and has %d address(es).\n\n", user.GetUsername(), control.ModeName(user.GetSplitMode()), count)

	if split {
		b.WriteString("  In split mode each address is a mailbox of its own, with the address as\n")
		b.WriteString("  the username. They still share one bridge password, the one\n")
		b.WriteString("  proton-info --secrets shows, and it keeps opening every address. Split\n")
		b.WriteString("  mode changes what a mail client sees, not what the password reaches.\n")
	} else {
		b.WriteString("  In combined mode all addresses share one mailbox. Any of them works as\n")
		b.WriteString("  the username, and each one shows the whole account.\n")
	}

	b.WriteString("\n  The bridge drops what it has downloaded for this account and fetches the\n")
	b.WriteString("  whole mailbox again. That runs in the background and takes as long as\n")
	b.WriteString("  the mailbox takes. Mail clients connected now will see their folders\n")
	b.WriteString("  empty and fill up again, and each of them has to be set up for the new\n")
	b.WriteString("  mode. Nothing is deleted on Proton's servers.\n\n")

	return b.String()
}

// confirm asks a yes-or-no question whose default is no.
func confirm(reader *bufio.Reader, question string) (bool, error) {
	fmt.Print(question)

	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("no answer: %w", err)
	}

	answer := strings.ToLower(strings.TrimSpace(line))

	return answer == "y" || answer == "yes", nil
}
