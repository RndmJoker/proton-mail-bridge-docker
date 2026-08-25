// Command proton-login signs an account in from the terminal.
//
//	docker exec -it proton-bridge proton-login
//
// It is the same sign-in as the setup page, through the same server and the
// same checks. Two ways in, one code path, so they cannot drift apart.
//
// Nothing typed here is echoed, logged or stored. The password goes to the
// bridge, which sends it to Proton; this program keeps no copy.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/config"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/setup"
	"golang.org/x/term"
)

// How long to wait for the bridge to answer a step, and how often to ask.
// Signing in involves a round trip to Proton, so this is not instant.
const (
	stepTimeout  = 3 * time.Minute
	pollInterval = 500 * time.Millisecond
)

// How long the bridge is given to say whether an account is connected, when the
// setup server has gone away mid sign-in. One local gRPC call to a bridge that
// is by then up and running, so this is a backstop rather than a budget.
const bridgeAskTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nproton-login: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	settings, err := bridgeclient.SettingsDir()
	if err != nil {
		return err
	}

	certDir := filepath.Join(settings, setup.CertDirName)

	// The token is read from the volume rather than asked for. It only exists
	// when the page is exposed beyond the container, and inside the container
	// there is nobody to keep it from.
	token, err := setup.LoadToken(certDir)
	if err != nil {
		return err
	}

	client, err := setup.NewClient(fmt.Sprintf("https://127.0.0.1:%d", cfg.SetupPort), certDir, token)
	if err != nil {
		return err
	}

	status, err := client.Status()
	if err != nil {
		return fmt.Errorf("%w\n\nThe sign-in page only runs while no account is signed in. Use proton-info to see whether one already is", err)
	}

	if status.State == login.StateSucceeded {
		fmt.Println("An account is already signed in.")
		return nil
	}

	fmt.Println("Signing in to Proton. Nothing you type here is echoed or logged.")
	fmt.Println()

	return drive(client, status)
}

// drive walks the sign-in until it ends, asking for whatever the bridge asks
// for next.
func drive(client *setup.Client, status login.Status) error {
	reader := bufio.NewReader(os.Stdin)

	for {
		switch status.State {
		case login.StateIdle, login.StateFailed:
			if status.Message != "" {
				fmt.Printf("%s\n\n", capitalise(status.Message))
			}

			if status.State == login.StateFailed && !status.Retryable {
				return errors.New("this sign-in cannot succeed from here")
			}

			var err error
			if status, err = askCredentials(client, reader); err != nil {
				return err
			}

		case login.StateAwaitingTOTP:
			code, err := prompt(reader, "Two-factor code: ")
			if err != nil {
				return err
			}

			status, err = client.TOTP(code)
			if err != nil {
				if status, err = resolveUnreachable(err); err != nil {
					return err
				}
			}

			clear(code)

		case login.StateAwaitingMailboxPassword:
			fmt.Println("This account uses a separate password for the mailbox.")

			password, err := promptSecret("Mailbox password: ")
			if err != nil {
				return err
			}

			status, err = client.MailboxPassword(password)
			if err != nil {
				if status, err = resolveUnreachable(err); err != nil {
					return err
				}
			}

			clear(password)

		case login.StateAwaitingHumanVerification:
			fmt.Println()
			fmt.Println("Proton wants a challenge solved in a browser. Open this link on any")
			fmt.Println("machine, complete it, then come back here:")
			fmt.Println()
			fmt.Printf("  %s\n\n", status.HumanVerificationURL)

			if _, err := prompt(reader, "Press Enter once it is done. "); err != nil {
				return err
			}

			var err error
			if status, err = askCredentials(client, reader); err != nil {
				return err
			}

		case login.StateAwaitingCredentials:
			var err error
			if status, err = waitForChange(client, status); err != nil {
				return err
			}

		case login.StateSucceeded:
			fmt.Println()
			fmt.Println("Signed in. The sign-in page is shutting down.")
			fmt.Println("Run proton-info to see the bridge password for your mail client.")

			return nil

		default:
			return fmt.Errorf("the sign-in reported an unknown state %q", status.State)
		}
	}
}

