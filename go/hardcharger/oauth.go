// Licensed under the Apache License, Version 2.0.

package hardcharger

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
)

// Browser sign-in through Hardcharger's sign-in server, as GizmoSQL's and the
// hardcharger command-line client's: GET /oauth/initiate answers
// {session_uuid, auth_url}; the user signs in to the identity provider at
// auth_url in a browser; GET /oauth/token/{uuid} answers "pending" until the
// provider has answered, then hands out the provider's ID token once. The
// driver then signs in to Warp SQL with that token (grant token_exchange).

// oauthPollInterval is how often the sign-in server is polled (tests shorten it).
var oauthPollInterval = time.Second

// openBrowser runs command (or the system's opener) with url. Tests replace it.
var openBrowser = func(command, url string) error {
	var args []string
	switch {
	case command != "":
		args = strings.Fields(command)
	case runtime.GOOS == "darwin":
		args = []string{"open"}
	case runtime.GOOS == "windows":
		args = []string{"rundll32", "url.dll,FileProtocolHandler"}
	default:
		args = []string{"xdg-open"}
	}
	cmd := exec.Command(args[0], append(args[1:], url)...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	// Don't wait for it: a browser started by its command often runs until it is closed.
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// oauthTarget is where and how to reach the sign-in server.
type oauthTarget struct {
	baseURL   string
	tlsConfig *tls.Config
	explicit  bool // baseURL is OptionOAuthURL, not derived from the uri
}

func oauthTargetFor(cfg *config, warpURI string, warpOptions map[string]string) (oauthTarget, error) {
	base := strings.TrimRight(cfg.oauthURL, "/")
	if base == "" {
		u, err := url.Parse(warpURI)
		if err != nil || u.Hostname() == "" {
			return oauthTarget{}, invalidArgument("browser sign-in needs the server's uri or %s", OptionOAuthURL)
		}
		scheme := "https"
		if !usesTLS(warpURI) {
			scheme = "http" // only when the connection is plain HTTP: no fallback
		}
		base = scheme + "://" + net.JoinHostPort(u.Hostname(), strconv.Itoa(cfg.oauthPort))
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if pem := warpOptions["adbc.warp.tls.root_certs"]; pem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return oauthTarget{}, invalidArgument("adbc.warp.tls.root_certs holds no PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	if strings.EqualFold(warpOptions["adbc.warp.tls.skip_verify"], "true") {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // asked for explicitly (development)
	}
	return oauthTarget{baseURL: base, tlsConfig: tlsConfig, explicit: cfg.oauthURL != ""}, nil
}

// speaksPlainHTTP reports a TLS handshake that the server answered with plain
// HTTP (only the TLS hello was sent).
func speaksPlainHTTP(err error) bool {
	if errors.Is(err, http.ErrSchemeMismatch) {
		return true
	}
	var record tls.RecordHeaderError
	return errors.As(err, &record) && string(record.RecordHeader[:]) == "HTTP/"
}

// plainHTTPSignInError is the error for a sign-in server reached over TLS that
// speaks plain HTTP, worded as the Warp SQL driver's for the server itself. The
// driver never falls back to plaintext by itself.
func plainHTTPSignInError(target oauthTarget) error {
	host := target.baseURL
	if u, err := url.Parse(target.baseURL); err == nil {
		port := u.Port()
		if port == "" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	advice := "Add ?tls=false to the URI"
	if target.explicit {
		advice = "Use an http:// URL in " + OptionOAuthURL
	}
	return oauthError(adbc.StatusIO, "The server at %s speaks plain HTTP, not TLS. %s to connect without encryption.",
		host, advice)
}

func oauthError(code adbc.Status, format string, args ...any) error {
	return adbc.Error{Code: code, Msg: "[hardcharger] browser sign-in: " + fmt.Sprintf(format, args...)}
}

// browserSignIn runs the browser sign-in and returns the identity provider's
// ID token.
func browserSignIn(ctx context.Context, target oauthTarget, browser string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: target.tlsConfig}}
	defer client.CloseIdleConnections()
	get := func(path string) (int, map[string]any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.baseURL+path, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		body := map[string]any{}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if len(data) > 0 && json.Unmarshal(data, &body) != nil {
			body = map[string]any{"error": string(data)}
		}
		return resp.StatusCode, body, nil
	}
	text := func(body map[string]any, key string) string {
		value, _ := body[key].(string)
		return value
	}

	status, flow, err := get("/oauth/initiate")
	if err != nil && speaksPlainHTTP(err) {
		return "", plainHTTPSignInError(target)
	}
	if err != nil {
		return "", oauthError(adbc.StatusIO, "could not reach Hardcharger's sign-in server at %s: %v", target.baseURL, err)
	}
	if status != http.StatusOK {
		return "", oauthError(adbc.StatusIO, "the sign-in server at %s answered HTTP %d to /oauth/initiate: %s",
			target.baseURL, status, text(flow, "error"))
	}
	uuid, authURL := text(flow, "session_uuid"), text(flow, "auth_url")
	if uuid == "" || authURL == "" {
		return "", oauthError(adbc.StatusIO, "unexpected answer from %s/oauth/initiate", target.baseURL)
	}

	if browser == "none" || openBrowser(browser, authURL) != nil {
		fmt.Fprintf(os.Stderr, "Open this URL in a browser to sign in to Hardcharger:\n%s\n", authURL)
	} else {
		fmt.Fprintln(os.Stderr, "Waiting for the browser sign-in...")
	}

	deadline := time.Now().Add(timeout)
	for {
		status, body, err := get("/oauth/token/" + url.PathEscape(uuid))
		switch {
		case err != nil && errors.Is(err, context.Canceled):
			return "", oauthError(adbc.StatusCancelled, "cancelled")
		case err != nil:
			return "", oauthError(adbc.StatusIO, "polling the sign-in server failed: %v", err)
		case status == http.StatusNotFound || text(body, "status") == "not_found":
			return "", oauthError(adbc.StatusUnauthenticated, "the sign-in expired or was already used")
		case status != http.StatusOK:
			return "", oauthError(adbc.StatusIO, "polling the sign-in server failed with HTTP %d", status)
		}
		switch state := text(body, "status"); state {
		case "complete":
			token := text(body, "token")
			if token == "" {
				return "", oauthError(adbc.StatusIO, "the sign-in completed without a token")
			}
			return token, nil
		case "error":
			message := text(body, "error")
			if message == "" {
				message = "unknown error"
			}
			return "", oauthError(adbc.StatusUnauthenticated, "the sign-in failed: %s", message)
		case "pending":
		default:
			return "", oauthError(adbc.StatusIO, "unexpected sign-in status %q", state)
		}
		if time.Now().After(deadline) {
			return "", oauthError(adbc.StatusTimeout, "timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return "", oauthError(adbc.StatusCancelled, "cancelled")
		case <-time.After(oauthPollInterval):
		}
	}
}
