// Package info assembles what an operator needs after setting the container
// up, and formats it for a terminal.
//
// The bridge password is in here. It is not the Proton password: the bridge
// generates one per account and mail clients authenticate with it. There is no
// way to see it without the graphical window, which is why this exists.
//
// It is printed on request only. Printing it at startup would write it into
// every log file that ever gets attached to a bug report.
package info

import (
	"fmt"
	"strings"
)

// Account is one signed-in Proton account.
type Account struct {
	Username  string
	State     string
	Addresses []string
	Password  string
}

// Report is everything proton-info shows.
type Report struct {
	BridgeVersion string

	Address  string
	IMAPPort int
	SMTPPort int
	IMAPSSL  bool
	SMTPSSL  bool

	// Fingerprint of the certificate the mail ports present. Empty if it could
	// not be fetched, which is not fatal: the rest of the report is still
	// worth showing.
	Fingerprint string

	// FingerprintErr explains an empty Fingerprint.
	FingerprintErr string

	// AutomaticUpdates is the bridge's own updater, read from the bridge
	// rather than assumed. bridge-control turns it off at startup; if this
	// ever says otherwise, the image has stopped being a record of what is
	// actually running.
	AutomaticUpdates bool

	Accounts []Account
}

// connectionMode names the TLS mode the way mail clients label it.
func connectionMode(useSSL bool) string {
	if useSSL {
		return "SSL/TLS"
	}

	return "STARTTLS"
}

// Format renders the report.
//
// Written by hand rather than through a table library: this is the one output
// in the project a person reads under pressure, right after setting up a mail
// client that is refusing to connect.
func Format(report Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Proton Mail Bridge %s\n\n", report.BridgeVersion)

	b.WriteString("Mail server\n")
	fmt.Fprintf(&b, "  IMAP                   %s:%d  %s\n", report.Address, report.IMAPPort, connectionMode(report.IMAPSSL))
	fmt.Fprintf(&b, "  SMTP                   %s:%d  %s\n", report.Address, report.SMTPPort, connectionMode(report.SMTPSSL))

	if report.Fingerprint != "" {
		fmt.Fprintf(&b, "  Certificate (SHA-256)  %s\n", report.Fingerprint)
	} else {
		fmt.Fprintf(&b, "  Certificate (SHA-256)  unavailable: %s\n", report.FingerprintErr)
	}

	b.WriteString("\n  The certificate is self-signed and generated on this machine, so every\n")
	b.WriteString("  mail client will ask about it once. Compare what it shows against the\n")
	b.WriteString("  fingerprint above before accepting it.\n")

	b.WriteString("\nUpdates\n")

	if report.AutomaticUpdates {
		b.WriteString("  Bridge self-update     ON\n")
		b.WriteString("\n  This is not how this container is meant to run. A bridge that replaces\n")
		b.WriteString("  its own binary makes the image worthless as a record of what is running,\n")
		b.WriteString("  and the launcher that would carry out the update is not in the image, so\n")
		b.WriteString("  it downloads what it can never apply. Pull a new image instead.\n")
	} else {
		b.WriteString("  Bridge self-update     off\n")
		b.WriteString("\n  A new image is the only way to update. The bridge will not replace its\n")
		b.WriteString("  own binary.\n")
	}

	b.WriteString("\nAccounts\n")

	if len(report.Accounts) == 0 {
		b.WriteString("  None. No account is signed in, so the mail ports answer but serve nothing.\n")

		return b.String()
	}

	for _, account := range report.Accounts {
		fmt.Fprintf(&b, "  %s  (%s)\n", account.Username, strings.ToLower(account.State))
		fmt.Fprintf(&b, "    Addresses        %s\n", strings.Join(account.Addresses, ", "))
		fmt.Fprintf(&b, "    Bridge password  %s\n", account.Password)
	}

	b.WriteString("\n  The bridge password is not your Proton password. It is generated per\n")
	b.WriteString("  account and it is what a mail client authenticates with. Anyone who has\n")
	b.WriteString("  it can read and send your mail through this bridge, so keep it out of\n")
	b.WriteString("  bug reports and screenshots.\n")

	return b.String()
}
