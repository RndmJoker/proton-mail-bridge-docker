package login

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// stubClient records the login calls. It embeds the generated interface so
// that any other method panics rather than quietly doing nothing.
type stubClient struct {
	bridgepb.BridgeClient

	login          *bridgepb.LoginRequest
	twoFA          *bridgepb.LoginRequest
	twoPasswords   *bridgepb.LoginRequest
	aborted        *bridgepb.LoginAbortRequest
	loginCallCount int

	err error

	// The account the bridge reports once a sign-in finished. Read and
	// switched from the goroutine that settles the mode, so behind a lock.
	mu        sync.Mutex
	splitMode bool
	setCalls  int
	userErr   error

	// hold, when set, keeps GetUser waiting until it is closed, so a test can
	// look at the session while the mode is still being settled.
	hold chan struct{}
}

func (s *stubClient) GetUser(_ context.Context, in *wrapperspb.StringValue, _ ...grpc.CallOption) (*bridgepb.User, error) {
	if s.hold != nil {
		<-s.hold
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.userErr != nil {
		return nil, s.userErr
	}

	return &bridgepb.User{
		Id:        in.GetValue(),
		Username:  "someone",
		State:     bridgepb.UserState_CONNECTED,
		SplitMode: s.splitMode,
	}, nil
}

func (s *stubClient) SetUserSplitMode(_ context.Context, in *bridgepb.UserSplitModeRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.setCalls++
	s.splitMode = in.GetActive()

	return &emptypb.Empty{}, nil
}

func (s *stubClient) switches() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.setCalls
}

// settled waits until the session is done with the mode of a finished
// sign-in, and returns where it ended up.
func settled(t *testing.T, session *Session) Status {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if status := session.Status(); status.State != StateSettingMode {
			return status
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("the session never left setting_mode")

	return Status{}
}

func finishedEvent(wasSignedOut bool) *bridgepb.StreamEvent {
	return loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_Finished{Finished: &bridgepb.LoginFinishedEvent{UserID: "u", WasSignedOut: wasSignedOut}},
	})
}

func (s *stubClient) Login(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.login = in
	s.loginCallCount++

	return &emptypb.Empty{}, nil
}

func (s *stubClient) Login2FA(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.twoFA = in

	return &emptypb.Empty{}, nil
}

func (s *stubClient) Login2Passwords(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.twoPasswords = in

	return &emptypb.Empty{}, nil
}

func (s *stubClient) LoginAbort(_ context.Context, in *bridgepb.LoginAbortRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.aborted = in

	return &emptypb.Empty{}, nil
}

// Events as the bridge builds them, so the tests exercise the same shapes the
// stream delivers.
func loginEvent(inner *bridgepb.LoginEvent) *bridgepb.StreamEvent {
	return &bridgepb.StreamEvent{Event: &bridgepb.StreamEvent_Login{Login: inner}}
}

func errorEvent(kind bridgepb.LoginErrorType, message string) *bridgepb.StreamEvent {
	return loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_Error{
			Error: &bridgepb.LoginErrorEvent{Type: kind, Message: message},
		},
	})
}

// The interface takes `bytes password` and says nothing more. The service
// base64-decodes it, so anything else fails authentication with no hint as to
// why. This is the check that keeps that knowledge from getting lost.
func TestPasswordIsBase64Encoded(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	const password = "correct horse battery staple"

	if err := session.Start(context.Background(), "someone@example.invalid", []byte(password), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := client.login.GetPassword()

	if string(sent) == password {
		t.Fatal("the password went out unencoded, the bridge would reject it")
	}

	decoded, err := base64.StdEncoding.DecodeString(string(sent))
	if err != nil {
		t.Fatalf("what was sent is not base64: %v", err)
	}

	if string(decoded) != password {
		t.Fatalf("decoded to %q", decoded)
	}
}

func TestStartSetsUsernameAndState(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := session.Status().State; got != StateAwaitingCredentials {
		t.Fatalf("state is %q", got)
	}

	if client.login.GetUsername() != "someone@example.invalid" {
		t.Fatalf("username arrived as %q", client.login.GetUsername())
	}

	// Sending it unasked makes the bridge look for verification details it
	// does not have.
	if client.login.UseHvDetails != nil {
		t.Fatal("human verification details were claimed on a first attempt")
	}
}

func TestHappyPath(t *testing.T) {
	session := New(&stubClient{}, nil)

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	session.Handle(finishedEvent(false))

	if got := settled(t, session).State; got != StateSucceeded {
		t.Fatalf("state is %q", got)
	}
}

// An account that is already signed in is not a failure. It is the state the
// caller wanted.
func TestAlreadyLoggedInCountsAsSuccess(t *testing.T) {
	session := New(&stubClient{}, nil)
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_AlreadyLoggedIn{AlreadyLoggedIn: &bridgepb.LoginFinishedEvent{UserID: "u"}},
	}))

	if got := session.Status().State; got != StateSucceeded {
		t.Fatalf("state is %q", got)
	}
}

