package setup

import (
	"encoding/base64"
	"html/template"
	"net/http"
)

// pageTemplate is the whole interface. One file, no assets, nothing loaded
// from anywhere.
//
// The script is inline and carries a per-request nonce, so the content
// security policy can name exactly this one block instead of allowing inline
// script in general. Everything the script displays is written with
// textContent: the status messages and the verification link come from Proton
// by way of the bridge, and none of it is ours to trust as markup.
var pageTemplate = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Proton Mail Bridge - sign in</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, sans-serif; line-height: 1.5; margin: 0 auto; max-width: 34rem; padding: 2rem 1rem; }
  h1 { font-size: 1.3rem; }
  label { display: block; margin-top: 1rem; font-weight: 600; }
  input { width: 100%; padding: .5rem; margin-top: .25rem; font-size: 1rem; box-sizing: border-box; }
  button { margin-top: 1rem; padding: .5rem 1rem; font-size: 1rem; cursor: pointer; }
  .note { opacity: .8; font-size: .9rem; }
  .error { color: #b00020; font-weight: 600; }
  .hidden { display: none; }
  fieldset { border: 1px solid currentColor; border-radius: .3rem; margin-top: 1.5rem; }
</style>
</head>
<body>
<h1>Sign in to Proton Mail Bridge</h1>

<p class="note">
  This page runs inside the container and talks to nothing but the bridge.
  Your password is sent to Proton through the bridge and is never written to a
  log or stored anywhere here.
</p>

<p class="note">Certificate fingerprint (SHA-256): <code>{{ .Fingerprint }}</code></p>

{{ if .TokenRequired }}
<fieldset id="token-step">
  <legend>Access token</legend>
  <p class="note">This page is reachable beyond the container, so it asks for the token printed in the container log at startup.</p>
  <label for="token">Token</label>
  <input id="token" type="password" autocomplete="off">
  <button id="token-submit">Continue</button>
</fieldset>
{{ end }}

<div id="app" {{ if .TokenRequired }}class="hidden"{{ end }}>

  <fieldset id="credentials-step">
    <legend>Account</legend>
    <label for="username">Proton username or address</label>
    <input id="username" type="text" autocomplete="username">
    <label for="password">Password</label>
    <input id="password" type="password" autocomplete="current-password">
    <button id="credentials-submit">Sign in</button>
  </fieldset>

  <fieldset id="totp-step" class="hidden">
    <legend>Two-factor code</legend>
    <label for="totp">Six-digit code</label>
    <input id="totp" type="text" inputmode="numeric" autocomplete="one-time-code">
    <button id="totp-submit">Submit</button>
  </fieldset>

  <fieldset id="mailbox-step" class="hidden">
    <legend>Mailbox password</legend>
    <p class="note">This account uses a second password for the mailbox.</p>
    <label for="mailbox">Mailbox password</label>
    <input id="mailbox" type="password" autocomplete="off">
    <button id="mailbox-submit">Submit</button>
  </fieldset>

  <fieldset id="hv-step" class="hidden">
    <legend>Human verification</legend>
    <p class="note">Proton wants a challenge solved in a browser. Open this link, complete it, then sign in again.</p>
    <p><a id="hv-link" href="#" rel="noreferrer noopener" target="_blank">Open the challenge</a></p>
  </fieldset>

  <fieldset id="done-step" class="hidden">
    <legend>Signed in</legend>
    <p>The account is signed in. This page is shutting down; you can close it.</p>
  </fieldset>

  <p id="message" class="error"></p>
</div>

<script nonce="{{ .Nonce }}">
(function () {
  "use strict";

  var token = "";

  function csrf() {
    var match = document.cookie.match(/(?:^|;\s*){{ .CSRFCookie }}=([^;]*)/);
    return match ? decodeURIComponent(match[1]) : "";
  }

  function headers() {
    var h = { "Content-Type": "application/json", "{{ .CSRFHeader }}": csrf() };
    if (token) { h["{{ .TokenHeader }}"] = token; }
    return h;
  }

  function show(id) {
    ["credentials-step", "totp-step", "mailbox-step", "hv-step", "done-step"].forEach(function (step) {
      document.getElementById(step).classList.toggle("hidden", step !== id);
    });
  }

  function say(text) {
    document.getElementById("message").textContent = text || "";
  }

  function render(status) {
    say(status.message);

    switch (status.state) {
      case "awaiting_totp": show("totp-step"); break;
      case "awaiting_mailbox_password": show("mailbox-step"); break;
      case "awaiting_human_verification":
        var link = document.getElementById("hv-link");
        // Only https. The URL comes from Proton by way of the bridge, and an
        // href is the one place on this page where a foreign string would be
        // executable rather than merely displayed.
        link.href = /^https:\/\//.test(status.humanVerificationUrl || "") ? status.humanVerificationUrl : "#";
        link.textContent = status.humanVerificationUrl || "";
        show("hv-step");
        break;
      case "succeeded": show("done-step"); break;
      default: show("credentials-step");
    }
  }

  function poll() {
    fetch("/api/status", { headers: headers(), credentials: "same-origin" })
      .then(function (r) { return r.json(); })
      .then(function (status) {
        if (status.error) { say(status.error); return; }
        render(status);
        if (status.state !== "succeeded") { setTimeout(poll, 1000); }
      })
      .catch(function () { setTimeout(poll, 2000); });
  }

  function post(path, body) {
    return fetch(path, {
      method: "POST",
      headers: headers(),
      credentials: "same-origin",
      body: JSON.stringify(body)
    }).then(function (r) { return r.json(); }).then(function (status) {
      if (status.error) { say(status.error); return; }
      render(status);
    });
  }

  var tokenStep = document.getElementById("token-step");
  if (tokenStep) {
    document.getElementById("token-submit").addEventListener("click", function () {
      token = document.getElementById("token").value;
      document.getElementById("token").value = "";
      fetch("/api/status", { headers: headers(), credentials: "same-origin" })
        .then(function (r) {
          if (r.status === 401) { say("That token was not accepted."); return null; }
          tokenStep.classList.add("hidden");
          document.getElementById("app").classList.remove("hidden");
          return r.json();
        })
        .then(function (status) { if (status) { render(status); poll(); } });
    });
  } else {
    poll();
  }

  document.getElementById("credentials-submit").addEventListener("click", function () {
    var password = document.getElementById("password");
    post("/api/login", {
      username: document.getElementById("username").value,
      secret: password.value
    });
    password.value = "";
  });

  document.getElementById("totp-submit").addEventListener("click", function () {
    var code = document.getElementById("totp");
    post("/api/totp", { secret: code.value });
    code.value = "";
  });

  document.getElementById("mailbox-submit").addEventListener("click", function () {
    var mailbox = document.getElementById("mailbox");
    post("/api/mailbox-password", { secret: mailbox.value });
    mailbox.value = "";
  });
})();
</script>
</body>
</html>
`))

type pageData struct {
	Fingerprint   string
	TokenRequired bool
	Nonce         string
	CSRFCookie    string
	CSRFHeader    string
	TokenHeader   string
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if err := s.checkHost(r); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	// The page itself is not behind the token: it has to be reachable to ask
	// for one. It contains nothing worth protecting, and every call it makes
	// is checked.
	nonce, err := NewToken()
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	nonce = base64.RawStdEncoding.EncodeToString([]byte(nonce))

	s.setCSRFCookie(w)

	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'; connect-src 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := pageData{
		Fingerprint:   s.fingerprint,
		TokenRequired: s.token != "",
		Nonce:         nonce,
		CSRFCookie:    csrfCookieName,
		CSRFHeader:    csrfHeaderName,
		TokenHeader:   TokenHeaderName,
	}

	if err := pageTemplate.Execute(w, data); err != nil {
		s.log("WARNING: could not render the setup page: %v", err)
	}
}
