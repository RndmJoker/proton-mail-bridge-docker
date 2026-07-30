// Package setup serves the page that signs an account in.
//
// It is the most sensitive part of this project: it accepts a Proton password.
// Everything here is arranged around that. It runs over TLS only, it binds
// inside the container unless told otherwise, it demands an access token when
// it is told otherwise, and it stops as soon as an account is signed in.
//
// It shows no mail. It is for signing in and for nothing else.
package setup

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
)

// Timeouts. Generous enough for a person typing a password from a password
// manager, short enough that a stuck connection does not hold a slot forever.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 2 * time.Minute
	writeTimeout      = 2 * time.Minute
	idleTimeout       = 5 * time.Minute
	shutdownTimeout   = 10 * time.Second
)

// Options configure the server.
type Options struct {
	// BindAddress is the address to listen on. Empty means 127.0.0.1, which
	// is reachable from inside the container only.
	BindAddress string

	Port int

	// Token is required on every request when set. It must be set whenever
	// BindAddress is anything but loopback; NewServer enforces that rather
	// than trusting the caller.
	Token string

	// CertDir is where the certificate lives, inside the volume.
	CertDir string

	// AllowedHosts are the Host header values this server answers for. The
	// bind address is always among them.
	AllowedHosts []string

	// Log receives progress. It must never be given a password, a token or a
	// CSRF value.
	Log func(format string, args ...any)
}

// Server is the setup page.
type Server struct {
	session *login.Session

	token        string
	csrfToken    string
	allowedHosts []string

	address     string
	fingerprint string

	http *http.Server
	log  func(format string, args ...any)

	// signedIn is closed once an account is signed in, which is the signal to
	// shut the whole thing down.
	signedIn chan struct{}
	once     sync.Once
}

// ErrTokenRequired is returned when the page would be reachable beyond the
// container without one.
var ErrTokenRequired = errors.New("an access token is required when the setup page is not bound to loopback")

// NewServer prepares the server. It does not listen yet.
func NewServer(session *login.Session, options Options) (*Server, error) {
	bind := options.BindAddress
	if bind == "" {
		bind = "127.0.0.1"
	}

	// The rule the whole exposure story rests on, enforced here rather than
	// left to whoever wires up the options. Reachable from outside without a
	// token would mean anyone who can route to the container can hand Proton
	// credentials to a page of their choosing.
	if !isLoopback(bind) && options.Token == "" {
		return nil, ErrTokenRequired
	}

	material, err := LoadOrCreateCertificate(options.CertDir, bind)
	if err != nil {
		return nil, err
	}

	csrf, err := NewToken()
	if err != nil {
		return nil, err
	}

	hosts := append([]string{bind, "localhost"}, options.AllowedHosts...)

	logf := options.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	server := &Server{
		session:      session,
		token:        options.Token,
		csrfToken:    csrf,
		allowedHosts: hosts,
		address:      net.JoinHostPort(bind, strconv.Itoa(options.Port)),
		fingerprint:  material.Fingerprint,
		log:          logf,
		signedIn:     make(chan struct{}),
	}

	server.http = &http.Server{
		Addr:              server.address,
		Handler:           server.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{material.Certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}

	return server, nil
}

// Fingerprint is the SHA-256 of the certificate being served.
func (s *Server) Fingerprint() string {
	return s.fingerprint
}

// Address is where the server listens.
func (s *Server) Address() string {
	return s.address
}

// SignedIn is closed once a sign-in succeeded.
func (s *Server) SignedIn() <-chan struct{} {
	return s.signedIn
}

// Serve listens until ctx is done or an account is signed in.
func (s *Server) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w", s.address, err)
	}

	errCh := make(chan error, 1)

	go func() {
		errCh <- s.http.ServeTLS(listener, "", "")
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return err

	case <-s.signedIn:
		s.log("An account is signed in, shutting the setup page down.")
		return s.shutdown()

	case <-ctx.Done():
		return s.shutdown()
	}
}

func (s *Server) shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return nil
}

