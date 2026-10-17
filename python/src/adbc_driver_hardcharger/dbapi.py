"""DB-API 2.0 interface for Hardcharger, on the Warp SQL ADBC driver.

Example::

    from adbc_driver_hardcharger import dbapi as hardcharger

    with hardcharger.connect(uri="hardcharger://db.example.com", username="joe",
                             password="...") as conn:
        with conn.cursor() as cur:
            cur.execute(operation="SELECT 42 AS answer")
            print(cur.fetch_arrow_table())

``hardcharger://host[:port]`` is HTTPS (TLS on), with the port defaulting to 31337; add
``?tls=false`` for plain HTTP to a development server. Everything else is the Warp SQL driver's
(``adbc_driver_warpsql``): the options, the cursor, Arrow results, bulk ingest, partitioned
results and their defaults.
"""

from __future__ import annotations

import json
import os
import re
from collections.abc import Mapping
from typing import Any, Literal, Optional, Union

from adbc_driver_warpsql import dbapi as _warp

# The DB-API 2.0 module globals and type constructors.
from adbc_driver_warpsql.dbapi import (  # noqa: F401
    Cursor,
    Date,
    DateFromTicks,
    Time,
    TimeFromTicks,
    Timestamp,
    TimestampFromTicks,
    apilevel,
    paramstyle,
    threadsafety,
)

from ._oauth import (
    DEFAULT_TIMEOUT,
    BrowserOpener,
    HardchargerOAuthError,  # noqa: F401
    get_oauth_token,
)
from ._options import DatabaseOptions
from ._uri import DEFAULT_OAUTH_PORT, host_of, to_warp_uri, uses_tls

PemSource = Union[str, bytes, "os.PathLike[str]"]
#: A query or session tag: a JSON object, as a mapping or as JSON text.
Tag = Union[Mapping[str, Any], str]
SettingValue = Union[str, int, bool]

_SETTING_NAME = re.compile(pattern=r"^[a-z_][a-z0-9_]*$")


def _setting_name(name: str) -> str:
    """``query_timeout`` or ``hardcharger.query_timeout`` -> ``query_timeout``."""
    bare = name[len("hardcharger.") :] if name.lower().startswith("hardcharger.") else name
    if not _SETTING_NAME.match(bare):
        raise ValueError(
            f"invalid Hardcharger setting name {name!r}: expected a name such as "
            "'query_timeout' (lower-case letters, digits and underscores)"
        )
    return bare


def _literal(value: SettingValue) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, str):
        return "'" + value.replace("'", "''") + "'"
    raise TypeError(f"a setting's value must be a str, int or bool, not {type(value).__name__}")


def _tag_text(tag: Tag) -> str:
    if isinstance(tag, str):
        return tag
    return json.dumps(obj=dict(tag), separators=(",", ":"), sort_keys=True)


def _scope_keyword(scope: str) -> str:
    keyword = scope.upper()
    if keyword not in ("SESSION", "GLOBAL"):
        raise ValueError(f"scope must be 'session' or 'global', not {scope!r}")
    return keyword


def _pem_text(value: PemSource) -> str:
    if isinstance(value, bytes):
        return value.decode(encoding="ascii")
    text = os.fspath(value)
    if "-----BEGIN" in text:
        return text
    with open(file=text, encoding="ascii") as f:
        return f.read()


