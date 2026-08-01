package info

import (
	"strings"
	"testing"
)

func exampleReport() Report {
	return Report{
		BridgeVersion: "3.25.0",
		Address:       "127.0.0.1",
		IMAPPort:      1143,
		SMTPPort:      1025,
		Fingerprint:   "AB:CD:EF",
		Accounts: []Account{
			{
				Username:  "someone@example.invalid",
				State:     "CONNECTED",
				Addresses: []string{"someone@example.invalid", "someone-else@example.invalid"},
				Password:  "not-a-real-password",
			},
		},
	}
}

func TestFormat(t *testing.T) {
	out := Format(exampleReport())

	// The password is deliberately not in this list. It used to be, which is
	// what made this test assert the behaviour of #30.
	for _, want := range []string{
		"3.25.0",
		"127.0.0.1:1143",
		"127.0.0.1:1025",
		"AB:CD:EF",
		"someone@example.invalid",
		"someone-else@example.invalid",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from the output:\n%s", want, out)
		}
	}
}

// TestFormatHidesThePasswordByDefault is #30.
//
// What people paste into a bug report is whatever the command printed. Until
// this changed, that always contained a live credential: on 2026-07-31 an
// invocation meant only to check whether an account was connected put one into
// a session transcript.
func TestFormatHidesThePasswordByDefault(t *testing.T) {
	out := Format(exampleReport())

	if strings.Contains(out, "not-a-real-password") {
		t.Errorf("the password is in the default output:\n%s", out)
	}

	// Hidden is not the same as absent. Somebody who needs it has to be told
	// how to get it, or they will go looking somewhere worse.
	if !strings.Contains(out, "--secrets") {
		t.Errorf("the output does not say how to get the password:\n%s", out)
	}
}

func TestFormatShowsThePasswordWhenAsked(t *testing.T) {
	report := exampleReport()
	report.Secrets = true

	out := Format(report)

	if !strings.Contains(out, "not-a-real-password") {
		t.Errorf("the password is missing although it was asked for:\n%s", out)
	}

	// Asking for it is deliberate; being told what the output now contains is
	// what makes the next paste deliberate too.
	if !strings.Contains(out, "contains a credential") {
		t.Errorf("nothing says the output now carries a secret:\n%s", out)
	}
}

// TestFormatNamesTheAccountMode is #28.
//
// In combined mode the bridge password belongs to the account rather than to an
// address, so a configuration handed on in the belief that it opens one address
// opens all of them. Nothing said which mode an account was in, and answering
// the question took a throwaway program.
func TestFormatNamesTheAccountMode(t *testing.T) {
	report := exampleReport()

	out := Format(report)

	if !strings.Contains(out, "combined mode") {
		t.Errorf("the mode is not named:\n%s", out)
	}

	if !strings.Contains(out, "opens the whole account") {
		t.Errorf("combined mode is named without saying what it means:\n%s", out)
	}

	report.Accounts[0].SplitMode = true

	out = Format(report)

	if !strings.Contains(out, "split mode") {
		t.Errorf("split mode is not named:\n%s", out)
	}

	if strings.Contains(out, "opens the whole account") {
		t.Errorf("split mode is described as combined:\n%s", out)
	}
}

// TestFormatSaysWhichSideThePortsAreOn is #29.
//
// The ports printed are the container's own. Anyone who published on different
// ones gets numbers that do not work in their mail client, with nothing saying
// why. It happens to everybody already running Proton's desktop bridge, which
// holds 1143 and 1025 on the host.
func TestFormatSaysWhichSideThePortsAreOn(t *testing.T) {
	report := exampleReport()

	out := Format(report)

	if !strings.Contains(out, "inside the container") {
		t.Errorf("the ports are not labelled as the container's own:\n%s", out)
	}

	if !strings.Contains(out, "BRIDGE_PUBLIC_IMAP_PORT") {
		t.Errorf("nothing says how to have the published ports named:\n%s", out)
	}

	report.PublicIMAPPort = 11143
	report.PublicSMTPPort = 11025

	out = Format(report)

	if !strings.Contains(out, "127.0.0.1:11143") || !strings.Contains(out, "127.0.0.1:11025") {
		t.Errorf("the declared host ports are missing:\n%s", out)
	}

	// Said, not measured. The container cannot check this, and a number it
	// cannot check must not be presented as one it did.
	if !strings.Contains(out, "not what the container measured") {
		t.Errorf("the declared ports are presented as fact:\n%s", out)
	}
}

