// Licensed under the Apache License, Version 2.0.

//go:build integration

package hardcharger

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func insecureTransport() *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // test probe
}

func errorsAsADBC(err error, target *adbc.Error) bool { return errors.As(err, target) }

// --- connecting ---------------------------------------------------------------------------

func TestIntegrationSelectThroughAHardchargerURI(t *testing.T) {
	cnxn := mustOpen(t, shared, nil)
	if got := scalar(t, cnxn, "SELECT 42 AS answer"); got != "42" {
		t.Fatalf("got %s", got)
	}
	if got := scalar(t, cnxn, "SELECT HARDCHARGER_USER()"); got != testUsername {
		t.Fatalf("user %s", got)
	}
}

func TestIntegrationGetInfoNamesHardcharger(t *testing.T) {
	values := info(t, mustOpen(t, shared, nil))
	if values[adbc.InfoDriverName] != DriverName {
		t.Errorf("driver name %q", values[adbc.InfoDriverName])
	}
	if values[adbc.InfoVendorName] != "Hardcharger" {
		t.Errorf("vendor name %q", values[adbc.InfoVendorName])
	}
}

func TestIntegrationWrongPasswordIsRefused(t *testing.T) {
	_, _, err := open(t, shared, map[string]string{adbc.OptionKeyPassword: "not-the-password"})
	wantStatus(t, err, adbc.StatusUnauthenticated, "the user name or password is not correct")
	if code := warpCode(err); code != "invalid_credentials" {
		t.Fatalf("warp.code %q", code)
	}
	// The twin: the right password.
	mustOpen(t, shared, nil)
}

func TestIntegrationWarpURIWorksToo(t *testing.T) {
	cnxn := mustOpen(t, shared, map[string]string{
		adbc.OptionKeyURI: "warp://127.0.0.1:" + strconv.Itoa(shared.port) + "?tls=false"})
	if got := scalar(t, cnxn, "SELECT 1"); got != "1" {
		t.Fatal(got)
	}
}

func TestIntegrationPlusHTTPIsRefused(t *testing.T) {
	_, _, err := open(t, shared, map[string]string{
		adbc.OptionKeyURI: "hardcharger+http://127.0.0.1:" + strconv.Itoa(shared.port)})
	wantStatus(t, err, adbc.StatusInvalidArgument, "?tls=false for plain HTTP")
}

// --- settings and tags --------------------------------------------------------------------

