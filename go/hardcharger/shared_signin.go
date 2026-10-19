// Licensed under the Apache License, Version 2.0.

package hardcharger

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
)

// A shared browser sign-in: with OptionOAuthShareSignIn, the databases of one
// process that sign in through the browser to the same server, through the same
// sign-in server, with the same TLS trust and the same OptionOAuthSignInKey,
// share one sign-in. The first to open a connection runs it (the others wait
// for it); the rest reuse its ID token, each exchanging it for an access token
// of its own, until the token is close to its expiry ("exp"), when the next
// database to open runs a new sign-in. A shared token that the server refuses
// is forgotten, and that database signs in again (once). Tokens never cross
// servers or sign-in keys, and live only in this process's memory.

// sharedSignInMargin is how long before its expiry a shared ID token stops
// being handed out (at most; a quarter of a shorter lifetime), so that the
// exchange that follows still finds it valid.
const sharedSignInMargin = time.Minute

// signInKey says which databases may share a sign-in.
type signInKey struct {
	server string // the Warp SQL server: scheme, host, port and base path
	oauth  string // the sign-in server's base URL
	trust  string // the TLS trust (root certificates, skip_verify, server name)
	label  string // OptionOAuthSignInKey
}

// sharedSignIn is one key's sign-in.
type sharedSignIn struct {
	// running is held (a 1-buffered channel, so a waiter can give up) by the
	// database whose sign-in is running.
	running chan struct{}

	mu      sync.Mutex
	token   string
	renewAt time.Time // when to stop handing the token out; zero when unknown
}

var sharedSignIns = struct {
	mu      sync.Mutex
	entries map[signInKey]*sharedSignIn
}{entries: map[signInKey]*sharedSignIn{}}

// ForgetSharedSignIns drops every shared browser sign-in of this process
// (OptionOAuthShareSignIn), for example when the user signs out: the next
// database to open a connection signs in again. Open connections keep their
// sessions and access tokens.
func ForgetSharedSignIns() {
	sharedSignIns.mu.Lock()
	defer sharedSignIns.mu.Unlock()
	sharedSignIns.entries = map[signInKey]*sharedSignIn{}
}

func sharedSignInFor(key signInKey) *sharedSignIn {
	sharedSignIns.mu.Lock()
	defer sharedSignIns.mu.Unlock()
	entry := sharedSignIns.entries[key]
	if entry == nil {
		entry = &sharedSignIn{running: make(chan struct{}, 1)}
		sharedSignIns.entries[key] = entry
	}
	return entry
}

// get returns the key's ID token, running signIn when there is none that is
// still good. fresh reports that this call signed in. Concurrent callers wait
// for one sign-in, or until ctx ends.
func (s *sharedSignIn) get(ctx context.Context, signIn func(context.Context) (string, error)) (token string, fresh bool, err error) {
	select {
	case s.running <- struct{}{}:
	case <-ctx.Done():
		status := adbc.StatusCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = adbc.StatusTimeout
		}
		return "", false, oauthError(status, "gave up waiting for the shared sign-in: %v", ctx.Err())
	}
	defer func() { <-s.running }()
	if token := s.current(time.Now()); token != "" {
		return token, false, nil
	}
	token, err = signIn(ctx)
	if err != nil {
		return "", false, err
	}
	s.mu.Lock()
	s.token, s.renewAt = token, renewTime(token, time.Now())
	s.mu.Unlock()
	return token, true, nil
}

// current is the token when it is still good at now, else "".
func (s *sharedSignIn) current(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" || (!s.renewAt.IsZero() && !now.Before(s.renewAt)) {
		return ""
	}
	return s.token
}

// forget drops token, unless another sign-in already replaced it.
func (s *sharedSignIn) forget(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token, s.renewAt = "", time.Time{}
	}
}

// renewTime is when a token signed in at now stops being shared: its expiry
// ("exp") less a margin. It is zero when the token is not a JWT with an
// expiry; such a token is shared until the server refuses it.
func renewTime(token string, now time.Time) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp float64 `json:"exp"`
		Iat float64 `json:"iat"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	expires := time.Unix(0, int64(claims.Exp*float64(time.Second)))
	issued := now
	if claims.Iat > 0 {
		issued = time.Unix(0, int64(claims.Iat*float64(time.Second)))
	}
	margin := sharedSignInMargin
	if lifetime := expires.Sub(issued); lifetime > 0 {
		margin = min(margin, lifetime/4)
	}
	return expires.Add(-margin)
}

// signInKeyFor is the key of a database's browser sign-in.
func signInKeyFor(warpURI string, target oauthTarget, warpOptions map[string]string, label string) signInKey {
	server := warpURI
	if u, err := url.Parse(warpURI); err == nil {
		tls := "tls"
		if !usesTLS(warpURI) {
			tls = "plain"
		}
		server = tls + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(u.Path, "/")
	}
	trust := sha256.New()
	for _, option := range []string{"adbc.warp.tls.root_certs", "adbc.warp.tls.skip_verify", "adbc.warp.tls.server_name"} {
		trust.Write([]byte(option + "=" + warpOptions[option] + "\x00"))
	}
	return signInKey{server: server, oauth: target.baseURL, trust: hex.EncodeToString(trust.Sum(nil)), label: label}
}
