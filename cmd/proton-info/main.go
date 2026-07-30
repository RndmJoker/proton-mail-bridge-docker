// Command proton-info prints what an operator needs after setting the
// container up: the bridge password, the addresses, the ports in use and the
// fingerprint of the self-signed certificate.
//
// It runs on request, inside the container:
//
//	docker exec proton-bridge proton-info
//
// Nothing here is written to a log or printed at startup. The bridge password
// is a credential, and a credential in a log file is a credential in every
// bug report that log is attached to.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgeclient"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/certinfo"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/control"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/info"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The bridge answers over a local socket, so anything slower than this means
// it is not answering at all.
const callTimeout = 15 * time.Second

// The address the bridge itself listens on. The forwards make the same ports
// reachable from outside, but the certificate is the same either way, and this
// works whether or not forwarding succeeded.
const loopback = "127.0.0.1"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "proton-info: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
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

	report, err := collect(ctx, client)
	if err != nil {
		return err
	}

	fmt.Print(info.Format(report))

	return nil
}

// collect gathers the report from a running bridge.
func collect(ctx context.Context, client *bridgeclient.Client) (info.Report, error) {
	var report info.Report

	version, err := client.Version(ctx, &emptypb.Empty{})
	if err != nil {
		return report, fmt.Errorf("the bridge did not answer: %w", err)
	}

	report.BridgeVersion = version.GetValue()
	report.Address = loopback

	settings, err := client.MailServerSettings(ctx, &emptypb.Empty{})
	if err != nil {
		return report, fmt.Errorf("could not read the mail server settings: %w", err)
	}

	report.IMAPPort = int(settings.GetImapPort())
	report.SMTPPort = int(settings.GetSmtpPort())
	report.IMAPSSL = settings.GetUseSSLForImap()
	report.SMTPSSL = settings.GetUseSSLForSmtp()

	// A missing fingerprint is not fatal. The mail port may still be coming
	// up, and everything else in the report is still worth printing.
	if cert, err := certinfo.Fetch(loopback, report.IMAPPort, report.IMAPSSL); err != nil {
		report.FingerprintErr = err.Error()
	} else {
		report.Fingerprint = certinfo.Fingerprint(cert)
	}

	// Read rather than assumed. bridge-control turns this off at startup and
	// refuses to carry on if it does not stick, but this program also runs
	// days later, against a bridge nothing in this process configured.
	report.AutomaticUpdates, err = control.AutomaticUpdatesOn(ctx, client)
	if err != nil {
		return report, err
	}

	users, err := client.GetUserList(ctx, &emptypb.Empty{})
	if err != nil {
		return report, fmt.Errorf("could not read the account list: %w", err)
	}

	for _, user := range users.GetUsers() {
		report.Accounts = append(report.Accounts, info.Account{
			Username:  user.GetUsername(),
			State:     user.GetState().String(),
			Addresses: user.GetAddresses(),
			Password:  string(user.GetPassword()),
		})
	}

	return report, nil
}

// Compile-time check that the generated user state enum is still what the
// report expects to print. If Proton renames it, this stops the build instead
// of quietly printing an empty state.
var _ = bridgepb.UserState_CONNECTED
