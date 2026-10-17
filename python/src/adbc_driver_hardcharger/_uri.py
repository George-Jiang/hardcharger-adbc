"""``hardcharger://`` URIs, translated to the Warp SQL driver's ``warp://``."""

from __future__ import annotations

import urllib.parse

#: Hardcharger's Warp SQL port, used when a ``hardcharger://`` URI names none.
DEFAULT_PORT = 31337
#: The port of Hardcharger's browser sign-in server (``--oauth-port``).
DEFAULT_OAUTH_PORT = 31339

SCHEME = "hardcharger"

_PASS_THROUGH = ("warp", "https", "http")


def to_warp_uri(uri: str) -> str:
    """Translate a ``hardcharger://`` URI into the ``warp://`` URI the Warp SQL driver reads.

    ``hardcharger://host[:port][/base-path][?tls=false]`` means exactly what
    ``warp://host:port[/base-path][?tls=false]`` means (HTTPS unless ``?tls=false``), except that
    the port defaults to Hardcharger's 31337 instead of HTTPS's 443. ``warp://``, ``https://``
    and ``http://`` URIs are passed through unchanged; the Warp SQL driver validates the rest
    (query parameters, credentials in the URI, and so on).

    Raises:
        ValueError: for an empty URI, a ``hardcharger+…://`` scheme, or a URI without a host.
    """
    if not uri:
        raise ValueError("a uri is required, e.g. hardcharger://host:31337")
    parts = urllib.parse.urlsplit(url=uri)
    scheme = parts.scheme.lower()
    if scheme in _PASS_THROUGH:
        return uri
    if scheme.startswith(SCHEME + "+"):
        raise ValueError(
            f"{parts.scheme}:// is not a Hardcharger URI scheme: use hardcharger://host:port for "
            "HTTPS, or hardcharger://host:port?tls=false for plain HTTP"
        )
    if scheme != SCHEME:
        # Not ours: let the Warp SQL driver refuse it with its own message.
        return uri
    if not parts.hostname:
        raise ValueError(f"invalid uri {_redacted(parts=parts)!r}: no host")
    try:
        port = parts.port
    except ValueError as error:
        raise ValueError(f"invalid uri {_redacted(parts=parts)!r}: {error}") from None
    netloc = parts.netloc
    if port is None:
        netloc = f"{netloc.rstrip(':')}:{DEFAULT_PORT}"
    return urllib.parse.urlunsplit(
        components=("warp", netloc, parts.path, parts.query, parts.fragment)
    )


def uses_tls(warp_uri: str) -> bool:
    """Whether a (translated) Warp SQL URI connects over TLS."""
    parts = urllib.parse.urlsplit(url=warp_uri)
    scheme = parts.scheme.lower()
    if scheme == "http":
        return False
    if scheme == "warp":
        for key, value in urllib.parse.parse_qsl(qs=parts.query):
            if key == "tls" and value.strip().lower() in ("false", "0", "f", "no", "off"):
                return False
    return True


def host_of(warp_uri: str) -> str:
    """The host a (translated) Warp SQL URI names."""
    return urllib.parse.urlsplit(url=warp_uri).hostname or ""


def _redacted(parts: urllib.parse.SplitResult) -> str:
    if parts.password is None:
        return parts.geturl()
    netloc = parts.netloc.replace(f":{parts.password}@", ":xxxxx@", 1)
    return parts._replace(netloc=netloc).geturl()