func TestFormatNamesTheConnectionMode(t *testing.T) {
	report := exampleReport()

	// A mail client asks for one of these two by name, and picking the wrong
	// one is the single most common reason a local bridge refuses to connect.
	if out := Format(report); !strings.Contains(out, "STARTTLS") {
		t.Errorf("STARTTLS is not named:\n%s", out)
	}

	report.IMAPSSL = true
	report.SMTPSSL = true

	out := Format(report)

	if !strings.Contains(out, "SSL/TLS") {
		t.Errorf("SSL/TLS is not named:\n%s", out)
	}

	if strings.Contains(out, "STARTTLS") {
		t.Errorf("STARTTLS is still named although both ports use direct TLS:\n%s", out)
	}
}

// The state has to be visible either way, and the wrong one has to be
// recognisable as wrong. Printing "off" unconditionally would make the line
// decoration rather than information.
func TestFormatShowsTheUpdateSetting(t *testing.T) {
	report := exampleReport()

	out := Format(report)

	if !strings.Contains(out, "Bridge self-update     off") {
		t.Errorf("the setting is not shown:\n%s", out)
	}

	report.AutomaticUpdates = true

	out = Format(report)

	if !strings.Contains(out, "Bridge self-update     ON") {
		t.Errorf("an enabled updater is not shown:\n%s", out)
	}

	if !strings.Contains(out, "not how this container is meant to run") {
		t.Errorf("an enabled updater is shown without saying it is wrong:\n%s", out)
	}
}

// An unreachable mail port is a real situation: the bridge may still be
// starting. The rest of the report is worth showing, and the missing part has
// to say why rather than appear as an empty line.
func TestFormatExplainsAMissingFingerprint(t *testing.T) {
	report := exampleReport()
	report.Fingerprint = ""
	report.FingerprintErr = "connection refused"

	out := Format(report)

	if !strings.Contains(out, "connection refused") {
		t.Errorf("the reason is missing:\n%s", out)
	}
}

func TestFormatWithNoAccounts(t *testing.T) {
	report := exampleReport()
	report.Accounts = nil

	out := Format(report)

	if !strings.Contains(out, "No account is signed in") {
		t.Errorf("the empty case is not explained:\n%s", out)
	}

	// With no account there is no password to warn about, and a warning about
	// a secret that is not on screen only teaches people to skip warnings.
	if strings.Contains(out, "Bridge password") {
		t.Errorf("a password line appeared with no accounts:\n%s", out)
	}
}

// The password is the most sensitive thing this program prints. It must be
// labelled as what it is, or it gets pasted into a bug report as "the bridge
// password, which is not secret, right?".
func TestFormatWarnsAboutThePassword(t *testing.T) {
	report := exampleReport()
	report.Secrets = true

	out := Format(report)

	if !strings.Contains(out, "not your Proton password") {
		t.Errorf("the password is not distinguished from the Proton one:\n%s", out)
	}

	if !strings.Contains(out, "bug reports") {
		t.Errorf("there is no warning about sharing it:\n%s", out)
	}
}

// TestFormatShowsTheThreeSwitches is #23.
//
// All three are read from the bridge rather than echoed from the environment,
// and the report has to say so: they live in the vault and survive restarts, so
// removing a variable from the compose file does not undo what it set. Somebody
// wondering why All Mail is still there needs that sentence.
func TestFormatShowsTheThreeSwitches(t *testing.T) {
	report := exampleReport()

	out := Format(report)

	for _, want := range []string{
		"Alternative routing    off",
		"Show All Mail          off",
		"Usage diagnostics      off",
		"Read from the bridge",
		"survive restarts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from the output:\n%s", want, out)
		}
	}

	report.AlternativeRouting = true
	report.ShowAllMail = true

	out = Format(report)

	if !strings.Contains(out, "Alternative routing    ON") {
		t.Error("alternative routing is not shown as on")
	}

	if !strings.Contains(out, "Show All Mail          ON") {
		t.Error("All Mail is not shown as on")
	}
}

// Telemetry on is worth a sentence rather than a word. This container turns it
// off, so seeing it on means either somebody set the variable or it was turned
// on in Proton's own application against the same vault - and the reader should
// not have to work out which possibilities exist.
func TestFormatExplainsTelemetryBeingOn(t *testing.T) {
	report := exampleReport()

	if strings.Contains(Format(report), "Usage diagnostics are ON") {
		t.Fatal("the explanation appears with telemetry off, so it says nothing")
	}

	report.Telemetry = true

	out := Format(report)

	if !strings.Contains(out, "Usage diagnostics      ON") {
		t.Error("telemetry is not shown as on")
	}

	if !strings.Contains(out, "BRIDGE_TELEMETRY=true") {
		t.Error("nothing says how it might have been turned on")
	}
}
