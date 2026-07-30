package setup

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/RndmJoker/proton-mail-bridge-docker/internal/login"
)

// clientTimeout bounds a call to the setup server. Everything happens over
// loopback inside one container, so anything slower means something is wrong
// rather than slow. Generous enough for the bridge to talk to Proton first.
const clientTimeout = 2 * time.Minute

// Client talks to the setup server from inside the container.
//
// It exists so that proton-login and the browser use the same server, the same
// checks and the same state machine. A second way in would be a second thing
// to secure, and the second one is the one nobody looks at.
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	csrf string
}

// NewClient builds a client that trusts exactly the certificate in certDir.
//
// Pinning rather than skipping verification: the certificate is self-signed,
// so there is no authority to check it against, but it is right there in the
// volume. Accepting anything would mean accepting whatever else answers on
// that port.
func NewClient(baseURL, certDir, token string) (*Client, error) {
	certPEM, err := os.ReadFile(filepath.Join(certDir, certFileName)) //nolint:gosec // path is ours
	if err != nil {
		return nil, fmt.Errorf("could not read the setup certificate: %w\nIs the bridge running in this container?", err)
	}

	pool := x509.NewCertPool()

	if !pool.AppendCertsFromPEM(certPEM) {
		return nil, errors.New("the stored setup certificate could not be parsed")
	}

	return &Client{
		baseURL: baseURL,
		token:   token,
		http: &http.Client{
			Timeout: clientTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					ServerName: "127.0.0.1",
					MinVersion: tls.VersionTLS12,
				},
			},
		},
	}, nil
}

// Status fetches the current state and picks up the CSRF token on the way.
func (c *Client) Status() (login.Status, error) {
	return c.call(http.MethodGet, "/api/status", nil)
}

// Login starts a sign-in.
func (c *Client) Login(username string, password []byte) (login.Status, error) {
	return c.call(http.MethodPost, "/api/login", &credentials{
		Username: username,
		Secret:   string(password),
	})
}

// TOTP answers a request for a two-factor code.
func (c *Client) TOTP(code []byte) (login.Status, error) {
	return c.call(http.MethodPost, "/api/totp", &credentials{Secret: string(code)})
}

// MailboxPassword answers a request for the second password.
func (c *Client) MailboxPassword(password []byte) (login.Status, error) {
	return c.call(http.MethodPost, "/api/mailbox-password", &credentials{Secret: string(password)})
}

// Abort gives up on the sign-in.
func (c *Client) Abort() (login.Status, error) {
	return c.call(http.MethodPost, "/api/abort", nil)
}

func (c *Client) call(method, path string, body *credentials) (login.Status, error) {
	var buf bytes.Buffer

	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return login.Status{}, err
		}
	}

	request, err := http.NewRequest(method, c.baseURL+path, &buf) //nolint:noctx // the client carries a timeout
	if err != nil {
		return login.Status{}, err
	}

	request.Header.Set("Content-Type", "application/json")

	if c.token != "" {
		request.Header.Set(TokenHeaderName, c.token)
	}

	// A write needs the token the server handed out. Fetching the status is
	// how it arrives, so a write that comes first picks it up here.
	if method != http.MethodGet && c.csrf == "" {
		if _, err := c.Status(); err != nil {
			return login.Status{}, err
		}
	}

	if c.csrf != "" {
		request.Header.Set(csrfHeaderName, c.csrf)
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: c.csrf})
	}

	response, err := c.http.Do(request)
	if err != nil {
		return login.Status{}, fmt.Errorf("could not reach the setup server at %s: %w", c.baseURL, err)
	}

	defer func() { _ = response.Body.Close() }()

	for _, cookie := range response.Cookies() {
		if cookie.Name == csrfCookieName {
			c.csrf = cookie.Value
		}
	}

	// A page's worth of JSON at most. The limit is what keeps a misdirected
	// connection from being read into memory without end.
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return login.Status{}, err
	}

	if response.StatusCode >= http.StatusBadRequest {
		var failure errorResponse

		if err := json.Unmarshal(payload, &failure); err == nil && failure.Error != "" {
			return login.Status{}, errors.New(failure.Error)
		}

		return login.Status{}, fmt.Errorf("the setup server answered %s", response.Status)
	}

	var status login.Status

	if err := json.Unmarshal(payload, &status); err != nil {
		return login.Status{}, fmt.Errorf("could not read the answer: %w", err)
	}

	return status, nil
}
