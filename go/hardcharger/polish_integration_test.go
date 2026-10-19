// Licensed under the Apache License, Version 2.0.

//go:build integration

package hardcharger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"go.uber.org/goleak"
)

// --- TLS pointed at a plain-HTTP server ---------------------------------------------------------

// byteRecorder forwards a loopback port to a server and records what clients send.
type byteRecorder struct {
	listener net.Listener
	mu       sync.Mutex
	sent     bytes.Buffer
	conns    []net.Conn
	wg       sync.WaitGroup
}

func newByteRecorder(t *testing.T, targetPort int) *byteRecorder {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &byteRecorder{listener: listener}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(targetPort))
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, server)
			p.mu.Unlock()
			p.wg.Add(2)
			go p.pipe(server, client, true)
			go p.pipe(client, server, false)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		p.mu.Lock()
		for _, conn := range p.conns {
			_ = conn.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *byteRecorder) pipe(dst, src net.Conn, record bool) {
	defer p.wg.Done()
	defer dst.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if record {
				p.mu.Lock()
				p.sent.Write(buf[:n])
				p.mu.Unlock()
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *byteRecorder) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

func (p *byteRecorder) received() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Clone(p.sent.Bytes())
}

func TestIntegrationTLSToAPlainHTTPServerSaysToTurnTLSOff(t *testing.T) {
	proxy := newByteRecorder(t, shared.port)
	uri := fmt.Sprintf("hardcharger://127.0.0.1:%d", proxy.port()) // TLS: the default
	_, _, err := open(t, shared, map[string]string{adbc.OptionKeyURI: uri})
	want := fmt.Sprintf("[warpsql] The server at 127.0.0.1:%d speaks plain HTTP, not TLS. "+
		"Add ?tls=false to the URI to connect without encryption.", proxy.port())
	var adbcErr adbc.Error
	if !errors.As(err, &adbcErr) || adbcErr.Code != adbc.StatusIO || adbcErr.Msg != want {
		t.Fatalf("error %v, want %q", err, want)
	}
	sent := proxy.received()
	if len(sent) == 0 || sent[0] != 0x16 {
		t.Fatalf("the server got %q, want a TLS hello only", sent)
	}
	for _, secret := range []string{shared.password, testUsername, "auth/token"} {
		if bytes.Contains(sent, []byte(secret)) {
			t.Errorf("%q reached the server", secret)
		}
	}
}

// The twin: with ?tls=false the same proxy carries the sign-in, password and all.
func TestIntegrationPlainHTTPThroughTheRecordingProxyTwin(t *testing.T) {
	proxy := newByteRecorder(t, shared.port)
	cnxn := mustOpen(t, shared, map[string]string{
		adbc.OptionKeyURI: fmt.Sprintf("hardcharger://127.0.0.1:%d?tls=false", proxy.port())})
	if got := scalar(t, cnxn, "SELECT 42"); got != "42" {
		t.Fatal(got)
	}
	if !bytes.Contains(proxy.received(), []byte(shared.password)) {
		t.Fatal("the recording proxy did not see the password of a plain-HTTP sign-in")
	}
}

// --- one browser sign-in for many connections ---------------------------------------------------

// countBrowser is browserThatSignsIn, counting the sign-ins.
func countBrowser(t *testing.T, server *testServer, code string) *atomic.Int64 {
	browserThatSignsIn(t, server, code)
	signIn := openBrowser
	count := &atomic.Int64{}
	openBrowser = func(command, url string) error {
		count.Add(1)
		return signIn(command, url)
	}
	return count
}

// oauthOptions are the options of a browser sign-in to server, shared when key is not nil.
func oauthOptions(server *testServer, key *string) map[string]string {
	options := map[string]string{adbc.OptionKeyUsername: "<unset>", adbc.OptionKeyPassword: "<unset>",
		OptionAuthType: AuthTypeExternal, OptionOAuthPort: strconv.Itoa(server.oauthPort)}
	if key != nil {
		options[OptionOAuthShareSignIn] = "true"
		options[OptionOAuthSignInKey] = *key
	}
	return options
}

func ptr(s string) *string { return &s }

