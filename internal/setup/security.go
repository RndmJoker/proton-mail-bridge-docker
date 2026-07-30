package setup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const (
	csrfCookieName = "setup_csrf"
	csrfHeaderName = "X-CSRF-Token"

	// TokenHeaderName carries the access token when the page is exposed beyond
	// the container. A header rather than a query parameter: query strings end
	// up in proxy logs, in browser history and in referrer headers.
	TokenHeaderName = "X-Setup-Token" //nolint:gosec // a header name, not a credential
)

// NewToken returns a random token suitable for the access token or the CSRF
// token.
//
// 32 bytes from crypto/rand. This guards a page that accepts a Proton
// password, so there is no argument for anything shorter or cheaper.
func NewToken() (string, error) {
	raw := make([]byte, 32)

	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("could not generate a token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

var (
	errMissingToken = errors.New("missing or wrong access token")
	errBadOrigin    = errors.New("request came from another page")
	errBadHost      = errors.New("request was addressed to an unexpected host")
	errMissingCSRF  = errors.New("missing or wrong CSRF token")
)

// equal compares in constant time, so that a wrong token cannot be found one
// character at a time.
func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// checkToken enforces the access token when one is set.
//
// No token is configured when the page is bound inside the container only, and
// then anything that can reach it is already inside.
func (s *Server) checkToken(r *http.Request) error {
	if s.token == "" {
		return nil
	}

	if equal(r.Header.Get(TokenHeaderName), s.token) {
		return nil
	}

	return errMissingToken
}

// checkHost rejects a request addressed to a host this server does not answer
// for.
//
// This is what stops DNS rebinding: a page on the internet can point a name it
// controls at 127.0.0.1 and have a browser send requests here. The browser
// sends that name in the Host header, and it is not one of ours.
func (s *Server) checkHost(r *http.Request) error {
	host := r.Host
	if host == "" {
		return errBadHost
	}

	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		host = hostOnly
	}

	for _, allowed := range s.allowedHosts {
		if strings.EqualFold(host, allowed) {
			return nil
		}
	}

	return fmt.Errorf("%w: %q", errBadHost, host)
}

// checkOrigin rejects a request a browser made on behalf of another page.
//
// Absent is allowed: a browser omits Origin on same-origin GETs, and
// proton-login is not a browser and sends none. It is the presence of a
// mismatching one that is decisive. The CSRF token below is what covers the
// case where Origin is absent because someone left it out on purpose.
func (s *Server) checkOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("%w: unparseable origin", errBadOrigin)
	}

	host := parsed.Hostname()

	for _, allowed := range s.allowedHosts {
		if strings.EqualFold(host, allowed) {
			return nil
		}
	}

	return fmt.Errorf("%w: %q", errBadOrigin, origin)
}

// checkCSRF enforces the double-submit token on anything that changes state.
//
// The value is handed out in a cookie and has to come back in a header. A page
// on another origin can cause the cookie to be sent but cannot read it, so it
// cannot produce the matching header.
func (s *Server) checkCSRF(r *http.Request) error {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return errMissingCSRF
	}

	if !equal(cookie.Value, r.Header.Get(csrfHeaderName)) {
		return errMissingCSRF
	}

	// The cookie has to be the one this server issued. Without this a caller
	// could invent a matching pair and satisfy the check with values of its
	// own choosing.
	if !equal(cookie.Value, s.csrfToken) {
		return errMissingCSRF
	}

	return nil
}

// setCSRFCookie hands out the token a later write has to echo back.
func (s *Server) setCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    s.csrfToken,
		Path:     "/",
		HttpOnly: false, // the page has to read it to send the header
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// guard runs every check that applies to a state-changing request.
//
// Order matters only for the message that comes back; all four have to pass.
func (s *Server) guard(r *http.Request) error {
	if err := s.checkHost(r); err != nil {
		return err
	}

	if err := s.checkOrigin(r); err != nil {
		return err
	}

	if err := s.checkToken(r); err != nil {
		return err
	}

	return s.checkCSRF(r)
}
