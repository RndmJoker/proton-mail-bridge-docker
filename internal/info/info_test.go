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

	for _, want := range []string{
		"3.25.0",
		"127.0.0.1:1143",
		"127.0.0.1:1025",
		"AB:CD:EF",
		"someone@example.invalid",
		"someone-else@example.invalid",
		"not-a-real-password",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from the output:\n%s", want, out)
		}
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
	out := Format(exampleReport())

	if !strings.Contains(out, "not your Proton password") {
		t.Errorf("the password is not distinguished from the Proton one:\n%s", out)
	}

	if !strings.Contains(out, "bug reports") {
		t.Errorf("there is no warning about sharing it:\n%s", out)
	}
}