func TestIntegrationSharedSignInSignsInOnce(t *testing.T) {
	ForgetSharedSignIns()
	idp := newMockIdP(t)
	server := startOAuthServer(t, idp)
	signIns := countBrowser(t, server, "code-ok")
	// One after another, as dbt opens its connections: each a database of its own.
	for i := range 5 {
		cnxn := mustOpen(t, server, oauthOptions(server, ptr("")))
		if got := scalar(t, cnxn, "SELECT HARDCHARGER_USER()"); got != idp.email {
			t.Fatalf("connection %d signed in as %s", i, got)
		}
	}
	if got := signIns.Load(); got != 1 {
		t.Fatalf("5 connections signed in %d times, want 1", got)
	}
	// All at once, under another key: the first runs the sign-in, the others wait for it.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, cnxn, err := open(t, server, oauthOptions(server, ptr("concurrent")))
			if err != nil {
				t.Errorf("open: %v", err)
				return
			}
			_ = cnxn
		})
	}
	wg.Wait()
	if got := signIns.Load(); got != 2 {
		t.Fatalf("8 concurrent connections under a new key signed in %d more times, want 1", got-1)
	}
}

// The twin: without sharing, every database signs in on its own.
func TestIntegrationUnsharedSignInTwin(t *testing.T) {
	ForgetSharedSignIns()
	idp := newMockIdP(t)
	server := startOAuthServer(t, idp)
	signIns := countBrowser(t, server, "code-ok")
	for range 3 {
		mustOpen(t, server, oauthOptions(server, nil))
	}
	if got := signIns.Load(); got != 3 {
		t.Fatalf("3 unshared connections signed in %d times, want 3", got)
	}
}

func TestIntegrationSharedSignInIsPerServerAndKey(t *testing.T) {
	ForgetSharedSignIns()
	idp := newMockIdP(t)
	first, second := startOAuthServer(t, idp), startOAuthServer(t, idp)
	signIns := countBrowser(t, first, "code-ok")
	mustOpen(t, first, oauthOptions(first, ptr("")))
	mustOpen(t, first, oauthOptions(first, ptr("")))
	if got := signIns.Load(); got != 1 {
		t.Fatalf("signed in %d times, want 1", got)
	}
	mustOpen(t, first, oauthOptions(first, ptr("alice")))
	mustOpen(t, first, oauthOptions(first, ptr("alice")))
	if got := signIns.Load(); got != 2 {
		t.Fatalf("another key: signed in %d times in all, want 2", got)
	}
	// Another server never gets the first one's token: it signs in through its own sign-in server.
	signIns = countBrowser(t, second, "code-ok")
	mustOpen(t, second, oauthOptions(second, ptr("")))
	mustOpen(t, second, oauthOptions(second, ptr("")))
	if got := signIns.Load(); got != 1 {
		t.Fatalf("another server: signed in %d times, want 1", got)
	}
}

func TestIntegrationSharedSignInRenewsBeforeTheTokenExpires(t *testing.T) {
	ForgetSharedSignIns()
	idp := newMockIdP(t)
	idp.lifetime.Store(8) // shared until 2 s (a quarter) before it expires
	server := startOAuthServer(t, idp)
	signIns := countBrowser(t, server, "code-ok")
	start := time.Now()
	db, early, err := open(t, server, oauthOptions(server, ptr("")))
	if err != nil {
		t.Fatal(err)
	}
	mustOpen(t, server, oauthOptions(server, ptr("")))
	if got := signIns.Load(); got != 1 {
		t.Fatalf("signed in %d times, want 1", got)
	}
	entry := sharedEntryOf(t, db)
	if entry.current(time.Now()) == "" {
		t.Fatal("a fresh token is not shared")
	}
	time.Sleep(time.Until(start.Add(6500 * time.Millisecond)))
	// No longer handed out, before any server could refuse it.
	if entry.current(time.Now()) != "" {
		t.Fatal("a token 1.5 s from its expiry is still shared")
	}
	late := mustOpen(t, server, oauthOptions(server, ptr("")))
	if got := signIns.Load(); got != 2 {
		t.Fatalf("a token about to expire was shared (%d sign-ins, want 2)", got)
	}
	// Both connections still work: each exchanged its ID token for its own access token.
	for _, cnxn := range []adbc.ConnectionWithContext{early, late} {
		if got := scalar(t, cnxn, "SELECT HARDCHARGER_USER()"); got != idp.email {
			t.Fatalf("signed in as %s", got)
		}
	}
}

// sharedEntryOf is the shared sign-in a database uses.
func sharedEntryOf(t *testing.T, db adbc.DatabaseWithContext) *sharedSignIn {
	t.Helper()
	d := db.(*database)
	target, err := oauthTargetFor(d.cfg, d.warpURI, d.warpOptions)
	if err != nil {
		t.Fatal(err)
	}
	return sharedSignInFor(signInKeyFor(d.warpURI, target, d.warpOptions, d.cfg.signInKey))
}

