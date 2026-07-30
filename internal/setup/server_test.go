package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/bridgepb"
	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// stubBridge accepts every login call and records nothing. What is under test
// here is the way in, not what happens after it.
type stubBridge struct {
	bridgepb.BridgeClient

	lastPassword []byte
}

func (s *stubBridge) Login(_ context.Context, in *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	s.lastPassword = in.GetPassword()
	return &emptypb.Empty{}, nil
}

func (s *stubBridge) Login2FA(_ context.Context, _ *bridgepb.LoginRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *stubBridge) LoginAbort(_ context.Context, _ *bridgepb.LoginAbortRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func newTestServer(t *testing.T, token string, bind string) (*Server, *stubBridge) {
	t.Helper()

	bridge := &stubBridge{}

	server, err := NewServer(login.New(bridge), Options{
		BindAddress: bind,
		Port:        8443,
		Token:       token,
		CertDir:     filepath.Join(t.TempDir(), "setup-tls"),
	})
	if err != nil {
		t.Fatalf("could not build the server: %v", err)
	}

	return server, bridge
}

// request builds a call that passes every check, so that each test can break
// exactly one thing and see it rejected.
func (s *Server) testRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer

	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}

	r := httptest.NewRequest(method, path, &buf)
	r.Host = "127.0.0.1:8443"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(csrfHeaderName, s.csrfToken)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: s.csrfToken})

	if s.token != "" {
		r.Header.Set(TokenHeaderName, s.token)
	}

	return r
}

// The rule the whole exposure story rests on. Without it, anyone who can route
// to the container reaches a page that takes Proton credentials.
func TestExposedWithoutATokenIsRefused(t *testing.T) {
	_, err := NewServer(login.New(&stubBridge{}), Options{
		BindAddress: "0.0.0.0",
		Port:        8443,
		CertDir:     filepath.Join(t.TempDir(), "setup-tls"),
	})

	if err == nil {
		t.Fatal("a server bound beyond loopback was built without a token")
	}
}

func TestLoopbackWithoutATokenIsFine(t *testing.T) {
	for _, bind := range []string{"", "127.0.0.1", "localhost"} {
		if _, err := NewServer(login.New(&stubBridge{}), Options{
			BindAddress: bind,
			Port:        8443,
			CertDir:     filepath.Join(t.TempDir(), "setup-tls"),
		}); err != nil {
			t.Errorf("bind %q was refused: %v", bind, err)
		}
	}
}

func TestStatusNeedsTheToken(t *testing.T) {
	server, _ := newTestServer(t, "the-token", "0.0.0.0")

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.Host = "0.0.0.0:8443"

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status without a token got %d, want 401", w.Code)
	}

	// A wrong one must be no better than none.
	r.Header.Set(TokenHeaderName, "not-the-token")

	w = httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status with a wrong token got %d, want 401", w.Code)
	}
}

func TestLoginNeedsEverything(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(*http.Request)
		want   int
	}{
		{
			name:   "nothing broken",
			break_: func(*http.Request) {},
			want:   http.StatusAccepted,
		},
		{
			// Without the header, a form on another page could cause the
			// browser to send the cookie and the request would go through.
			name:   "no CSRF header",
			break_: func(r *http.Request) { r.Header.Del(csrfHeaderName) },
			want:   http.StatusForbidden,
		},
		{
			name: "CSRF header that does not match the cookie",
			break_: func(r *http.Request) {
				r.Header.Set(csrfHeaderName, "something-else")
			},
			want: http.StatusForbidden,
		},
		{
			// A caller that invents its own matching pair would otherwise
			// satisfy a double-submit check.
			name: "a self-invented but consistent pair",
			break_: func(r *http.Request) {
				r.Header.Set(csrfHeaderName, "invented")
				r.Header.Del("Cookie")
				r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "invented"})
			},
			want: http.StatusForbidden,
		},
		{
			name:   "an origin from somewhere else",
			break_: func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.invalid") },
			want:   http.StatusForbidden,
		},
		{
			// DNS rebinding: a name someone else controls, pointed at
			// 127.0.0.1, so a browser sends requests here on their behalf.
			name:   "a host we do not answer for",
			break_: func(r *http.Request) { r.Host = "rebound.example.invalid" },
			want:   http.StatusForbidden,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestServer(t, "", "127.0.0.1")

			r := server.testRequest(http.MethodPost, "/api/login", credentials{
				Username: "someone@example.invalid",
				Secret:   "pw",
			})

			test.break_(r)

			w := httptest.NewRecorder()
			server.routes().ServeHTTP(w, r)

			if w.Code != test.want {
				t.Fatalf("got %d, want %d (body: %s)", w.Code, test.want, w.Body.String())
			}
		})
	}
}

