package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
)

func account(name string, addresses ...string) *bridgepb.User {
	return &bridgepb.User{
		Id:        "id-" + name,
		Username:  name,
		State:     bridgepb.UserState_CONNECTED,
		Addresses: addresses,
	}
}

func TestParseArgs(t *testing.T) {
	split, wanted, err := parseArgs(nil)
	if err != nil || split != nil || wanted != "" {
		t.Errorf("no arguments: got %v, %q, %v; want a listing", split, wanted, err)
	}

	split, wanted, err = parseArgs([]string{"split"})
	if err != nil || split == nil || !*split || wanted != "" {
		t.Errorf("split: got %v, %q, %v", split, wanted, err)
	}

	split, wanted, err = parseArgs([]string{"combined", "someone@example.invalid"})
	if err != nil || split == nil || *split || wanted != "someone@example.invalid" {
		t.Errorf("combined with an account: got %v, %q, %v", split, wanted, err)
	}

	// A typo must not fall through to either mode. "splt" read as combined
	// would start a full download in the opposite direction.
	for _, bad := range [][]string{{"splt"}, {"on"}, {"split", "a", "b"}} {
		if _, _, err := parseArgs(bad); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
}

func TestPickAccountTakesTheOnlyOne(t *testing.T) {
	user, err := pickAccount([]*bridgepb.User{account("someone", "a@example.invalid")}, "")
	if err != nil {
		t.Fatalf("pickAccount: %v", err)
	}

	if user.GetUsername() != "someone" {
		t.Errorf("picked %q", user.GetUsername())
	}
}

// The address is what people have in front of them, not the username, and
// mail addresses are not case sensitive in practice.
func TestPickAccountFindsAnAddress(t *testing.T) {
	users := []*bridgepb.User{
		account("first", "a@example.invalid"),
		account("second", "b@example.invalid", "Test@Example.invalid"),
	}

	user, err := pickAccount(users, "test@example.invalid")
	if err != nil {
		t.Fatalf("pickAccount: %v", err)
	}

	if user.GetUsername() != "second" {
		t.Errorf("picked %q, want second", user.GetUsername())
	}
}

// With several accounts a guess would cost a full download of the wrong one.
func TestPickAccountRefusesToGuess(t *testing.T) {
	users := []*bridgepb.User{account("first"), account("second")}

	_, err := pickAccount(users, "")
	if err == nil {
		t.Fatal("picked an account out of two without being told which")
	}

	if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Errorf("the error does not name the choices: %v", err)
	}
}

func TestPickAccountNamesWhatItCouldNotFind(t *testing.T) {
	_, err := pickAccount([]*bridgepb.User{account("someone", "a@example.invalid")}, "other@example.invalid")
	if err == nil {
		t.Fatal("an unknown account was accepted")
	}

	if _, err := pickAccount(nil, ""); err == nil {
		t.Fatal("an empty vault was not reported")
	}
}

func TestFormatModes(t *testing.T) {
	split := account("someone", "a@example.invalid")
	split.SplitMode = true

	out := formatModes([]*bridgepb.User{account("other", "b@example.invalid"), split})

	if !strings.Contains(out, "other  (connected, combined mode)") {
		t.Errorf("combined mode is not named:\n%s", out)
	}

	if !strings.Contains(out, "someone  (connected, split mode)") {
		t.Errorf("split mode is not named:\n%s", out)
	}

	if !strings.Contains(formatModes(nil), "No account is signed in") {
		t.Error("an empty vault is not explained")
	}
}

// The sentence this command exists to say before anyone agrees. Split mode is
// easily read as one password per address, and it is not.
func TestExplainSaysThePasswordIsShared(t *testing.T) {
	out := explain(account("someone", "a@example.invalid", "b@example.invalid"), true)

	if !strings.Contains(out, "share one bridge password") {
		t.Errorf("split mode is explained without the shared password:\n%s", out)
	}

	if !strings.Contains(out, "fetches the") || !strings.Contains(out, "whole mailbox again") {
		t.Errorf("the download is not mentioned:\n%s", out)
	}

	if !strings.Contains(explain(account("someone"), false), "share one mailbox") {
		t.Error("combined mode is not explained")
	}
}

// No is the default. An Enter pressed out of habit must not start a download.
func TestConfirmDefaultsToNo(t *testing.T) {
	for answer, want := range map[string]bool{
		"\n":      false,
		"n\n":     false,
		"maybe\n": false,
		"y\n":     true,
		"YES\n":   true,
		" yes \n": true,
	} {
		got, err := confirm(bufio.NewReader(strings.NewReader(answer)), "")
		if err != nil {
			t.Fatalf("%q: %v", answer, err)
		}

		if got != want {
			t.Errorf("%q: got %v, want %v", answer, got, want)
		}
	}

	if _, err := confirm(bufio.NewReader(strings.NewReader("")), ""); err == nil {
		t.Error("no answer at all was taken as one")
	}
}
