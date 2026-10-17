"""ADBC driver for Hardcharger, GizmoData's DuckDB 2.0 server.

A thin layer on the Warp SQL ADBC driver (``adbc_driver_warpsql``): ``hardcharger://`` URIs
with Hardcharger's default port, helpers for Hardcharger's session settings and query tags,
and browser sign-in.

Quick start::

    from adbc_driver_hardcharger import dbapi as hardcharger

    with hardcharger.connect(uri="hardcharger://localhost?tls=false", username="hardcharger",
                             password="change-me") as conn:
        with conn.cursor() as cur:
            cur.execute(operation="SELECT 42 AS answer")
            print(cur.fetch_arrow_table())
"""

from __future__ import annotations

from ._oauth import HardchargerOAuthError, OAuthResult, get_oauth_token
from ._options import ConnectionOptions, DatabaseOptions, StatementOptions
from ._uri import DEFAULT_OAUTH_PORT, DEFAULT_PORT, to_warp_uri
from ._version import __version__

__all__ = [
    "DEFAULT_OAUTH_PORT",
    "DEFAULT_PORT",
    "ConnectionOptions",
    "DatabaseOptions",
    "HardchargerOAuthError",
    "OAuthResult",
    "StatementOptions",
    "__version__",
    "get_oauth_token",
    "to_warp_uri",
]