func TestTOTPFlow(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TfaRequested{
			TfaRequested: &bridgepb.LoginTfaRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if got := session.Status().State; got != StateAwaitingTOTP {
		t.Fatalf("state is %q", got)
	}

	if err := session.SubmitTOTP(context.Background(), []byte("123456")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(string(client.twoFA.GetPassword()))
	if err != nil {
		t.Fatalf("the code is not base64: %v", err)
	}

	if string(decoded) != "123456" {
		t.Fatalf("code arrived as %q", decoded)
	}

	session.Handle(finishedEvent(false))

	if got := settled(t, session).State; got != StateSucceeded {
		t.Fatalf("state is %q", got)
	}
}

// The answer reaches a new account, and what is reported is what the bridge
// says afterwards rather than what was asked for.
func TestANewAccountGetsTheAnswer(t *testing.T) {
	client := &stubClient{}

	var logged []string

	session := New(client, func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), true)
	session.Handle(finishedEvent(false))

	status := settled(t, session)

	if status.State != StateSucceeded {
		t.Fatalf("state is %q: %s", status.State, status.Message)
	}

	if status.SplitMode == nil || !*status.SplitMode {
		t.Errorf("split mode was asked for and the status reports %v", status.SplitMode)
	}

	if status.ModeKept {
		t.Error("a new account is reported as having kept its mode")
	}

	if client.switches() != 1 {
		t.Errorf("the bridge was asked to switch %d times, want once", client.switches())
	}

	if len(logged) != 1 || !strings.Contains(logged[0], "split mode, read back") {
		t.Errorf("the log does not record the mode: %q", logged)
	}
}

// Combined is the mode a new account already has, so answering no costs
// nothing and must not reach the bridge as a call.
func TestANewAccountThatStaysCombinedIsLeftAlone(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)
	session.Handle(finishedEvent(false))

	status := settled(t, session)

	if status.SplitMode == nil || *status.SplitMode {
		t.Errorf("the status reports %v, want combined", status.SplitMode)
	}

	if client.switches() != 0 {
		t.Error("the bridge was asked to switch an account that was already in combined mode")
	}
}

// The case that would hurt. An account that was signed out, by a password
// change or a revoked session, comes back with its mailbox already downloaded.
// Applying the answer to it would throw that away and fetch it all again.
func TestAReturningAccountKeepsItsMode(t *testing.T) {
	client := &stubClient{splitMode: true}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)
	session.Handle(finishedEvent(true))

	status := settled(t, session)

	if client.switches() != 0 {
		t.Fatal("a returning account was switched")
	}

	if !status.ModeKept {
		t.Error("the status does not say the mode was kept")
	}

	if status.SplitMode == nil || !*status.SplitMode {
		t.Errorf("the status reports %v, want the split mode the account had", status.SplitMode)
	}
}

// The sign-in worked. A mode that could not be confirmed is something left to
// do, not a failed sign-in, and calling it one would send people back to type
// their password into a page that is about to close.
func TestAModeThatCannotBeConfirmedIsStillASuccess(t *testing.T) {
	client := &stubClient{userErr: errors.New("unavailable")}

	var logged []string

	session := New(client, func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), true)
	session.Handle(finishedEvent(false))

	status := settled(t, session)

	if status.State != StateSucceeded {
		t.Fatalf("state is %q", status.State)
	}

	if status.SplitMode != nil {
		t.Error("a mode is reported that was never read")
	}

	if !strings.Contains(status.Message, "proton-mode") {
		t.Errorf("the message does not say what to do: %q", status.Message)
	}

	if len(logged) != 1 || !strings.HasPrefix(logged[0], "WARNING") {
		t.Errorf("the log does not warn: %q", logged)
	}
}

