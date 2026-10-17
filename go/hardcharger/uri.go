// Licensed under the Apache License, Version 2.0.

package hardcharger

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
)

const (
	// Scheme is Hardcharger's URI scheme: hardcharger://host[:port] means
	// exactly warp://host:port (HTTPS unless ?tls=false).
	Scheme = "hardcharger"
	// DefaultPort is Hardcharger's Warp SQL port, used when a hardcharger://
	// URI names none (warp:// would default to 443).
	DefaultPort = 31337
	// DefaultOAuthPort is the port of Hardcharger's browser sign-in server
	// (hardcharger-server --oauth-port).
	DefaultOAuthPort = 31339
)

// ToWarpURI translates a hardcharger:// URI into the warp:// URI the Warp SQL
// driver reads, adding the default port 31337 when the URI has none.
// warp://, https:// and http:// URIs pass through unchanged; the Warp SQL
// driver validates everything else (parameters, credentials in the URI).
func ToWarpURI(raw string) (string, error) {
	if raw == "" {
		return "", invalidArgument("a uri is required, e.g. hardcharger://host:31337")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error repeats the URI, password included: report only the cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return "", invalidArgument("invalid uri: %v", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == Scheme:
	case strings.HasPrefix(scheme, Scheme+"+"):
		return "", invalidArgument("%s:// is not a Hardcharger URI scheme: use hardcharger://host:port for HTTPS, "+
			"or hardcharger://host:port?tls=false for plain HTTP", u.Scheme)
	default:
		return raw, nil // warp://, https://, http://, or something the Warp SQL driver refuses
	}
	if u.Hostname() == "" {
		return "", invalidArgument("invalid uri %q: no host", u.Redacted())
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(DefaultPort))
	}
	u.Scheme = "warp"
	return u.String(), nil
}

// usesTLS reports whether a (translated) Warp SQL URI connects over TLS.
func usesTLS(warpURI string) bool {
	u, err := url.Parse(warpURI)
	if err != nil {
		return true
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return false
	case "warp":
		switch strings.ToLower(strings.TrimSpace(u.Query().Get("tls"))) {
		case "false", "0", "f", "no", "off":
			return false
		}
	}
	return true
}

func invalidArgument(format string, args ...any) error {
	return adbc.Error{Code: adbc.StatusInvalidArgument, Msg: "[hardcharger] " + fmt.Sprintf(format, args...)}
}
