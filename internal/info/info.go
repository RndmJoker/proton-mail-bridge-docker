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

	// SplitMode is the bridge's per-account setting for whether each address is
	// its own login. False is combined mode, which is Proton's default and the
	// source of the surprise this report now names.
	SplitMode bool
}

// Report is everything proton-info shows.
type Report struct {
	BridgeVersion string

	Address  string
	IMAPPort int
	SMTPPort int
	IMAPSSL  bool
	SMTPSSL  bool

	// PublicIMAPPort and PublicSMTPPort are what the operator published the
	// ports as on the host. Zero when they were not declared.
	//
	// The container cannot find this out. Publishing a port happens outside it,
	// and from inside there is no way to see which port it landed on. The only
	// mechanism that would work is reading the container engine's socket, which
	// means handing a container that holds a mailbox control of the engine, for
	// a line of output. So it is asked for instead.
	PublicIMAPPort int
	PublicSMTPPort int

	// Secrets is whether the bridge password may be printed. False leaves the
	// report safe to paste into a bug report, which is what people do with it.
	Secrets bool

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

	// AlternativeRouting, ShowAllMail and Telemetry are the three plain
	// settings, read back from the bridge rather than echoed from the
	// environment. They live in the vault and survive restarts, so what this
	// container was configured with and what the bridge currently believes are
	// two different questions.
	AlternativeRouting bool
	ShowAllMail        bool
	Telemetry          bool

	Accounts []Account
}

// connectionMode names the TLS mode the way mail clients label it.
func connectionMode(useSSL bool) string {
	if useSSL {
		return "SSL/TLS"
	}

	return "STARTTLS"
}

// modeName labels the account mode the way Proton's own settings do.
func modeName(splitMode bool) string {
	if splitMode {
		return "split mode"
	}

	return "combined mode"
}

// modeExplanation says what the mode means for the password below it.
//
// The sentence exists because of what somebody does with the answer they never
// got. In combined mode the bridge password belongs to the account, not to an
// address, so a configuration handed to a script or another person in the
// belief that it opens one address opens all of them. Found on 2026-07-31 by
// signing in with a single address and receiving the entire mailbox, which is
// correct behaviour and the last thing anyone expects.
func modeExplanation(account Account) string {
	if account.SplitMode {
		return fmt.Sprintf("    Each of these %d addresses is its own login, with its own password.\n",
			len(account.Addresses))
	}

	return fmt.Sprintf("    All %d addresses share one login and one mailbox. The password below\n"+
		"    opens the whole account, whichever address is used as the username.\n",
		len(account.Addresses))
}

// hostPorts names the ports on the host, or says that the container cannot
// know them and gives the form to fill in.
//
// The same shape as the sign-in page, which prints the addresses it can know,
// names the one it cannot, and shows what to substitute. Printing a number
// without saying what it is the answer to is how somebody ends up typing 1143
// into a mail client that needed 11143.
func hostPorts(report Report) string {
	var b strings.Builder

	b.WriteString("\n")

	if report.PublicIMAPPort == 0 && report.PublicSMTPPort == 0 {
		b.WriteString("  Those are the ports inside the container. Which ports they were\n")
		b.WriteString("  published as on the host is decided outside it and cannot be seen from\n")
		b.WriteString("  here. Published with -p 11143:%d, IMAP is at 127.0.0.1:11143 on your\n")
		b.WriteString("  host. Set BRIDGE_PUBLIC_IMAP_PORT and BRIDGE_PUBLIC_SMTP_PORT to have\n")
		b.WriteString("  them named here instead.\n")

		return strings.Replace(b.String(), "%d", fmt.Sprint(report.IMAPPort), 1)
	}

	b.WriteString("  On your host, as published:\n")

	if report.PublicIMAPPort != 0 {
		fmt.Fprintf(&b, "  IMAP                   127.0.0.1:%d\n", report.PublicIMAPPort)
	}

	if report.PublicSMTPPort != 0 {
		fmt.Fprintf(&b, "  SMTP                   127.0.0.1:%d\n", report.PublicSMTPPort)
	}

	b.WriteString("\n  Those come from BRIDGE_PUBLIC_IMAP_PORT and BRIDGE_PUBLIC_SMTP_PORT, so\n")
	b.WriteString("  they are what somebody said, not what the container measured.\n")

	return b.String()
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
	fmt.Fprintf(&b, "  IMAP                   %s:%d  %s   inside the container\n", report.Address, report.IMAPPort, connectionMode(report.IMAPSSL))
	fmt.Fprintf(&b, "  SMTP                   %s:%d  %s   inside the container\n", report.Address, report.SMTPPort, connectionMode(report.SMTPSSL))

	if report.Fingerprint != "" {
		fmt.Fprintf(&b, "  Certificate (SHA-256)  %s\n", report.Fingerprint)
	} else {
		fmt.Fprintf(&b, "  Certificate (SHA-256)  unavailable: %s\n", report.FingerprintErr)
	}

	b.WriteString(hostPorts(report))

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

	b.WriteString("\nSettings\n")
	fmt.Fprintf(&b, "  Alternative routing    %s\n", onOff(report.AlternativeRouting))
	fmt.Fprintf(&b, "  Show All Mail          %s\n", onOff(report.ShowAllMail))
	fmt.Fprintf(&b, "  Usage diagnostics      %s\n", onOff(report.Telemetry))

	// Read from the bridge, not from our own environment, and that difference
	// is worth stating. These live in the vault and survive a restart, so a
	// variable removed from the compose file does not undo what it once set.
	// Somebody looking for why All Mail is still there needs to know that.
	b.WriteString("\n  Read from the bridge, not from this container's environment. They live\n")
	b.WriteString("  in the vault and survive restarts, so removing BRIDGE_SHOW_ALL_MAIL does\n")
	b.WriteString("  not undo what it set - change the value instead.\n")

	if report.Telemetry {
		b.WriteString("\n  Usage diagnostics are ON. This container defaults them off; something\n")
		b.WriteString("  set BRIDGE_TELEMETRY=true, or they were turned on in Proton's own\n")
		b.WriteString("  application against this vault.\n")
	}

	b.WriteString("\nAccounts\n")

	if len(report.Accounts) == 0 {
		b.WriteString("  None. No account is signed in, so the mail ports answer but serve nothing.\n")

		return b.String()
	}

	for _, account := range report.Accounts {
		fmt.Fprintf(&b, "  %s  (%s, %s)\n", account.Username, strings.ToLower(account.State), modeName(account.SplitMode))
		fmt.Fprintf(&b, "    Addresses        %s\n", strings.Join(account.Addresses, ", "))
		b.WriteString(modeExplanation(account))

		if report.Secrets {
			fmt.Fprintf(&b, "    Bridge password  %s\n", account.Password)
		} else {
			b.WriteString("    Bridge password  hidden, run with --secrets to show it\n")
		}
	}

	if report.Secrets {
		b.WriteString("\n  This output contains a credential.\n")
		b.WriteString("\n  The bridge password is not your Proton password. It is generated per\n")
		b.WriteString("  account and it is what a mail client authenticates with. Anyone who has\n")
		b.WriteString("  it can read and send your mail through this bridge, so keep it out of\n")
		b.WriteString("  bug reports and screenshots.\n")
	}

	return b.String()
}

// onOff names a boolean the way the rest of this report does: a capitalised ON
// for the state worth noticing, lower case for the quiet one.
func onOff(v bool) string {
	if v {
		return "ON"
	}

	return "off"
}
