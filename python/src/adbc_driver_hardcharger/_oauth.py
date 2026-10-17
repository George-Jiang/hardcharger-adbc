"""Browser sign-in through Hardcharger's sign-in server (``hardcharger-server --oauth-port``).

The flow, as GizmoSQL's and the ``hardcharger`` command-line client's:

1. ``GET /oauth/initiate`` answers ``{session_uuid, auth_url}``;
2. the user signs in to the identity provider at ``auth_url``, in a browser;
3. ``GET /oauth/token/{session_uuid}`` answers ``pending`` until the provider has answered,
   then hands out the provider's ID token once;
4. the driver exchanges that ID token for a Warp SQL access token (grant ``token_exchange``).

Standard library only.
"""

from __future__ import annotations

import json
import ssl
import sys
import time
import urllib.error
import urllib.request
import webbrowser
from dataclasses import dataclass
from typing import Any, Callable, Optional, Union

from ._uri import DEFAULT_OAUTH_PORT

DEFAULT_POLL_INTERVAL = 1.0  # seconds
DEFAULT_TIMEOUT = 300  # seconds

#: ``True`` opens the system browser, ``False`` prints the URL, a callable receives it.
BrowserOpener = Union[bool, Callable[[str], Any]]


class HardchargerOAuthError(Exception):
    """The browser sign-in failed."""


@dataclass
class OAuthResult:
    """A finished browser sign-in.

    Attributes:
        token: The identity provider's ID token (a JWT).
        session_uuid: The sign-in server's id for this sign-in.
    """

    token: str
    session_uuid: str


def _ssl_context(tls_skip_verify: bool, tls_root_certs: Optional[str]) -> ssl.SSLContext:
    if tls_root_certs:
        context = ssl.create_default_context(cadata=tls_root_certs)
    else:
        context = ssl.create_default_context()
    if tls_skip_verify:
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
    return context


def _get_json(url: str, context: Optional[ssl.SSLContext]) -> tuple[int, dict]:
    request = urllib.request.Request(url=url, method="GET", headers={"Accept": "application/json"})
    try:
        with urllib.request.urlopen(url=request, context=context, timeout=30) as response:
            return response.status, json.loads(s=response.read() or b"{}")
    except urllib.error.HTTPError as error:
        body = error.read()
        try:
            return error.code, json.loads(s=body or b"{}")
        except ValueError:
            return error.code, {"error": body.decode(encoding="utf-8", errors="replace")}
    except urllib.error.URLError as error:
        raise HardchargerOAuthError(
            f"could not reach Hardcharger's sign-in server at {url}: {error.reason}"
        ) from error


def _open(auth_url: str, open_browser: BrowserOpener) -> None:
    if callable(open_browser):
        open_browser(auth_url)
        return
    if open_browser and webbrowser.open(url=auth_url):
        print("Waiting for the browser sign-in... (Ctrl+C cancels)", file=sys.stderr)
        return
    print(f"Open this URL in a browser to sign in:\n{auth_url}", file=sys.stderr)


def get_oauth_token(
    *,
    oauth_url: Optional[str] = None,
    host: Optional[str] = None,
    port: int = DEFAULT_OAUTH_PORT,
    tls: bool = True,
    tls_skip_verify: bool = False,
    tls_root_certs: Optional[str] = None,
    timeout: float = DEFAULT_TIMEOUT,
    poll_interval: float = DEFAULT_POLL_INTERVAL,
    open_browser: BrowserOpener = True,
) -> OAuthResult:
    """Sign in through Hardcharger's browser sign-in and return the identity provider's ID token.

    Args:
        oauth_url: The sign-in server's base URL (``hardcharger-server --oauth-base-url``).
            Default: ``https://HOST:PORT`` (``http://`` when ``tls`` is false).
        host: The server's host, for the default ``oauth_url``.
        port: The sign-in server's port (``--oauth-port``, default 31339).
        tls: Whether the default ``oauth_url`` is HTTPS; follows the Warp SQL connection.
            There is no fallback to plain HTTP.
        tls_skip_verify: Do not verify the sign-in server's certificate (development only).
        tls_root_certs: PEM text of the CA certificates to trust instead of the system's.
        timeout: Seconds to wait for the user to finish signing in.
        poll_interval: Seconds between polls of the sign-in server.
        open_browser: ``True`` opens the system browser, ``False`` prints the URL to stderr, and
            a callable is called with the URL (for notebooks, or a browser of your choosing).

    Raises:
        HardchargerOAuthError: when the sign-in fails, expires or times out.
    """
    if oauth_url is None:
        if not host:
            raise ValueError("browser sign-in needs the server's host or an oauth_url")
        oauth_url = f"{'https' if tls else 'http'}://{host}:{port}"
    base = oauth_url.rstrip("/")
    context = _ssl_context(tls_skip_verify=tls_skip_verify, tls_root_certs=tls_root_certs)
    if not base.lower().startswith("https://"):
        context = None

    status, flow = _get_json(url=f"{base}/oauth/initiate", context=context)
    if status != 200:
        raise HardchargerOAuthError(
            f"Hardcharger's sign-in server at {base} answered HTTP {status} to /oauth/initiate: "
            f"{flow.get('error') or flow}"
        )
    session_uuid = flow.get("session_uuid")
    auth_url = flow.get("auth_url")
    if not session_uuid or not auth_url:
        raise HardchargerOAuthError(f"unexpected answer from {base}/oauth/initiate: {flow}")

    _open(auth_url=auth_url, open_browser=open_browser)

    poll_url = f"{base}/oauth/token/{session_uuid}"
    deadline = time.monotonic() + timeout
    while True:
        status, body = _get_json(url=poll_url, context=context)
        state = body.get("status")
        if status == 404 or state == "not_found":
            raise HardchargerOAuthError("the browser sign-in expired or was already used")
        if status != 200:
            raise HardchargerOAuthError(f"polling the browser sign-in failed with HTTP {status}")
        if state == "complete":
            token = body.get("token")
            if not token:
                raise HardchargerOAuthError("the browser sign-in completed without a token")
            return OAuthResult(token=token, session_uuid=session_uuid)
        if state == "error":
            raise HardchargerOAuthError(
                f"the browser sign-in failed: {body.get('error') or 'unknown error'}"
            )
        if state != "pending":
            raise HardchargerOAuthError(f"unexpected browser sign-in status {state!r}")
        if time.monotonic() >= deadline:
            raise HardchargerOAuthError(f"the browser sign-in timed out after {timeout:g} seconds")
        time.sleep(poll_interval)