class Connection(_warp.Connection):
    """A Warp SQL connection, plus helpers for Hardcharger's session settings and tags.

    Every ``hardcharger.<name>`` setting that ``hardcharger_settings()`` lists can be read and
    set; ``SET`` statements work as well (``cursor.execute(operation="SET hardcharger....")``).
    """

    def _run(self, sql: str) -> None:
        with self.cursor() as cur:
            cur.execute(operation=sql)

    def _scalar(self, sql: str, parameters: Optional[list] = None) -> Any:
        with self.cursor() as cur:
            cur.execute(operation=sql, parameters=parameters)
            row = cur.fetchone()
            return None if row is None else row[0]

    def set_setting(
        self, name: str, value: SettingValue, *, scope: Literal["session", "global"] = "session"
    ) -> None:
        """``SET [SESSION | GLOBAL] hardcharger.<name> = <value>``.

        Session settings (``query_timeout``, ``query_tag``, ...) apply to this connection's
        session; global ones (``max_concurrent_statements``, ...) to the whole server, and only
        an admin may set them.
        """
        bare = _setting_name(name=name)
        self._run(sql=f"SET {_scope_keyword(scope=scope)} hardcharger.{bare} = {_literal(value)}")

    def reset_setting(self, name: str, *, scope: Literal["session", "global"] = "session") -> None:
        """``RESET [SESSION | GLOBAL] hardcharger.<name>``: back to the server's value."""
        bare = _setting_name(name=name)
        self._run(sql=f"RESET {_scope_keyword(scope=scope)} hardcharger.{bare}")

    def get_setting(self, name: str) -> Optional[str]:
        """The value of ``hardcharger.<name>`` that applies to this session, as text.

        Raises:
            KeyError: when the server has no such setting, or it is admin-only and this
                session's user is not an admin.
        """
        bare = _setting_name(name=name)
        # The name is a literal, not a bound parameter: hardcharger-server 0.2.0's
        # hardcharger_settings() returns no rows when a prepared statement runs it a second
        # time with another parameter. _setting_name allows only [a-z0-9_], so this is safe.
        with self.cursor() as cur:
            cur.execute(
                operation="SELECT CAST(value AS VARCHAR) FROM hardcharger_settings() "
                f"WHERE name = 'hardcharger.{bare}'"
            )
            rows = cur.fetchall()
        if not rows:
            raise KeyError(f"no Hardcharger setting hardcharger.{bare} visible to this session")
        return rows[0][0]

    def settings(self):
        """Every setting visible to this session (``hardcharger_settings()``), as an Arrow table."""
        with self.cursor() as cur:
            cur.execute(operation="SELECT * FROM hardcharger_settings() ORDER BY name")
            return cur.fetch_arrow_table()

    def set_query_tag(self, tag: Optional[Tag]) -> None:
        """Tag this session's following statements (``hardcharger.query_tag``).

        Hardcharger records the tag, a JSON object, with each statement in its instrumentation
        tables (``--enable-instrumentation``). ``None`` removes it.
        """
        if tag is None:
            self.reset_setting(name="query_tag")
        else:
            self.set_setting(name="query_tag", value=_tag_text(tag=tag))

    def set_session_tag(self, tag: Optional[Tag]) -> None:
        """Tag this session (``hardcharger.session_tag``), a JSON object; ``None`` removes it."""
        if tag is None:
            self.reset_setting(name="session_tag")
        else:
            self.set_setting(name="session_tag", value=_tag_text(tag=tag))

    def current_user(self) -> str:
        """The signed-in user (``HARDCHARGER_USER()``)."""
        return self._scalar(sql="SELECT HARDCHARGER_USER()")

    def current_session(self) -> str:
        """This connection's server session id (``HARDCHARGER_CURRENT_SESSION()``)."""
        return self._scalar(sql="SELECT CAST(HARDCHARGER_CURRENT_SESSION() AS VARCHAR)")

    def server_version(self) -> str:
        """The server's version, e.g. ``v0.2.0`` (``HARDCHARGER_VERSION()``)."""
        return self._scalar(sql="SELECT HARDCHARGER_VERSION()")