// poisonSharedSignIn replaces the shared sign-in of the databases that options
// open on server with a token the server refuses (well formed, unexpired, unsigned).
func poisonSharedSignIn(t *testing.T, server *testServer, options map[string]string) {
	t.Helper()
	db, _, err := open(t, server, options)
	if err != nil {
		t.Fatal(err)
	}
	entry := sharedEntryOf(t, db)
	entry.mu.Lock()
	entry.token = fakeJWT(map[string]any{"iss": "nobody", "exp": time.Now().Add(time.Hour).Unix()})
	entry.renewAt = time.Now().Add(time.Hour)
	entry.mu.Unlock()
}

func TestIntegrationARefusedSharedTokenIsReplaced(t *testing.T) {
	ForgetSharedSignIns()
	idp := newMockIdP(t)
	server := startOAuthServer(t, idp)
	signIns := countBrowser(t, server, "code-ok")
	poisonSharedSignIn(t, server, oauthOptions(server, ptr("")))
	if got := signIns.Load(); got != 1 {
		t.Fatalf("signed in %d times, want 1", got)
	}
	cnxn := mustOpen(t, server, oauthOptions(server, ptr("")))
	if got := scalar(t, cnxn, "SELECT HARDCHARGER_USER()"); got != idp.email {
		t.Fatalf("signed in as %s", got)
	}
	mustOpen(t, server, oauthOptions(server, ptr("")))
	if got := signIns.Load(); got != 2 {
		t.Fatalf("signed in %d times, want 2 (once more for the refused token, then shared)", got)
	}
}

// The negative twin: when the sign-in after a refused token fails too, Open
// fails with that sign-in's error, after one more sign-in (no loop).
func TestIntegrationARefusedSharedTokenThenAFailedSignIn(t *testing.T) {
	ForgetSharedSignIns()
	idp := newMockIdP(t)
	server := startOAuthServer(t, idp)
	countBrowser(t, server, "code-ok")
	poisonSharedSignIn(t, server, oauthOptions(server, ptr("")))
	signIns := countBrowser(t, server, "code-bad")
	_, _, err := open(t, server, oauthOptions(server, ptr("")))
	wantStatus(t, err, adbc.StatusUnauthenticated, "the sign-in failed")
	if got := signIns.Load(); got != 1 {
		t.Fatalf("signed in %d times after the refusal, want 1", got)
	}
}

// --- closing after the server stopped -----------------------------------------------------------

func TestIntegrationCloseAfterTheServerStopped(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	server := startServer(t)
	ctx := context.Background()
	integrationTests.Add(1)
	alloc := memory.NewCheckedAllocator(memory.DefaultAllocator)
	db, err := NewDriver(alloc).NewDatabaseWithContext(ctx, map[string]string{adbc.OptionKeyURI: server.uri(),
		adbc.OptionKeyUsername: testUsername, adbc.OptionKeyPassword: server.password,
		OptionQueryTag: `{"test": "close"}`})
	if err != nil {
		t.Fatal(err)
	}
	var cnxns []adbc.ConnectionWithContext
	var stmts []adbc.StatementWithContext
	for range 3 {
		cnxn, err := db.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cnxns = append(cnxns, cnxn)
		prepared, _ := cnxn.NewStatement(ctx)
		if err := prepared.SetSqlQuery(ctx, "SELECT 1 + 1"); err != nil {
			t.Fatal(err)
		}
		if err := prepared.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		queried, _ := cnxn.NewStatement(ctx)
		if err := queried.SetSqlQuery(ctx, "SELECT * FROM range(100000)"); err != nil {
			t.Fatal(err)
		}
		reader, _, err := queried.ExecuteQuery(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reader.Next()
		reader.Release()
		stmts = append(stmts, prepared, queried)
	}
	server.stop()
	start := time.Now()
	for i, stmt := range stmts {
		if err := stmt.Close(ctx); err != nil {
			t.Errorf("statement %d: Close: %v", i, err)
		}
	}
	for i, cnxn := range cnxns {
		if err := cnxn.Close(ctx); err != nil {
			t.Errorf("connection %d: Close: %v", i, err)
		}
	}
	if err := db.Close(ctx); err != nil {
		t.Errorf("database: Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("closing took %s against a stopped server", elapsed)
	}
	alloc.AssertSize(t, 0)
	goleak.VerifyNone(t, ignore)
}
