// Licensed under the Apache License, Version 2.0.

package hardcharger

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestToWarpURI(t *testing.T) {
	for raw, want := range map[string]string{
		"hardcharger://db.example.com":            "warp://db.example.com:31337",
		"hardcharger://db.example.com:443":        "warp://db.example.com:443",
		"hardcharger://127.0.0.1?tls=false":       "warp://127.0.0.1:31337?tls=false",
		"hardcharger://127.0.0.1:4000?tls=false":  "warp://127.0.0.1:4000?tls=false",
		"hardcharger://h/custom/base":             "warp://h:31337/custom/base",
		"HARDCHARGER://h":                         "warp://h:31337",
		"hardcharger://[::1]":                     "warp://[::1]:31337",
		"hardcharger://[::1]:5000?tls=false":      "warp://[::1]:5000?tls=false",
		"warp://h:1?tls=false":                    "warp://h:1?tls=false",
		"https://h/warp":                          "https://h/warp",
		"http://h:8080":                           "http://h:8080",
		"grpc://h:1":                              "grpc://h:1", // the Warp SQL driver refuses it
		"hardcharger://joe@h:1":                   "warp://joe@h:1",
		"hardcharger://h:1?tls=false&extra=thing": "warp://h:1?tls=false&extra=thing",
	} {
		got, err := ToWarpURI(raw)
		if err != nil || got != want {
			t.Errorf("ToWarpURI(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func wantStatus(t *testing.T, err error, status adbc.Status, text string) {
	t.Helper()
	var adbcErr adbc.Error
	if !errors.As(err, &adbcErr) {
		t.Fatalf("error %v (%T), want an adbc.Error", err, err)
	}
	if adbcErr.Code != status || !strings.Contains(adbcErr.Msg, text) {
		t.Fatalf("error %v (status %v), want status %v and %q", adbcErr.Msg, adbcErr.Code, status, text)
	}
}

func TestPlusHTTPSchemeIsRefusedWithTheSpellingToUse(t *testing.T) {
	_, err := ToWarpURI("hardcharger+http://127.0.0.1:31337")
	wantStatus(t, err, adbc.StatusInvalidArgument, "hardcharger://host:port?tls=false for plain HTTP")
	// The twin: the plain-HTTP spelling.
	got, err := ToWarpURI("hardcharger://127.0.0.1:31337?tls=false")
	if err != nil || got != "warp://127.0.0.1:31337?tls=false" || usesTLS(got) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestBadURIs(t *testing.T) {
	_, err := ToWarpURI("")
	wantStatus(t, err, adbc.StatusInvalidArgument, "a uri is required")
	_, err = ToWarpURI("hardcharger:///base")
	wantStatus(t, err, adbc.StatusInvalidArgument, "no host")
	_, err = ToWarpURI("hardcharger://joe:secret@h:bad")
	wantStatus(t, err, adbc.StatusInvalidArgument, "invalid uri")
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("the password leaked: %v", err)
	}
}

func TestUsesTLS(t *testing.T) {
	for uri, want := range map[string]bool{
		"warp://h:1": true, "warp://h:1?tls=false": false, "warp://h:1?tls=0": false,
		"warp://h:1?tls=true": true, "https://h": true, "http://h": false,
	} {
		if got := usesTLS(uri); got != want {
			t.Errorf("usesTLS(%q) = %v", uri, got)
		}
	}
}

func TestSettingNames(t *testing.T) {
	for _, name := range []string{"query_timeout", "hardcharger.query_timeout", "x_1"} {
		if _, err := settingName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"query_timeout; DROP TABLE t", "Query_Timeout", "", "a.b", "1abc", "a'b"} {
		_, err := settingName(name)
		wantStatus(t, err, adbc.StatusInvalidArgument, "invalid Hardcharger setting name")
	}
}

func TestSetStatement(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		global      bool
		want        string
	}{
		{"query_timeout", "30", false, "SET SESSION hardcharger.query_timeout = '30'"},
		{"query_tag", `{"a":"it's"}`, false, `SET SESSION hardcharger.query_tag = '{"a":"it''s"}'`},
		{"graceful_shutdown", "false", true, "SET GLOBAL hardcharger.graceful_shutdown = 'false'"},
		{"query_timeout", "", false, "RESET SESSION hardcharger.query_timeout"},
		{"max_concurrent_statements", "", true, "RESET GLOBAL hardcharger.max_concurrent_statements"},
	} {
		if got := setStatement(tc.name, tc.value, tc.global); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
}

func newDB(t *testing.T, opts map[string]string) (*database, error) {
	t.Helper()
	db, err := NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return db.(*database), nil
}

func TestDatabaseOptions(t *testing.T) {
	ctx := context.Background()
	db, err := newDB(t, map[string]string{
		adbc.OptionKeyURI:                     "hardcharger://h?tls=false",
		OptionAuthType:                        AuthTypeExternal,
		OptionOAuthPort:                       "4000",
		OptionOAuthTimeoutSeconds:             "2.5",
		OptionQueryTag:                        `{"job":"x"}`,
		OptionSettingPrefix + "query_timeout": "30",
		OptionSettingPrefix + "hardcharger.bypass_queue": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		adbc.OptionKeyURI:                     "hardcharger://h?tls=false",
		OptionAuthType:                        AuthTypeExternal,
		OptionOAuthPort:                       "4000",
		OptionOAuthTimeoutSeconds:             "2.5",
		OptionQueryTag:                        `{"job":"x"}`,
		OptionSettingPrefix + "query_timeout": "30",
		OptionSettingPrefix + "bypass_queue":  "true",
	} {
		if got, err := db.GetOption(ctx, key); err != nil || got != want {
			t.Errorf("GetOption(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
	// The Warp SQL driver got the translated URI and none of Hardcharger's options.
	if db.warpURI != "warp://h:31337?tls=false" {
		t.Errorf("warp uri %q", db.warpURI)
	}
	_, err = db.GetOption(ctx, OptionSessionTag)
	wantStatus(t, err, adbc.StatusNotFound, "is not set")
}

func TestBadDatabaseOptionsAreRefused(t *testing.T) {
	for opts, text := range map[[2]string]string{
		{OptionAuthType, "oauth"}:                              "expected password, external or client_certificate (mtls)",
		{OptionOAuthPort, "0"}:                                 "expected a port number",
		{OptionOAuthTimeoutSeconds, "soon"}:                    "expected a number of seconds",
		{OptionSettingPrefix + "bad name", "1"}:                "invalid Hardcharger setting name",
		{OptionGlobalSettingPrefix + "graceful_shutdown", "1"}: "is a connection option",
		{"adbc.hardcharger.nope", "1"}:                         "unknown option",
		{adbc.OptionKeyURI, "hardcharger+https://h"}:           "is not a Hardcharger URI scheme",
	} {
		_, err := newDB(t, map[string]string{adbc.OptionKeyURI: "hardcharger://h", opts[0]: opts[1]})
		if err == nil || !strings.Contains(err.Error(), text) {
			t.Errorf("%s=%s: error %v, want %q", opts[0], opts[1], err, text)
		}
	}
	// The twin: the same keys with good values.
	if _, err := newDB(t, map[string]string{adbc.OptionKeyURI: "hardcharger://h", OptionAuthType: AuthTypePassword,
		OptionOAuthPort: "31339", OptionSettingPrefix + "query_timeout": "1"}); err != nil {
		t.Fatal(err)
	}
}

func TestSetOptionAfterCreation(t *testing.T) {
	ctx := context.Background()
	db, err := newDB(t, map[string]string{adbc.OptionKeyURI: "hardcharger://h2"})
	if err != nil {
		t.Fatal(err)
	}
	if db.warpURI != "warp://h2:31337" {
		t.Fatalf("warp uri %q", db.warpURI)
	}
	if got, _ := db.GetOption(ctx, adbc.OptionKeyURI); got != "hardcharger://h2" {
		t.Fatalf("uri %q", got)
	}
	if err := db.SetOption(ctx, OptionQueryTag, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetOption(ctx, OptionQueryTag); got != `{"a":1}` {
		t.Fatalf("query tag %q", got)
	}
	wantStatus(t, db.SetOption(ctx, OptionAuthType, "nope"), adbc.StatusInvalidArgument, "expected password, external or client_certificate (mtls)")
	// The Warp SQL driver's options reach it after creation too (driverbase's
	// SetOptions refused them as unknown), and it validates them.
	if err := db.SetOption(ctx, "adbc.warp.retry.max_attempts", "3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetOption(ctx, "adbc.warp.retry.max_attempts"); got != "3" {
		t.Fatalf("max attempts %q", got)
	}
	if err := db.SetOptions(ctx, map[string]string{OptionIngestParts: "2", "adbc.warp.tls.skip_verify": "true"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetOption(ctx, "adbc.warp.tls.skip_verify"); got != "true" {
		t.Fatalf("skip verify %q", got)
	}
	wantStatus(t, db.SetOption(ctx, "adbc.warp.retry.max_attempts", "zero"), adbc.StatusInvalidArgument, "adbc.warp.retry.max_attempts")
}

// fakeSignIn answers /oauth/initiate and then the polls with the queued answers.
type fakeSignIn struct {
	mu       sync.Mutex
	polls    []fakeAnswer
	initiate *fakeAnswer
	requests []string
	server   *httptest.Server
}

type fakeAnswer struct {
	status int
	body   map[string]any
}

func newFakeSignIn(t *testing.T, polls ...fakeAnswer) *fakeSignIn {
	f := &fakeSignIn{polls: polls}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.URL.Path)
		answer := fakeAnswer{http.StatusOK, map[string]any{"status": "pending"}}
		switch {
		case r.URL.Path == "/oauth/initiate" && f.initiate != nil:
			answer = *f.initiate
		case r.URL.Path == "/oauth/initiate":
			answer = fakeAnswer{http.StatusOK, map[string]any{"session_uuid": "u-1", "auth_url": "http://idp/authorize?state=s"}}
		case len(f.polls) > 0:
			answer, f.polls = f.polls[0], f.polls[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		_ = json.NewEncoder(w).Encode(answer.body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func withBrowser(t *testing.T, fn func(command, url string) error) {
	t.Helper()
	saved, savedInterval := openBrowser, oauthPollInterval
	openBrowser, oauthPollInterval = fn, 10*time.Millisecond
	t.Cleanup(func() { openBrowser, oauthPollInterval = saved, savedInterval })
}

func TestBrowserSignInCompletesAfterPending(t *testing.T) {
	var opened []string
	withBrowser(t, func(command, url string) error { opened = append(opened, command+" "+url); return nil })
	fake := newFakeSignIn(t, fakeAnswer{200, map[string]any{"status": "pending"}},
		fakeAnswer{200, map[string]any{"status": "complete", "token": "id.token.jwt"}})
	token, err := browserSignIn(context.Background(), oauthTarget{baseURL: fake.server.URL}, "my-browser --new", time.Minute)
	if err != nil || token != "id.token.jwt" {
		t.Fatalf("token %q, %v", token, err)
	}
	if len(opened) != 1 || opened[0] != "my-browser --new http://idp/authorize?state=s" {
		t.Fatalf("opened %q", opened)
	}
	if strings.Join(fake.requests, " ") != "/oauth/initiate /oauth/token/u-1 /oauth/token/u-1" {
		t.Fatalf("requests %q", fake.requests)
	}
}

func TestBrowserSignInFailures(t *testing.T) {
	withBrowser(t, func(string, string) error { return nil })
	for _, tc := range []struct {
		name     string
		polls    []fakeAnswer
		initiate *fakeAnswer
		timeout  time.Duration
		status   adbc.Status
		text     string
	}{
		{"provider error", []fakeAnswer{{200, map[string]any{"status": "error", "error": "invalid_grant: bad code"}}},
			nil, time.Minute, adbc.StatusUnauthenticated, "the sign-in failed: invalid_grant: bad code"},
		{"expired", []fakeAnswer{{404, map[string]any{"status": "not_found"}}},
			nil, time.Minute, adbc.StatusUnauthenticated, "expired or was already used"},
		{"timeout", nil, nil, 50 * time.Millisecond, adbc.StatusTimeout, "timed out after 50ms"},
		{"not configured", nil, &fakeAnswer{404, map[string]any{"error": "not_found"}},
			time.Minute, adbc.StatusIO, "answered HTTP 404 to /oauth/initiate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeSignIn(t, tc.polls...)
			fake.initiate = tc.initiate
			_, err := browserSignIn(context.Background(), oauthTarget{baseURL: fake.server.URL}, "", tc.timeout)
			wantStatus(t, err, tc.status, tc.text)
		})
	}
	_, err := browserSignIn(context.Background(), oauthTarget{baseURL: "http://127.0.0.1:1"}, "", time.Minute)
	wantStatus(t, err, adbc.StatusIO, "could not reach Hardcharger's sign-in server")
}

func TestBrowserNoneOnlyPrintsTheURL(t *testing.T) {
	called := false
	withBrowser(t, func(string, string) error { called = true; return nil })
	fake := newFakeSignIn(t, fakeAnswer{200, map[string]any{"status": "complete", "token": "t"}})
	if _, err := browserSignIn(context.Background(), oauthTarget{baseURL: fake.server.URL}, "none", time.Minute); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("the browser was opened")
	}
}

func TestOAuthTargetFollowsTheConnection(t *testing.T) {
	cfg := newConfig()
	for warpURI, want := range map[string]string{
		"warp://db.example.com:31337":      "https://db.example.com:31339",
		"warp://127.0.0.1:31337?tls=false": "http://127.0.0.1:31339",
		"http://127.0.0.1:8080":            "http://127.0.0.1:31339",
		"warp://[::1]:31337?tls=false":     "http://[::1]:31339",
	} {
		target, err := oauthTargetFor(cfg, warpURI, nil)
		if err != nil || target.baseURL != want {
			t.Errorf("%s: %q, %v; want %q", warpURI, target.baseURL, err, want)
		}
	}
	cfg.oauthURL = "https://sso.example.com/"
	if target, _ := oauthTargetFor(cfg, "warp://h:1", nil); target.baseURL != "https://sso.example.com" {
		t.Errorf("explicit url: %q", target.baseURL)
	}
	_, err := oauthTargetFor(newConfig(), "warp://h:1", map[string]string{"adbc.warp.tls.root_certs": "not pem"})
	wantStatus(t, err, adbc.StatusInvalidArgument, "holds no PEM certificate")
}

func TestIngestPartsIsTheWarpOption(t *testing.T) {
	ctx := context.Background()
	db, err := newDB(t, map[string]string{adbc.OptionKeyURI: "hardcharger://h", OptionIngestParts: "2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{OptionIngestParts, warpIngestParts} {
		if got, err := db.GetOption(ctx, key); err != nil || got != "2" {
			t.Errorf("GetOption(%s) = %q, %v", key, got, err)
		}
	}
	// The Warp SQL driver validates it, under either name; the twin is above.
	_, err = newDB(t, map[string]string{adbc.OptionKeyURI: "hardcharger://h", OptionIngestParts: "none"})
	wantStatus(t, err, adbc.StatusInvalidArgument, warpIngestParts)
	// Unset, it is the Warp SQL driver's default: 8.
	db, err = newDB(t, map[string]string{adbc.OptionKeyURI: "hardcharger://h"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetOption(ctx, OptionIngestParts); err != nil || got != "8" {
		t.Errorf("default ingest parts %q, %v", got, err)
	}
	if got, err := db.GetOption(ctx, "adbc.warp.statement.result_partitions"); err != nil || got != "8" {
		t.Errorf("default result partitions %q, %v", got, err)
	}
}

// selfSignedPEM is a throwaway client certificate and key, for options that
// only need to parse.
func selfSignedPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unit"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		EmailAddresses: []string{"unit@example.com"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

// auth_type=client_certificate is the Warp SQL driver's adbc.warp.auth.type:
// the Warp SQL driver's own checks run on it (they need a certificate, TLS
// and no password), so a refusal from it proves the option reached it.
func TestClientCertificateAuthTypeIsTheWarpOption(t *testing.T) {
	ctx := context.Background()
	cert, key := selfSignedPEM(t)
	withCertificate := func(extra map[string]string) map[string]string {
		opts := map[string]string{adbc.OptionKeyURI: "hardcharger://h", OptionAuthType: AuthTypeClientCertificate,
			"adbc.warp.tls.client_cert": cert, "adbc.warp.tls.client_key": key}
		for k, v := range extra {
			if v == "<unset>" {
				delete(opts, k)
			} else {
				opts[k] = v
			}
		}
		return opts
	}
	for name, tc := range map[string]struct {
		opts map[string]string
		text string
	}{
		"no certificate": {withCertificate(map[string]string{"adbc.warp.tls.client_cert": "<unset>",
			"adbc.warp.tls.client_key": "<unset>"}), "needs a client certificate: set adbc.warp.tls.client_cert and adbc.warp.tls.client_key"},
		"a password beside it": {withCertificate(map[string]string{adbc.OptionKeyUsername: "joe", adbc.OptionKeyPassword: "pw"}),
			"signs in with the client certificate alone; do not also set username, password"},
		"plain HTTP": {withCertificate(map[string]string{adbc.OptionKeyURI: "hardcharger://h?tls=false"}), "needs TLS"},
	} {
		_, err := newDB(t, tc.opts)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		wantStatus(t, err, adbc.StatusInvalidArgument, tc.text)
	}
	// The twin: certificate and key over TLS, under either spelling.
	for _, spelling := range []string{AuthTypeClientCertificate, "mtls"} {
		db, err := newDB(t, withCertificate(map[string]string{OptionAuthType: spelling}))
		if err != nil {
			t.Fatalf("%s: %v", spelling, err)
		}
		for _, k := range []string{OptionAuthType, warpAuthType} {
			if got, err := db.GetOption(ctx, k); err != nil || got != AuthTypeClientCertificate {
				t.Errorf("%s: GetOption(%s) = %q, %v", spelling, k, got, err)
			}
		}
	}
	// Back to a password: the Warp SQL driver is told too.
	db, err := newDB(t, withCertificate(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetOption(ctx, OptionAuthType, AuthTypePassword); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetOption(ctx, warpAuthType); got != AuthTypePassword {
		t.Errorf("warp auth type %q after auth_type=password", got)
	}
}
