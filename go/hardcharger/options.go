// Licensed under the Apache License, Version 2.0.

package hardcharger

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
)

// Hardcharger's ADBC options. Every other option is the Warp SQL driver's
// (adbc.warp.*, uri, username, password, ...).
const (
	optionPrefix = "adbc.hardcharger."

	// OptionAuthType is "password" (the default; also for a pre-issued
	// access token) or "external": sign in through the server's browser
	// sign-in (hardcharger-server --oauth-client-id), when the first
	// connection opens.
	OptionAuthType   = "adbc.hardcharger.auth_type"
	AuthTypePassword = "password"
	AuthTypeExternal = "external"

	// OptionOAuthURL is the browser sign-in server's base URL
	// (hardcharger-server --oauth-base-url). Default: https://HOST:PORT, or
	// http:// when the connection is plain HTTP.
	OptionOAuthURL = "adbc.hardcharger.oauth.url"
	// OptionOAuthPort is the browser sign-in server's port (default 31339).
	OptionOAuthPort = "adbc.hardcharger.oauth.port"
	// OptionOAuthTimeoutSeconds is how long to wait for the user to sign in
	// (default 300).
	OptionOAuthTimeoutSeconds = "adbc.hardcharger.oauth.timeout_seconds"
	// OptionOAuthBrowser is the command that opens the sign-in page, given
	// its URL as the last argument. Default: $HARDCHARGER_BROWSER, else the
	// system's (open, xdg-open, rundll32). "none" only prints the URL to
	// stderr.
	OptionOAuthBrowser = "adbc.hardcharger.oauth.browser"

	// OptionSessionTag is a JSON object tagging the session
	// (hardcharger.session_tag) in Hardcharger's instrumentation.
	OptionSessionTag = "adbc.hardcharger.session_tag"
	// OptionQueryTag is a JSON object recorded with each of the session's
	// following statements (hardcharger.query_tag).
	OptionQueryTag = "adbc.hardcharger.query_tag"
	// OptionSettingPrefix + NAME sets the session setting hardcharger.NAME
	// (SET SESSION); an empty value resets it. On a connection, GetOption
	// reads the value that applies to the session.
	OptionSettingPrefix = "adbc.hardcharger.setting."
	// OptionGlobalSettingPrefix + NAME sets the server-wide setting
	// hardcharger.NAME (SET GLOBAL; admins only); an empty value resets it.
	// Connections only.
	OptionGlobalSettingPrefix = "adbc.hardcharger.global_setting."
)

const defaultOAuthTimeout = 300 * time.Second

var settingNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// setting is one hardcharger.NAME = VALUE, in the order it was given.
type setting struct {
	name, value string
}

// config holds a database's Hardcharger options.
type config struct {
	authType     string
	oauthURL     string
	oauthPort    int
	oauthTimeout time.Duration
	browser      string
	sessionTag   *string
	queryTag     *string
	settings     []setting
}

func newConfig() *config {
	return &config{authType: AuthTypePassword, oauthPort: DefaultOAuthPort, oauthTimeout: defaultOAuthTimeout}
}

func isHardchargerOption(key string) bool { return strings.HasPrefix(key, optionPrefix) }

// settingName validates NAME (also accepting hardcharger.NAME); it becomes
// part of a SET statement, so only [a-z0-9_] are allowed.
func settingName(name string) (string, error) {
	bare := strings.TrimPrefix(name, "hardcharger.")
	if !settingNamePattern.MatchString(bare) {
		return "", invalidArgument("invalid Hardcharger setting name %q: expected a name such as "+
			"query_timeout (lower-case letters, digits and underscores)", name)
	}
	return bare, nil
}

func (c *config) set(key, value string) error {
	switch {
	case key == OptionAuthType:
		if value != AuthTypePassword && value != AuthTypeExternal {
			return invalidArgument("invalid value %q for %s: expected password or external", value, key)
		}
		c.authType = value
	case key == OptionOAuthURL:
		c.oauthURL = value
	case key == OptionOAuthPort:
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return invalidArgument("invalid value %q for %s: expected a port number", value, key)
		}
		c.oauthPort = port
	case key == OptionOAuthTimeoutSeconds:
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil || seconds <= 0 {
			return invalidArgument("invalid value %q for %s: expected a number of seconds", value, key)
		}
		c.oauthTimeout = time.Duration(seconds * float64(time.Second))
	case key == OptionOAuthBrowser:
		c.browser = value
	case key == OptionSessionTag:
		c.sessionTag = &value
	case key == OptionQueryTag:
		c.queryTag = &value
	case strings.HasPrefix(key, OptionSettingPrefix):
		name, err := settingName(strings.TrimPrefix(key, OptionSettingPrefix))
		if err != nil {
			return err
		}
		c.settings = append(c.settings, setting{name: name, value: value})
	case strings.HasPrefix(key, OptionGlobalSettingPrefix):
		return invalidArgument("%s is a connection option: set it on an open connection", key)
	default:
		return adbc.Error{Code: adbc.StatusNotImplemented, Msg: "[hardcharger] unknown option " + key}
	}
	return nil
}

func (c *config) get(key string) (string, error) {
	switch {
	case key == OptionAuthType:
		return c.authType, nil
	case key == OptionOAuthURL:
		return c.oauthURL, nil
	case key == OptionOAuthPort:
		return strconv.Itoa(c.oauthPort), nil
	case key == OptionOAuthTimeoutSeconds:
		return strconv.FormatFloat(c.oauthTimeout.Seconds(), 'g', -1, 64), nil
	case key == OptionOAuthBrowser:
		return c.browser, nil
	case key == OptionSessionTag && c.sessionTag != nil:
		return *c.sessionTag, nil
	case key == OptionQueryTag && c.queryTag != nil:
		return *c.queryTag, nil
	case strings.HasPrefix(key, OptionSettingPrefix):
		name := strings.TrimPrefix(strings.TrimPrefix(key, OptionSettingPrefix), "hardcharger.")
		for i := len(c.settings) - 1; i >= 0; i-- {
			if c.settings[i].name == name {
				return c.settings[i].value, nil
			}
		}
	}
	return "", adbc.Error{Code: adbc.StatusNotFound, Msg: "[hardcharger] option " + key + " is not set"}
}

// sqlString quotes value as a SQL string literal.
func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// setStatement is SET (or, for an empty value, RESET) of hardcharger.NAME.
func setStatement(name, value string, global bool) string {
	scope := "SESSION"
	if global {
		scope = "GLOBAL"
	}
	if value == "" {
		return "RESET " + scope + " hardcharger." + name
	}
	return "SET " + scope + " hardcharger." + name + " = " + sqlString(value)
}