def connect(
    uri: Optional[str] = None,
    *,
    profile: Optional[str] = None,
    username: Optional[str] = None,
    password: Optional[str] = None,
    access_token: Optional[str] = None,
    auth_type: Literal["password", "external"] = "password",
    oauth_url: Optional[str] = None,
    oauth_port: int = DEFAULT_OAUTH_PORT,
    oauth_timeout: float = DEFAULT_TIMEOUT,
    open_browser: BrowserOpener = True,
    session_tag: Optional[Tag] = None,
    query_tag: Optional[Tag] = None,
    settings: Optional[Mapping[str, SettingValue]] = None,
    tls_skip_verify: bool = False,
    tls_root_certs: Optional[PemSource] = None,
    tls_client_cert: Optional[PemSource] = None,
    tls_client_key: Optional[PemSource] = None,
    catalog: Optional[str] = None,
    db_schema: Optional[str] = None,
    db_kwargs: Optional[dict[str, str]] = None,
    conn_kwargs: Optional[dict[str, str]] = None,
    autocommit: bool = True,
) -> Connection:
    """Connect to a Hardcharger server (DB-API 2.0).

    Args:
        uri: ``hardcharger://host[:port][?tls=false]``: HTTPS unless ``?tls=false``, port 31337
            unless given. ``warp://``, ``https://`` and ``http://`` URIs work too, as does
            ``profile://<name>``.
        profile: Name (or absolute path) of an ADBC connection profile.
        username: User name for password sign-in.
        password: Password for password sign-in.
        access_token: A Hardcharger access token obtained elsewhere.
        auth_type: ``"password"`` (default; also for ``access_token``) or ``"external"``: sign
            in through the server's browser sign-in (``hardcharger-server --oauth-client-id``).
        oauth_url: Base URL of the browser sign-in server (``--oauth-base-url``). Default:
            ``https://HOST:oauth_port``, or ``http://`` when the URI says ``?tls=false``.
        oauth_port: Port of the browser sign-in server (default 31339).
        oauth_timeout: Seconds to wait for the browser sign-in.
        open_browser: ``True`` opens the system browser for the sign-in, ``False`` prints the
            URL, and a callable is called with it.
        session_tag: A JSON object tagging this session (``hardcharger.session_tag``).
        query_tag: A JSON object tagging this session's statements (``hardcharger.query_tag``).
        settings: More ``hardcharger.<name>`` session settings to apply, e.g.
            ``{"query_timeout": 60}``.
        tls_skip_verify: Do not verify the server's certificate (development only).
        tls_root_certs: CA certificates to trust, as PEM text or a file path.
        tls_client_cert: Client certificate chain for mutual TLS (PEM text or path).
        tls_client_key: The client certificate's private key (PEM text or path).
        catalog: Catalog to make current for the session.
        db_schema: Schema to make current for the session.
        db_kwargs: Extra ADBC database options (``adbc.warp.*``, telemetry).
        conn_kwargs: Extra ADBC connection options.
        autocommit: Enable autocommit (default True).

    Returns:
        A DB-API 2.0 :class:`Connection`.

    Raises:
        HardchargerOAuthError: when the browser sign-in fails.
        ValueError: for an invalid URI, ``auth_type`` or setting name.
    """
    warp_uri = None
    if uri is not None and not uri.startswith("profile://"):
        warp_uri = to_warp_uri(uri=uri)
    elif uri is not None:
        warp_uri = uri

    db_kwargs = dict(db_kwargs or {})
    if auth_type == "external":
        if username is not None or password is not None or access_token is not None:
            raise ValueError(
                "auth_type='external' signs in through the browser: drop username, "
                "password and access_token"
            )
        if oauth_url is None and (warp_uri is None or warp_uri.startswith("profile://")):
            raise ValueError(
                "auth_type='external' with a connection profile needs an explicit uri or oauth_url"
            )
        result = get_oauth_token(
            oauth_url=oauth_url,
            host=None if warp_uri is None else host_of(warp_uri=warp_uri),
            port=oauth_port,
            tls=warp_uri is None or uses_tls(warp_uri=warp_uri),
            tls_skip_verify=tls_skip_verify,
            tls_root_certs=None if tls_root_certs is None else _pem_text(value=tls_root_certs),
            timeout=oauth_timeout,
            open_browser=open_browser,
        )
        db_kwargs[DatabaseOptions.SUBJECT_TOKEN.value] = result.token
    elif auth_type != "password":
        raise ValueError(f"invalid auth_type {auth_type!r}: expected 'password' or 'external'")

    # Validate before connecting, so a typo fails fast and leaves no session behind.
    session_settings: list[tuple[str, SettingValue]] = [
        (_setting_name(name=name), value) for name, value in (settings or {}).items()
    ]
    for value in (session_tag, query_tag):
        if value is not None and not isinstance(value, (str, Mapping)):
            raise TypeError(f"a tag must be a mapping or JSON text, not {type(value).__name__}")

    conn = _warp.connect(
        uri=warp_uri,
        profile=profile,
        username=username,
        password=password,
        access_token=access_token,
        tls_skip_verify=tls_skip_verify,
        tls_root_certs=tls_root_certs,
        tls_client_cert=tls_client_cert,
        tls_client_key=tls_client_key,
        catalog=catalog,
        db_schema=db_schema,
        db_kwargs=db_kwargs,
        conn_kwargs=conn_kwargs,
        autocommit=autocommit,
    )
    # The Warp SQL driver builds the connection; this class adds methods and no state.
    conn.__class__ = Connection
    try:
        if session_tag is not None:
            conn.set_session_tag(tag=session_tag)
        if query_tag is not None:
            conn.set_query_tag(tag=query_tag)
        for name, value in session_settings:
            conn.set_setting(name=name, value=value)
    except BaseException:
        conn.close()
        raise
    return conn