func askCredentials(client *setup.Client, reader *bufio.Reader) (login.Status, error) {
	username, err := prompt(reader, "Proton username or address: ")
	if err != nil {
		return login.Status{}, err
	}

	if len(username) == 0 {
		return login.Status{}, errors.New("no username given")
	}

	password, err := promptSecret("Password: ")
	if err != nil {
		return login.Status{}, err
	}

	status, err := client.Login(string(username), password)

	clear(password)

	// For an account with no further question this is already the last step, so
	// the page can be the thing that goes away here.
	if err != nil {
		return resolveUnreachable(err)
	}

	fmt.Println("Talking to Proton...")

	return waitForChange(client, status)
}

// waitForChange polls until the bridge has decided what it wants next.
//
// The login call returns immediately and everything real arrives on the
// bridge's event stream, which the server is watching. Polling the server is
// how a terminal sees that.
func waitForChange(client *setup.Client, current login.Status) (login.Status, error) {
	deadline := time.Now().Add(stepTimeout)

	for {
		if current.State != login.StateAwaitingCredentials {
			return current, nil
		}

		if time.Now().After(deadline) {
			return login.Status{}, fmt.Errorf("the bridge did not answer within %v", stepTimeout)
		}

		time.Sleep(pollInterval)

		next, err := client.Status()
		if err != nil {
			return resolveUnreachable(err)
		}

		current = next
	}
}

// resolveUnreachable turns a setup server that has gone away into an answer.
//
// The page shutting itself down is how a successful sign-in ends: bridge-control
// watches the bridge's event stream and stops the server as soon as an account
// is connected. So a call that cannot reach it any more is very probably the
// success this program was waiting for, and reporting a failure there is what
// #35 left behind. Measured on 2026-08-24: a sign-in that had just succeeded
// ended with "connection refused" on the next status poll, 500 ms later.
//
// Confirmed rather than assumed. The page could have stopped for another
// reason, and announcing a sign-in that did not happen would be worse than the
// bug being fixed. The bridge is the authority and it is reachable from inside
// this container over the same socket proton-info uses.
//
// Any error that is not an unreachable server is passed through untouched.
func resolveUnreachable(err error) (login.Status, error) {
	if !errors.Is(err, setup.ErrUnreachable) {
		return login.Status{}, err
	}

	done, askErr := confirmSignedIn()
	if askErr != nil {
		return login.Status{}, fmt.Errorf("%w, and the bridge could not say whether the sign-in finished: %w", err, askErr)
	}

	if !done {
		return login.Status{}, err
	}

	return login.Status{State: login.StateSucceeded}, nil
}

// confirmSignedIn is the question resolveUnreachable asks, as a variable so a
// test can answer it without a bridge. Nothing else reassigns it.
var confirmSignedIn = signedIn

// signedIn asks the bridge whether an account is connected.
func signedIn() (bool, error) {
	configPath, err := bridgeclient.ServerConfigPath()
	if err != nil {
		return false, err
	}

	serverConfig, err := bridgeclient.LoadServerConfig(configPath)
	if err != nil {
		return false, fmt.Errorf("could not read %s: %w", configPath, err)
	}

	client, err := bridgeclient.Dial(serverConfig)
	if err != nil {
		return false, err
	}

	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), bridgeAskTimeout)
	defer cancel()

	needed, reported, err := control.AccountsNeedSignIn(ctx, client)
	if err != nil {
		return false, err
	}

	return reported && !needed, nil
}

// prompt reads a line that may be shown on screen.
func prompt(reader *bufio.Reader, label string) ([]byte, error) {
	fmt.Print(label)

	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return nil, err
	}

	return []byte(strings.TrimRight(line, "\r\n")), nil
}

// promptSecret reads a line without echoing it.
//
// Without a terminal there is nothing to switch off, and reading a password
// from a pipe would put it wherever that pipe came from. Refusing is the
// honest answer: this program is meant to be run with `docker exec -it`.
func promptSecret(label string) ([]byte, error) {
	fd := int(os.Stdin.Fd())

	if !term.IsTerminal(fd) {
		return nil, errors.New("no terminal, so a password cannot be read without echoing it; run this with `docker exec -it`")
	}

	fmt.Print(label)

	secret, err := term.ReadPassword(fd)

	fmt.Println()

	if err != nil {
		return nil, fmt.Errorf("could not read the password: %w", err)
	}

	return secret, nil
}

// capitalise makes a message from the bridge read like a sentence.
func capitalise(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}