// MarkSignedIn tells the server an account exists, which ends it.
//
// Called both from the sign-in itself and from whoever watches the bridge's
// account list, because an account can also appear without this page being
// involved.
func (s *Server) MarkSignedIn() {
	s.once.Do(func() { close(s.signedIn) })
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handlePage)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/totp", s.handleTOTP)
	mux.HandleFunc("POST /api/mailbox-password", s.handleMailboxPassword)
	mux.HandleFunc("POST /api/abort", s.handleAbort)

	return securityHeaders(mux)
}

// securityHeaders applies the same set to every response.
//
// The content security policy is as tight as the page allows: everything comes
// from this origin, nothing may frame it, and no request may leave it. A page
// that takes a password has no business loading anything from elsewhere.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'; connect-src 'self'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cache-Control", "no-store")

		next.ServeHTTP(w, r)
	})
}

// writeJSON sends a response and never a secret.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(body)
}

type errorResponse struct {
	Error string `json:"error"`
}

// fail answers a rejected request.
//
// The message is the check that failed, never the value that was wrong.
// Echoing back what was submitted is how a token ends up in a browser console
// and from there in a screenshot.
func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.checkHost(r); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	if err := s.checkToken(r); err != nil {
		s.fail(w, http.StatusUnauthorized, err)
		return
	}

	s.setCSRFCookie(w)

	writeJSON(w, http.StatusOK, s.session.Status())
}

// credentials is the shape every write takes. One field, always called secret
// on the wire, so that nothing in a log or a proxy trace is labelled
// "password".
type credentials struct {
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var body credentials

	// A password is not megabytes. The limit is what stops a request body from
	// being a way to use up the container's memory.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))

	if err := decoder.Decode(&body); err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("could not read the request"))
		return credentials{}, false
	}

	return body, true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.guard(r); err != nil {
		s.rejectWrite(w, err)
		return
	}

	body, ok := s.decode(w, r)
	if !ok {
		return
	}

	if body.Username == "" || body.Secret == "" {
		s.fail(w, http.StatusBadRequest, errors.New("username and password are both required"))
		return
	}

	secret := []byte(body.Secret)

	if err := s.session.Start(r.Context(), body.Username, secret); err != nil {
		s.fail(w, http.StatusConflict, err)
		return
	}

	clear(secret)

	writeJSON(w, http.StatusAccepted, s.session.Status())
}

func (s *Server) handleTOTP(w http.ResponseWriter, r *http.Request) {
	s.handleSecret(w, r, s.session.SubmitTOTP)
}

func (s *Server) handleMailboxPassword(w http.ResponseWriter, r *http.Request) {
	s.handleSecret(w, r, s.session.SubmitMailboxPassword)
}

func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request, submit func(context.Context, []byte) error) {
	if err := s.guard(r); err != nil {
		s.rejectWrite(w, err)
		return
	}

	body, ok := s.decode(w, r)
	if !ok {
		return
	}

	if body.Secret == "" {
		s.fail(w, http.StatusBadRequest, errors.New("nothing was submitted"))
		return
	}

	secret := []byte(body.Secret)

	if err := submit(r.Context(), secret); err != nil {
		s.fail(w, http.StatusConflict, err)
		return
	}

	clear(secret)

	writeJSON(w, http.StatusAccepted, s.session.Status())
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	if err := s.guard(r); err != nil {
		s.rejectWrite(w, err)
		return
	}

	if err := s.session.Abort(r.Context()); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, s.session.Status())
}

// rejectWrite maps a failed guard to a status code.
func (s *Server) rejectWrite(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMissingToken):
		s.fail(w, http.StatusUnauthorized, err)
	case errors.Is(err, errBadHost), errors.Is(err, errBadOrigin):
		s.fail(w, http.StatusForbidden, err)
	default:
		s.fail(w, http.StatusForbidden, err)
	}
}

// isLoopback says whether an address is reachable from inside the container
// only.
func isLoopback(address string) bool {
	if address == "localhost" {
		return true
	}

	ip := net.ParseIP(address)

	return ip != nil && ip.IsLoopback()
}
