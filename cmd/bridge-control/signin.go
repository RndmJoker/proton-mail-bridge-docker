package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/config"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/forward"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/setup"
)

// How often to look at the account list when nothing has told us to. The event
// stream is the real signal; this is the fallback for the case where an event
// is missed or the stream had to be re-established.
const accountPollInterval = 30 * time.Second

// How long to wait before trying the event stream again after it ended.
const streamRetryDelay = 5 * time.Second

// runSignIn keeps the sign-in page available whenever it is needed, and only
// then.
//
// "Whenever" rather than "at startup": the bridge signs an account out when
// the Proton password changes, when the session is revoked, after a long time
// offline or after a failed sync. A page that only appeared during
// installation would leave a container nobody can get back into.
func runSignIn(ctx context.Context, cfg config.Config, client *bridgeclient.Client) {
	session := login.New(client)

	// Buffered by one: a burst of events only needs to result in one look at
	// the account list, and the sender must never block on this.
	changed := make(chan struct{}, 1)

	go streamEvents(ctx, client, session, changed)

	for ctx.Err() == nil {
		needed, err := control.AccountsNeedSignIn(ctx, client)
		if err != nil {
			logf("WARNING: %v", err)
			waitFor(ctx, changed, accountPollInterval)

			continue
		}

		if !needed {
			waitFor(ctx, changed, accountPollInterval)
			continue
		}

		serveSetup(ctx, cfg, client, session, changed)
	}
}

// waitFor blocks until something happened, the interval passed, or ctx is done.
func waitFor(ctx context.Context, changed <-chan struct{}, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-changed:
	case <-timer.C:
	}
}

// serveSetup runs the page until an account is signed in or ctx is done.
func serveSetup(ctx context.Context, cfg config.Config, client *bridgeclient.Client, session *login.Session, changed <-chan struct{}) {
	settings, err := bridgeclient.SettingsDir()
	if err != nil {
		logf("ERROR: %v", err)
		return
	}

	certDir := filepath.Join(settings, setup.CertDirName)

	options := setup.Options{
		Port:    cfg.SetupPort,
		CertDir: certDir,
		Log:     logf,
	}

	if cfg.SetupExpose {
		if err := exposeSetup(&options, certDir); err != nil {
			logf("ERROR: %v", err)
			return
		}

		// Whatever happens below, the token does not outlive the page.
		defer forgetToken(certDir)
	} else {
		// A token from an earlier run with BRIDGE_SETUP_EXPOSE set has no
		// business surviving in the volume after it was turned off.
		forgetToken(certDir)
	}

	server, err := setup.NewServer(session, options)
	if err != nil {
		logf("ERROR: could not start the sign-in page: %v", err)
		return
	}

	logf("No account is signed in. The sign-in page is at https://%s", server.Address())
	logf("Certificate fingerprint (SHA-256): %s", server.Fingerprint())

	if !cfg.SetupExpose {
		logf("It is bound inside the container. Sign in with: docker exec -it <container> proton-login")
		logf("To reach it from a browser, set BRIDGE_SETUP_EXPOSE=true and read the access token from this log.")
	}

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The page also has to end when an account appears without it, which is
	// what happens when someone signs in through proton-login while a browser
	// has the page open, or when the bridge reconnects an account by itself.
	go watchForAccount(serveCtx, client, session, server, changed)

	if err := server.Serve(ctx); err != nil {
		logf("ERROR: the sign-in page stopped: %v", err)
	}
}

// exposeSetup prepares the options for a page reachable beyond the container.
func exposeSetup(options *setup.Options, certDir string) error {
	address, err := forward.ContainerAddress()
	if err != nil {
		return err
	}

	token, err := setup.NewToken()
	if err != nil {
		return err
	}

	// Written where proton-login can read it, so the terminal way in keeps
	// working while the page is exposed. 0600 in a 0700 directory, owned by
	// the only user in the container.
	if err := setup.SaveToken(certDir, token); err != nil {
		return err
	}

	// 0.0.0.0 rather than the container address alone, so that proton-login
	// still reaches it on 127.0.0.1 from inside.
	options.BindAddress = "0.0.0.0"
	options.Token = token
	options.AllowedHosts = []string{address, "0.0.0.0", "127.0.0.1"}

	logf("WARNING: the sign-in page is reachable beyond the container. It accepts your")
	logf("Proton password. Do not put it on the open internet; use a tunnel or a VPN.")
	logf("")
	logf("The sign-in page is at:")
	logf("  https://127.0.0.1:%d/      inside the container, and for proton-login", options.Port)
	logf("  https://%s:%d/      the container's own address", address, options.Port)
	logf("")

	// The host address is not printed because the container cannot know it.
	// Port publishing happens on the host; from in here there is no way to see
	// which address or which port it landed on, and asking some service on the
	// internet is not something this container is going to do.
	logf("Published with -p %d:%d, it is also at https://<your-host>:%d/", options.Port, options.Port, options.Port)
	logf("")

	// The token is printed here on purpose. It is generated fresh every time
	// this page starts, and the page only starts while no account is signed
	// in, so what is in the log stops working the moment the sign-in
	// succeeds. See security.md.
	logf("Access token: %s", token)
	logf("")

	return nil
}

// forgetToken removes the stored token once the page is gone.
//
// The token only means anything while the page is running, and the page stops
// as soon as an account is signed in. Leaving the file behind would leave a
// dead secret in the volume that looks like a live one to anyone who finds it
// later.
func forgetToken(certDir string) {
	if err := setup.RemoveToken(certDir); err != nil {
		logf("WARNING: could not remove the stored setup token: %v", err)
	}
}

// watchForAccount ends the page once an account is connected.
func watchForAccount(ctx context.Context, client *bridgeclient.Client, session *login.Session, server *setup.Server, changed <-chan struct{}) {
	for {
		if session.Status().State == login.StateSucceeded {
			server.MarkSignedIn()
			session.Reset()

			return
		}

		needed, err := control.AccountsNeedSignIn(ctx, client)
		if err == nil && !needed {
			server.MarkSignedIn()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-time.After(time.Second):
		}
	}
}

// streamEvents keeps the bridge's event stream open and feeds it to the
// session.
//
// The bridge allows exactly one stream, so this is the only place that opens
// one. Everything else that needs to know what the bridge is doing goes
// through the session or through the account list.
func streamEvents(ctx context.Context, client *bridgeclient.Client, session *login.Session, changed chan<- struct{}) {
	for ctx.Err() == nil {
		stream, err := client.RunEventStream(ctx, &bridgepb.EventStreamRequest{ClientPlatform: runtime.GOOS})
		if err != nil {
			logf("WARNING: could not open the bridge event stream: %v", err)
			sleep(ctx, streamRetryDelay)

			continue
		}

		for {
			event, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					logf("WARNING: the bridge event stream ended: %v", err)
				}

				break
			}

			session.Handle(event)

			// Anything about a login or a user may have changed whether the
			// page is needed. Non-blocking, because the buffered slot is
			// enough: one look at the account list covers any number of
			// events.
			if event.GetLogin() != nil || event.GetUser() != nil {
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		}

		sleep(ctx, streamRetryDelay)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