// While the mode is being settled the sign-in is not over, and a second one
// would be started on top of it.
func TestASecondSignInWaitsForTheMode(t *testing.T) {
	client := &stubClient{hold: make(chan struct{})}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), true)
	session.Handle(finishedEvent(false))

	if got := session.Status().State; got != StateSettingMode {
		t.Fatalf("state is %q, want setting_mode", got)
	}

	if err := session.Start(context.Background(), "other@example.invalid", []byte("pw"), false); !errors.Is(err, ErrNotExpected) {
		t.Errorf("a second sign-in was accepted while the first was settling: %v", err)
	}

	close(client.hold)

	if got := settled(t, session).State; got != StateSucceeded {
		t.Errorf("state is %q", got)
	}
}

// The bridge sends this when the account has both a security key and TOTP.
// A container has no security key, so the code is the only way through and
// this has to land in the same state as a plain TOTP request.
func TestTfaOrFidoAsksForACode(t *testing.T) {
	session := New(&stubClient{}, nil)
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TfaOrFidoRequested{
			TfaOrFidoRequested: &bridgepb.LoginTfaOrFidoRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if got := session.Status().State; got != StateAwaitingTOTP {
		t.Fatalf("state is %q", got)
	}
}

// A security key and nothing else cannot be satisfied from a container. It has
// to end the attempt with an explanation rather than wait for an answer nobody
// can give.
func TestFidoOnlyFailsWithAnExplanation(t *testing.T) {
	session := New(&stubClient{}, nil)
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_FidoRequested{
			FidoRequested: &bridgepb.LoginFidoRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	status := session.Status()

	if status.State != StateFailed {
		t.Fatalf("state is %q", status.State)
	}

	if status.Retryable {
		t.Fatal("marked retryable, but trying again cannot help")
	}

	if status.Message == "" {
		t.Fatal("no explanation")
	}
}

func TestTwoPasswordFlow(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TwoPasswordRequested{
			TwoPasswordRequested: &bridgepb.LoginTwoPasswordsRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if got := session.Status().State; got != StateAwaitingMailboxPassword {
		t.Fatalf("state is %q", got)
	}

	if err := session.SubmitMailboxPassword(context.Background(), []byte("mailbox")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.twoPasswords == nil {
		t.Fatal("the mailbox password was never sent")
	}
}

// Proton asks for a challenge to be solved in a browser. The retry has to tell
// the bridge to use the details it stored, or it starts from scratch and asks
// again.
func TestHumanVerification(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_HvRequested{
			HvRequested: &bridgepb.LoginHvRequestedEvent{HvUrl: "https://verify.example.invalid/challenge"},
		},
	}))

	status := session.Status()

	if status.State != StateAwaitingHumanVerification {
		t.Fatalf("state is %q", status.State)
	}

	if status.HumanVerificationURL != "https://verify.example.invalid/challenge" {
		t.Fatalf("url is %q", status.HumanVerificationURL)
	}

	// Starting again is allowed from this state, and only from here does the
	// retry claim the stored details.
	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.login.UseHvDetails == nil || !client.login.GetUseHvDetails() {
		t.Fatal("the retry did not ask the bridge to use the verification details")
	}
}

func TestErrorsAreExplained(t *testing.T) {
	tests := []struct {
		name          string
		kind          bridgepb.LoginErrorType
		message       string
		wantRetryable bool
	}{
		{name: "wrong password", kind: bridgepb.LoginErrorType_USERNAME_PASSWORD_ERROR, wantRetryable: true},
		{name: "free plan", kind: bridgepb.LoginErrorType_FREE_USER, wantRetryable: false},
		{name: "no connection", kind: bridgepb.LoginErrorType_CONNECTION_ERROR, wantRetryable: true},
		{name: "bad code", kind: bridgepb.LoginErrorType_TFA_ERROR, wantRetryable: true},
		{name: "bad mailbox password", kind: bridgepb.LoginErrorType_TWO_PASSWORDS_ERROR, wantRetryable: true},
		{name: "too many attempts", kind: bridgepb.LoginErrorType_TWO_PASSWORDS_ABORT, wantRetryable: true},
		{name: "verification failed", kind: bridgepb.LoginErrorType_HV_ERROR, wantRetryable: true},
		{name: "security key", kind: bridgepb.LoginErrorType_FIDO_ERROR, wantRetryable: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := New(&stubClient{}, nil)
			_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

			session.Handle(errorEvent(test.kind, test.message))

			status := session.Status()

			if status.State != StateFailed {
				t.Fatalf("state is %q", status.State)
			}

			if status.Message == "" {
				t.Fatal("no explanation")
			}

			if status.Retryable != test.wantRetryable {
				t.Fatalf("retryable is %v, want %v", status.Retryable, test.wantRetryable)
			}
		})
	}
}

