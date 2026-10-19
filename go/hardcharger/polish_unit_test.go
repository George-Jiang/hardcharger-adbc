// Licensed under the Apache License, Version 2.0.

package hardcharger

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// --- shared browser sign-in -------------------------------------------------------------------

// fakeJWT is an unsigned JWT with the given claims (the driver reads exp and
// iat only; the server verifies tokens).
func fakeJWT(claims map[string]any) string {
	b64 := func(v any) string { data, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(data) }
	return b64(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + b64(claims) + ".c2ln"
}

func TestRenewTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		token string
		want  time.Time
	}{
		"an hour":            {fakeJWT(map[string]any{"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}), now.Add(time.Hour - time.Minute)},
		"short: a quarter":   {fakeJWT(map[string]any{"iat": now.Unix(), "exp": now.Add(8 * time.Second).Unix()}), now.Add(6 * time.Second)},
		"no iat: from now":   {fakeJWT(map[string]any{"exp": now.Add(8 * time.Second).Unix()}), now.Add(6 * time.Second)},
		"no exp: unknown":    {fakeJWT(map[string]any{"iat": now.Unix()}), time.Time{}},
		"not a JWT: unknown": {"opaque-token", time.Time{}},
		"bad payload":        {"a.%%%.c", time.Time{}},
	} {
		if got := renewTime(tc.token, now); !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestSharedSignInRunsOnceForConcurrentCallers(t *testing.T) {
	shared := &sharedSignIn{running: make(chan struct{}, 1)}
	var runs atomic.Int64
	token := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	signIn := func(context.Context) (string, error) {
		runs.Add(1)
		time.Sleep(50 * time.Millisecond) // the user, in the browser
		return token, nil
	}
	var wg sync.WaitGroup
	var freshCount atomic.Int64
	for range 16 {
		wg.Go(func() {
			got, fresh, err := shared.get(context.Background(), signIn)
			if err != nil || got != token {
				t.Errorf("get: %q, %v", got, err)
			}
			if fresh {
				freshCount.Add(1)
			}
		})
	}
	wg.Wait()
	if runs.Load() != 1 || freshCount.Load() != 1 {
		t.Fatalf("%d sign-ins (%d fresh), want 1", runs.Load(), freshCount.Load())
	}
}

func TestSharedSignInRunsAgainNearExpiryAndAfterForget(t *testing.T) {
	shared := &sharedSignIn{running: make(chan struct{}, 1)}
	runs := 0
	lifetime := 400 * time.Millisecond
	signIn := func(context.Context) (string, error) {
		runs++
		now := time.Now()
		return fakeJWT(map[string]any{"n": runs, "iat": float64(now.UnixMilli()) / 1000,
			"exp": float64(now.Add(lifetime).UnixMilli()) / 1000}), nil
	}
	first, _, _ := shared.get(context.Background(), signIn)
	if again, fresh, _ := shared.get(context.Background(), signIn); again != first || fresh {
		t.Fatal("a token far from its expiry was not reused")
	}
	time.Sleep(lifetime * 3 / 4) // past exp - lifetime/4
	second, fresh, _ := shared.get(context.Background(), signIn)
	if second == first || !fresh || runs != 2 {
		t.Fatalf("a token close to its expiry was handed out (runs %d)", runs)
	}
	shared.forget(first) // an old token: no effect
	if got, _, _ := shared.get(context.Background(), signIn); got != second {
		t.Fatal("forgetting an old token dropped the current one")
	}
	shared.forget(second)
	if got, fresh, _ := shared.get(context.Background(), signIn); got == second || !fresh || runs != 3 {
		t.Fatalf("a forgotten token was handed out (runs %d)", runs)
	}
}

func TestSharedSignInFailureIsNotShared(t *testing.T) {
	shared := &sharedSignIn{running: make(chan struct{}, 1)}
	runs := 0
	failing := func(context.Context) (string, error) { runs++; return "", errors.New("no") }
	for range 2 {
		if _, _, err := shared.get(context.Background(), failing); err == nil {
			t.Fatal("a failed sign-in succeeded")
		}
	}
	if runs != 2 {
		t.Fatalf("%d sign-ins, want each caller to try", runs)
	}
}

func TestSharedSignInWaiterGivesUp(t *testing.T) {
	shared := &sharedSignIn{running: make(chan struct{}, 1)}
	release := make(chan struct{})
	go func() {
		_, _, _ = shared.get(context.Background(), func(context.Context) (string, error) { <-release; return "t", nil })
	}()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := shared.get(ctx, func(context.Context) (string, error) { return "other", nil })
	wantStatus(t, err, adbc.StatusTimeout, "gave up waiting for the shared sign-in")
	close(release)
}

func TestSignInKeys(t *testing.T) {
	target := oauthTarget{baseURL: "https://h:31339"}
	base := signInKeyFor("warp://h:31337", target, nil, "")
	same := []signInKey{
		signInKeyFor("warp://H:31337/", target, map[string]string{"adbc.warp.retry.max_attempts": "2"}, ""),
	}
	different := map[string]signInKey{
		"another server":    signInKeyFor("warp://h:31338", target, nil, ""),
		"another host":      signInKeyFor("warp://other:31337", target, nil, ""),
		"plain HTTP":        signInKeyFor("warp://h:31337?tls=false", target, nil, ""),
		"another base path": signInKeyFor("warp://h:31337/x", target, nil, ""),
		"another sign-in":   signInKeyFor("warp://h:31337", oauthTarget{baseURL: "https://sso:1"}, nil, ""),
		"other roots":       signInKeyFor("warp://h:31337", target, map[string]string{"adbc.warp.tls.root_certs": "pem"}, ""),
		"skip verify":       signInKeyFor("warp://h:31337", target, map[string]string{"adbc.warp.tls.skip_verify": "true"}, ""),
		"server name":       signInKeyFor("warp://h:31337", target, map[string]string{"adbc.warp.tls.server_name": "x"}, ""),
		"another key":       signInKeyFor("warp://h:31337", target, nil, "alice"),
	}
	for _, key := range same {
		if key != base {
			t.Errorf("%+v != %+v", key, base)
		}
	}
	for name, key := range different {
		if key == base {
			t.Errorf("%s: same key", name)
		}
	}
}

func TestShareSignInOptions(t *testing.T) {
	ctx := context.Background()
	db, err := NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, map[string]string{
		"uri": "hardcharger://127.0.0.1:1", OptionAuthType: AuthTypeExternal,
		OptionOAuthShareSignIn: "true", OptionOAuthSignInKey: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	for key, want := range map[string]string{OptionOAuthShareSignIn: "true", OptionOAuthSignInKey: "alice"} {
		if got, err := db.(*database).GetOption(ctx, key); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", key, got, err, want)
		}
	}
	_, err = NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, map[string]string{
		"uri": "hardcharger://127.0.0.1:1", OptionOAuthShareSignIn: "sometimes"})
	wantStatus(t, err, adbc.StatusInvalidArgument, "expected true or false")
	// Sharing is for browser sign-ins: with a password it is refused, before anything is sent.
	db, err = NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, map[string]string{
		"uri": "hardcharger://127.0.0.1:1", "username": "u", "password": "p", OptionOAuthShareSignIn: "true"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	_, err = db.Open(ctx)
	wantStatus(t, err, adbc.StatusInvalidArgument, "needs adbc.hardcharger.auth_type=external")
}

// --- TLS to a plain-HTTP server -----------------------------------------------------------------

// plainHTTPListener answers anything with an HTTP 400 and records what it got.
type plainHTTPListener struct {
	listener net.Listener
	accepted atomic.Int64
	mu       sync.Mutex
	received bytes.Buffer
}

func newPlainHTTPListener(t *testing.T) *plainHTTPListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &plainHTTPListener{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.accepted.Add(1)
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 4096)
				for first := true; ; first = false {
					n, err := conn.Read(buf)
					p.mu.Lock()
					p.received.Write(buf[:n])
					p.mu.Unlock()
					if first {
						_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return p
}

func (p *plainHTTPListener) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

func (p *plainHTTPListener) bytes() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Clone(p.received.Bytes())
}

func TestTLSToAPlainHTTPServerSaysToTurnTLSOff(t *testing.T) {
	server := newPlainHTTPListener(t)
	ctx := context.Background()
	const password = "pl41n:p@ss-never-sent"
	db, err := NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, map[string]string{
		"uri": fmt.Sprintf("hardcharger://127.0.0.1:%d", server.port()), "username": "plain_user", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	_, err = db.Open(ctx)
	want := fmt.Sprintf("[warpsql] The server at 127.0.0.1:%d speaks plain HTTP, not TLS. "+
		"Add ?tls=false to the URI to connect without encryption.", server.port())
	var adbcErr adbc.Error
	if !errors.As(err, &adbcErr) || adbcErr.Code != adbc.StatusIO || adbcErr.Msg != want {
		t.Fatalf("error %v, want %q", err, want)
	}
	if got := server.accepted.Load(); got != 1 {
		t.Errorf("%d connections, want 1 (no retries, no plaintext fallback)", got)
	}
	received := server.bytes()
	if len(received) == 0 || received[0] != 0x16 {
		t.Errorf("the server got %q, want a TLS hello only", received)
	}
	for _, secret := range []string{password, "plain_user", "auth/token"} {
		if bytes.Contains(received, []byte(secret)) {
			t.Errorf("%q reached the plain-HTTP server", secret)
		}
	}
}

func TestBrowserSignInToAPlainHTTPSignInServer(t *testing.T) {
	signInServer := newPlainHTTPListener(t)
	opened := 0
	withBrowser(t, func(string, string) error { opened++; return nil })
	ctx := context.Background()
	for _, tc := range []struct {
		options map[string]string
		advice  string
	}{
		{map[string]string{OptionOAuthPort: strconv.Itoa(signInServer.port())}, "Add ?tls=false to the URI"},
		{map[string]string{OptionOAuthURL: fmt.Sprintf("https://127.0.0.1:%d", signInServer.port())},
			"Use an http:// URL in adbc.hardcharger.oauth.url"},
	} {
		options := map[string]string{"uri": "hardcharger://127.0.0.1:1", OptionAuthType: AuthTypeExternal}
		for key, value := range tc.options {
			options[key] = value
		}
		db, err := NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Open(ctx)
		_ = db.Close(ctx)
		wantStatus(t, err, adbc.StatusIO, fmt.Sprintf("browser sign-in: The server at 127.0.0.1:%d speaks plain HTTP, not TLS. %s to connect without encryption.",
			signInServer.port(), tc.advice))
	}
	if opened != 0 {
		t.Fatalf("the browser was opened %d times", opened)
	}
}