// The same origin is normal and must not be rejected, or the page cannot talk
// to its own server.
func TestOwnOriginIsAccepted(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	r := server.testRequest(http.MethodPost, "/api/login", credentials{Username: "u", Secret: "p"})
	r.Header.Set("Origin", "https://127.0.0.1:8443")

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202 (body: %s)", w.Code, w.Body.String())
	}
}

func TestLoginRequiresBothFields(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	for _, body := range []credentials{
		{Username: "someone@example.invalid"},
		{Secret: "pw"},
		{},
	} {
		r := server.testRequest(http.MethodPost, "/api/login", body)

		w := httptest.NewRecorder()
		server.routes().ServeHTTP(w, r)

		if w.Code != http.StatusBadRequest {
			t.Errorf("body %+v got %d, want 400", body, w.Code)
		}
	}
}

// Nothing that was submitted may come back out. A response that echoes a
// password puts it in the browser console, and from there in a screenshot
// attached to a bug report.
func TestNoSecretIsEchoedBack(t *testing.T) {
	server, _ := newTestServer(t, "the-token", "0.0.0.0")

	const password = "correct horse battery staple"

	r := server.testRequest(http.MethodPost, "/api/login", credentials{
		Username: "someone@example.invalid",
		Secret:   password,
	})
	r.Host = "0.0.0.0:8443"

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	body := w.Body.String()

	for _, secret := range []string{password, "the-token", server.csrfToken} {
		if strings.Contains(body, secret) {
			t.Errorf("the response carries a secret: %s", body)
		}
	}
}

// A rejected request must not confirm what was wrong with the value, only that
// the check failed.
func TestRejectionDoesNotEchoTheAttempt(t *testing.T) {
	server, _ := newTestServer(t, "the-token", "0.0.0.0")

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.Host = "0.0.0.0:8443"
	r.Header.Set(TokenHeaderName, "guessed-token")

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	if strings.Contains(w.Body.String(), "guessed-token") {
		t.Fatalf("the rejected value came back: %s", w.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.Host = "127.0.0.1:8443"

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s is %q, want %q", header, got, want)
		}
	}

	// The page takes a password. Nothing about it should be embeddable in
	// another page, and nothing it loads should come from anywhere else.
	csp := w.Header().Get("Content-Security-Policy")

	for _, directive := range []string{"frame-ancestors 'none'", "default-src 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("%q is missing from the policy: %s", directive, csp)
		}
	}
}

func TestStatusHandsOutTheCSRFCookie(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.Host = "127.0.0.1:8443"

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	var found *http.Cookie

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			found = cookie
		}
	}

	if found == nil {
		t.Fatal("no CSRF cookie was set, the page could never make a write")
	}

	if !found.Secure {
		t.Error("the cookie is not marked Secure although the page is TLS only")
	}

	if found.SameSite != http.SameSiteStrictMode {
		t.Error("the cookie is not SameSite=Strict")
	}
}

func TestPageRenders(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "127.0.0.1:8443"

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}

	body := w.Body.String()

	// The fingerprint is the only way a person can tell this page apart from
	// one that merely looks like it.
	if !strings.Contains(body, server.Fingerprint()) {
		t.Error("the page does not show the certificate fingerprint")
	}

	// The inline script is allowed by name rather than by allowing inline
	// script in general.
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'unsafe-inline'") {
		t.Error("the page allows inline script in general")
	}

	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
		t.Error("the script is not bound to a nonce")
	}
}

// The page has to be reachable without the token, because it is where the
// token gets entered. It carries nothing worth protecting.
func TestPageIsReachableWithoutTheToken(t *testing.T) {
	server, _ := newTestServer(t, "the-token", "0.0.0.0")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "0.0.0.0:8443"

	w := httptest.NewRecorder()
	server.routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	if strings.Contains(w.Body.String(), "the-token") {
		t.Fatal("the page contains the token it is supposed to ask for")
	}
}

func TestMarkSignedInIsIdempotent(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	server.MarkSignedIn()
	server.MarkSignedIn()

	select {
	case <-server.SignedIn():
	default:
		t.Fatal("the signal never fired")
	}
}

func TestServeStopsWhenSignedIn(t *testing.T) {
	server, _ := newTestServer(t, "", "127.0.0.1")

	// Port 0 lets the operating system choose, so the test does not fight over
	// a fixed one.
	server.address = "127.0.0.1:0"
	server.http.Addr = server.address

	done := make(chan error, 1)

	go func() { done <- server.Serve(context.Background()) }()

	server.MarkSignedIn()

	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