// A free account is the one failure where trying again is pointless, and the
// message has to say what to do instead rather than repeat "failed".
func TestFreePlanIsExplainedNotJustRejected(t *testing.T) {
	session := New(&stubClient{}, nil)
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(errorEvent(bridgepb.LoginErrorType_FREE_USER, ""))

	status := session.Status()

	if status.Retryable {
		t.Fatal("marked retryable")
	}

	if status.Message == "the sign-in failed" {
		t.Fatal("fell through to the generic message")
	}
}

func TestStepsOutOfOrderAreRefused(t *testing.T) {
	session := New(&stubClient{}, nil)

	if err := session.SubmitTOTP(context.Background(), []byte("123456")); !errors.Is(err, ErrNotExpected) {
		t.Fatalf("expected ErrNotExpected, got %v", err)
	}

	if err := session.SubmitMailboxPassword(context.Background(), []byte("pw")); !errors.Is(err, ErrNotExpected) {
		t.Fatalf("expected ErrNotExpected, got %v", err)
	}
}

// The bridge keeps one set of authentication state. A second attempt started
// while the first is still waiting would overwrite it, and both would end in
// something inexplicable.
func TestASecondSignInIsRefusedWhileOneIsWaiting(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(loginEvent(&bridgepb.LoginEvent{
		Event: &bridgepb.LoginEvent_TfaRequested{
			TfaRequested: &bridgepb.LoginTfaRequestedEvent{Username: "someone@example.invalid"},
		},
	}))

	if err := session.Start(context.Background(), "other@example.invalid", []byte("pw"), false); !errors.Is(err, ErrNotExpected) {
		t.Fatalf("expected ErrNotExpected, got %v", err)
	}

	if client.loginCallCount != 1 {
		t.Fatalf("the bridge saw %d login calls", client.loginCallCount)
	}
}

// A failed attempt has to be retryable without anything being reset by hand,
// because that is what a wrong password looks like.
func TestStartAgainAfterAFailure(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("wrong"), false)
	session.Handle(errorEvent(bridgepb.LoginErrorType_USERNAME_PASSWORD_ERROR, ""))

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("right"), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.loginCallCount != 2 {
		t.Fatalf("the bridge saw %d login calls", client.loginCallCount)
	}
}

func TestAbort(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.aborted.GetUsername() != "someone@example.invalid" {
		t.Fatalf("aborted %q", client.aborted.GetUsername())
	}

	if got := session.Status().State; got != StateIdle {
		t.Fatalf("state is %q", got)
	}
}

// Nothing was started, so there is nothing for the bridge to abort. Sending an
// abort for an empty username would be a call about nobody.
func TestAbortWithoutASignIn(t *testing.T) {
	client := &stubClient{}
	session := New(client, nil)

	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.aborted != nil {
		t.Fatal("an abort was sent for nobody")
	}
}

// Events about anything else share the stream. Reacting to them would move the
// sign-in on something that has nothing to do with it.
func TestUnrelatedEventsAreIgnored(t *testing.T) {
	session := New(&stubClient{}, nil)
	_ = session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false)

	session.Handle(&bridgepb.StreamEvent{
		Event: &bridgepb.StreamEvent_Mail{
			Mail: &bridgepb.MailEvent{
				Event: &bridgepb.MailEvent_AddressChanged{
					AddressChanged: &bridgepb.AddressChangedEvent{Address: "someone@example.invalid"},
				},
			},
		},
	})

	if got := session.Status().State; got != StateAwaitingCredentials {
		t.Fatalf("an unrelated event moved the state to %q", got)
	}
}

func TestStartReportsATransportFailure(t *testing.T) {
	client := &stubClient{err: errors.New("unavailable")}
	session := New(client, nil)

	if err := session.Start(context.Background(), "someone@example.invalid", []byte("pw"), false); err == nil {
		t.Fatal("expected an error, got none")
	}

	if got := session.Status().State; got != StateFailed {
		t.Fatalf("state is %q", got)
	}
}