func TestIntegrationDatabaseSettingsApplyToEverySession(t *testing.T) {
	db, cnxn, err := open(t, shared, map[string]string{
		OptionSettingPrefix + "query_timeout": "12",
		OptionSettingPrefix + "bypass_queue":  "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := cnxn.(adbc.GetSetOptionsWithContext)
	ctx := context.Background()
	for name, want := range map[string]string{"query_timeout": "12", "bypass_queue": "true"} {
		if got, err := opts.GetOption(ctx, OptionSettingPrefix+name); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	// A second connection gets them too.
	second, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	if got := scalar(t, second, "SELECT value FROM hardcharger_settings() WHERE name = 'hardcharger.query_timeout'"); got != "12" {
		t.Errorf("second connection: %s", got)
	}
}

func TestIntegrationConnectionSettings(t *testing.T) {
	ctx := context.Background()
	opts := mustOpen(t, shared, nil).(adbc.GetSetOptionsWithContext)
	key := OptionSettingPrefix + "query_timeout"
	if got, _ := opts.GetOption(ctx, key); got != "0" {
		t.Fatalf("default %q", got)
	}
	if err := opts.SetOption(ctx, key, "30"); err != nil {
		t.Fatal(err)
	}
	if got, _ := opts.GetOption(ctx, key); got != "30" {
		t.Fatalf("after SET %q", got)
	}
	if err := opts.SetOption(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := opts.GetOption(ctx, key); got != "0" {
		t.Fatalf("after RESET %q", got)
	}
}

func TestIntegrationInvalidSettingValueIsRefused(t *testing.T) {
	ctx := context.Background()
	opts := mustOpen(t, shared, nil).(adbc.GetSetOptionsWithContext)
	err := opts.SetOption(ctx, OptionSettingPrefix+"query_timeout", "soon")
	wantStatus(t, err, adbc.StatusInvalidArgument, "Invalid value for query_timeout: 'soon' is not a whole number")
	if code := warpCode(err); code != "sql_error" {
		t.Fatalf("warp.code %q", code)
	}
	// The twin.
	if err := opts.SetOption(ctx, OptionSettingPrefix+"query_timeout", "45"); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationUnknownSettingIsRefused(t *testing.T) {
	ctx := context.Background()
	opts := mustOpen(t, shared, nil).(adbc.GetSetOptionsWithContext)
	wantStatus(t, opts.SetOption(ctx, OptionSettingPrefix+"no_such_setting", "1"),
		adbc.StatusInvalidArgument, "Unknown Hardcharger setting: hardcharger.no_such_setting")
	_, err := opts.GetOption(ctx, OptionSettingPrefix+"no_such_setting")
	wantStatus(t, err, adbc.StatusNotFound, "no setting hardcharger.no_such_setting")
}

func TestIntegrationGlobalSettingNeedsGlobalScope(t *testing.T) {
	ctx := context.Background()
	opts := mustOpen(t, shared, nil).(adbc.GetSetOptionsWithContext)
	wantStatus(t, opts.SetOption(ctx, OptionSettingPrefix+"graceful_shutdown", "false"),
		adbc.StatusInvalidArgument, "hardcharger.graceful_shutdown can only be set with SET GLOBAL")
	// The twin, with the global option.
	if err := opts.SetOption(ctx, OptionGlobalSettingPrefix+"graceful_shutdown", "false"); err != nil {
		t.Fatal(err)
	}
	if got, _ := opts.GetOption(ctx, OptionGlobalSettingPrefix+"graceful_shutdown"); got != "false" {
		t.Fatalf("got %q", got)
	}
	if err := opts.SetOption(ctx, OptionGlobalSettingPrefix+"graceful_shutdown", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := opts.GetOption(ctx, OptionSettingPrefix+"graceful_shutdown"); got != "true" {
		t.Fatalf("after RESET %q", got)
	}
}

func TestIntegrationDatabaseOnlyOptionsAreRefusedOnConnections(t *testing.T) {
	opts := mustOpen(t, shared, nil).(adbc.GetSetOptionsWithContext)
	wantStatus(t, opts.SetOption(context.Background(), OptionAuthType, AuthTypeExternal),
		adbc.StatusInvalidArgument, "is a database option")
}

func TestIntegrationABadDatabaseSettingFailsOpen(t *testing.T) {
	_, _, err := open(t, shared, map[string]string{OptionSettingPrefix + "query_timeout": "soon"})
	wantStatus(t, err, adbc.StatusInvalidArgument, "'soon' is not a whole number")
}

func TestIntegrationTagsAreRecorded(t *testing.T) {
	server := startServer(t, "--enable-instrumentation")
	ctx := context.Background()
	cnxn := mustOpen(t, server, map[string]string{
		OptionSessionTag: `{"app":"hardcharger-adbc-go-tests"}`,
		OptionQueryTag:   `{"job":"nightly-load"}`,
	})
	scalar(t, cnxn, "SELECT 1 AS tagged_by_option")
	session := scalar(t, cnxn, "SELECT CAST(HARDCHARGER_CURRENT_SESSION() AS VARCHAR)")
	eventually(t, cnxn, "SELECT query_tag->>'job' FROM _hardcharger_instr.sql_statements "+
		"WHERE sql_text LIKE '%tagged_by_option%' AND sql_text NOT LIKE '%sql_statements%'", "nightly-load")
	eventually(t, cnxn, "SELECT session_tag->>'app' FROM _hardcharger_instr.sessions "+
		"WHERE session_id = '"+session+"'", "hardcharger-adbc-go-tests")

	opts := cnxn.(adbc.GetSetOptionsWithContext)
	if err := opts.SetOption(ctx, OptionQueryTag, `{"job":"second"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := opts.GetOption(ctx, OptionQueryTag); got != `{"job":"second"}` {
		t.Fatalf("query tag %q", got)
	}
	scalar(t, cnxn, "SELECT 2 AS tagged_second")
	eventually(t, cnxn, "SELECT query_tag->>'job' FROM _hardcharger_instr.sql_statements "+
		"WHERE sql_text LIKE '%tagged_second%' AND sql_text NOT LIKE '%sql_statements%'", "second")

	if err := opts.SetOption(ctx, OptionQueryTag, ""); err != nil {
		t.Fatal(err)
	}
	scalar(t, cnxn, "SELECT 3 AS untagged_third")
	eventually(t, cnxn, "SELECT CAST(query_tag IS NULL AS VARCHAR) FROM _hardcharger_instr.sql_statements "+
		"WHERE sql_text LIKE '%untagged_third%' AND sql_text NOT LIKE '%sql_statements%'", "true")

	// Not JSON: refused by the server, with its reason; the JSON twin is above.
	wantStatus(t, opts.SetOption(ctx, OptionQueryTag, "not json"), adbc.StatusInvalidArgument, "Invalid JSON for query_tag")
}

// --- TLS ------------------------------------------------------------------------------------

func writeCertificates(t *testing.T) (caPEM []byte, certFile, keyFile string) {
	t.Helper()
	now := time.Now()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hardcharger-adbc test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certFile, keyFile
}

func TestIntegrationTLSIsTheDefaultAndVerifies(t *testing.T) {
	caPEM, certFile, keyFile := writeCertificates(t)
	server := startServer(t, "--tls-cert", certFile, "--tls-key", keyFile)
	if !strings.HasPrefix(server.uri(), "hardcharger://") || strings.Contains(server.uri(), "tls=") {
		t.Fatalf("uri %s", server.uri())
	}
	_, _, err := open(t, server, nil)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("an untrusted certificate: %v", err)
	}
	cnxn := mustOpen(t, server, map[string]string{"adbc.warp.tls.root_certs": string(caPEM)})
	if got := scalar(t, cnxn, "SELECT 1"); got != "1" {
		t.Fatal(got)
	}
}

// --- browser sign-in ------------------------------------------------------------------------

const (
	oauthAudience = "hardcharger-adbc-test"
	oauthClientID = "hardcharger-adbc-client"
	oauthSecret   = "hardcharger-adbc-secret"
)

// mockIdP is a small OpenID Connect provider: discovery, a JWKS, and a token
// endpoint that trades validCode for a signed ID token naming email.
type mockIdP struct {
	server    *httptest.Server
	key       *rsa.PrivateKey
	validCode string
	email     string
	lifetime  atomic.Int64 // of the ID tokens it signs, in seconds
}

func newMockIdP(t *testing.T) *mockIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &mockIdP{key: key, validCode: "code-ok", email: "analyst@gizmodata.com"}
	idp.lifetime.Store(300)
	idp.server = httptest.NewUnstartedServer(http.HandlerFunc(idp.serve))
	idp.server.Start() // after the assignment: the handler reads idp.server
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *mockIdP) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	issuer := idp.server.URL
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		reply(200, map[string]string{"issuer": issuer, "jwks_uri": issuer + "/jwks",
			"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token"})
	case "/jwks":
		b64 := base64.RawURLEncoding.EncodeToString
		reply(200, map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(idp.key.N.Bytes()), "e": b64(big.NewInt(int64(idp.key.E)).Bytes())}}})
	case "/token":
		_ = r.ParseForm()
		if r.Form.Get("client_id") != oauthClientID || r.Form.Get("client_secret") != oauthSecret {
			reply(401, map[string]string{"error": "invalid_client"})
		} else if r.Form.Get("code") != idp.validCode {
			reply(400, map[string]string{"error": "invalid_grant", "error_description": "the code is not valid"})
		} else {
			reply(200, map[string]string{"access_token": "unused", "token_type": "Bearer", "id_token": idp.idToken()})
		}
	default:
		reply(404, map[string]string{"error": "not_found"})
	}
}

func (idp *mockIdP) idToken() string {
	b64 := func(v any) string { data, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(data) }
	now := time.Now().Unix()
	unsigned := b64(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + b64(map[string]any{
		"iss": idp.server.URL, "aud": oauthAudience, "sub": idp.email, "email": idp.email, "iat": now, "exp": now + idp.lifetime.Load()})
	digest := sha256.Sum256([]byte(unsigned))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func startOAuthServer(t *testing.T, idp *mockIdP) *testServer {
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	return startServer(t, "--token-allowed-issuer", idp.server.URL, "--token-allowed-audience", oauthAudience,
		"--token-default-role", "admin", "--token-authorized-emails", "*@gizmodata.com",
		"--oauth-client-id", oauthClientID, "--oauth-client-secret", oauthSecret, "--oauth-port", strconv.Itoa(port))
}

// browserThatSignsIn stands in for the user's browser: the identity provider
// sends it back to the server's callback with code, as after a sign-in.
func browserThatSignsIn(t *testing.T, server *testServer, code string) {
	saved := openBrowser
	openBrowser = func(_ string, authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		callback := "http://127.0.0.1:" + strconv.Itoa(server.oauthPort) + "/oauth/callback?" +
			url.Values{"code": {code}, "state": {parsed.Query().Get("state")}}.Encode()
		resp, err := http.Get(callback)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	t.Cleanup(func() { openBrowser = saved })
}

func TestIntegrationBrowserSignIn(t *testing.T) {
	idp := newMockIdP(t)
	server := startOAuthServer(t, idp)
	browserThatSignsIn(t, server, "code-ok")
	cnxn := mustOpen(t, server, map[string]string{adbc.OptionKeyUsername: "<unset>", adbc.OptionKeyPassword: "<unset>",
		OptionAuthType: AuthTypeExternal, OptionOAuthPort: strconv.Itoa(server.oauthPort)})
	if got := scalar(t, cnxn, "SELECT HARDCHARGER_USER()"); got != idp.email {
		t.Fatalf("signed in as %s", got)
	}
}

func TestIntegrationBrowserSignInWithARejectedCode(t *testing.T) {
	idp := newMockIdP(t)
	server := startOAuthServer(t, idp)
	browserThatSignsIn(t, server, "code-bad")
	_, _, err := open(t, server, map[string]string{adbc.OptionKeyUsername: "<unset>", adbc.OptionKeyPassword: "<unset>",
		OptionAuthType: AuthTypeExternal, OptionOAuthURL: "http://127.0.0.1:" + strconv.Itoa(server.oauthPort)})
	wantStatus(t, err, adbc.StatusUnauthenticated, "the sign-in failed")
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error %v", err)
	}
}

// A prepared hardcharger_settings() query runs again with another parameter
// and reads that row (hardcharger-server before 3fbd36b answered no rows: its
// table functions built their rows at bind time). GetOption relies on it.
func TestIntegrationPreparedSettingsQueryReExecutes(t *testing.T) {
	ctx := context.Background()
	cnxn := mustOpen(t, shared, nil)
	stmt, err := cnxn.NewStatement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close(ctx)
	if err := stmt.SetSqlQuery(ctx, "SELECT CAST(value AS VARCHAR) FROM hardcharger_settings() WHERE name = ?"); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"hardcharger.query_timeout": "0", "hardcharger.graceful_shutdown": "true",
		"hardcharger.bypass_queue": "false"} {
		builder := array.NewRecordBuilder(memory.DefaultAllocator,
			arrow.NewSchema([]arrow.Field{{Name: "name", Type: arrow.BinaryTypes.String}}, nil))
		builder.Field(0).(*array.StringBuilder).Append(name)
		params := builder.NewRecordBatch()
		builder.Release()
		if err := stmt.Bind(ctx, params); err != nil {
			t.Fatal(err)
		}
		params.Release()
		reader, _, err := stmt.ExecuteQuery(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for reader.Next() {
			rec := reader.RecordBatch()
			for i := range int(rec.NumRows()) {
				got = append(got, rec.Column(0).ValueStr(i))
			}
		}
		reader.Release()
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: rows %q, want [%q]", name, got, want)
		}
	}
}
