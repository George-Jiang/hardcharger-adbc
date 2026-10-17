"""Unit tests (no server): URI translation, setting and tag helpers, connect() argument
handling, the manifest, and the browser sign-in client against a fake sign-in server."""

from __future__ import annotations

import http.server
import json
import threading
from unittest.mock import MagicMock, patch

import pytest

from adbc_driver_hardcharger import DEFAULT_OAUTH_PORT, DEFAULT_PORT, manifest, to_warp_uri
from adbc_driver_hardcharger import dbapi as hardcharger
from adbc_driver_hardcharger._oauth import HardchargerOAuthError, get_oauth_token
from adbc_driver_hardcharger._uri import uses_tls

_PATCH_WARP_CONNECT = "adbc_driver_hardcharger.dbapi._warp.connect"


class TestUri:
    @pytest.mark.parametrize(
        argnames=("uri", "expected"),
        argvalues=[
            ("hardcharger://db.example.com", "warp://db.example.com:31337"),
            ("hardcharger://db.example.com:443", "warp://db.example.com:443"),
            ("hardcharger://127.0.0.1?tls=false", "warp://127.0.0.1:31337?tls=false"),
            ("hardcharger://127.0.0.1:4000?tls=false", "warp://127.0.0.1:4000?tls=false"),
            ("hardcharger://h/custom/base", "warp://h:31337/custom/base"),
            ("HARDCHARGER://h", "warp://h:31337"),
            ("hardcharger://[::1]", "warp://[::1]:31337"),
            ("hardcharger://[::1]:5000?tls=false", "warp://[::1]:5000?tls=false"),
            # The Warp SQL driver's own spellings pass through untouched.
            ("warp://h:1?tls=false", "warp://h:1?tls=false"),
            ("https://h/warp", "https://h/warp"),
            ("http://h:8080", "http://h:8080"),
        ],
    )
    def test_translation(self, uri, expected):
        assert to_warp_uri(uri=uri) == expected

    def test_default_ports(self):
        assert DEFAULT_PORT == 31337 and DEFAULT_OAUTH_PORT == 31339

    def test_plus_http_scheme_is_refused_with_the_spelling_to_use(self):
        with pytest.raises(expected_exception=ValueError) as error:
            to_warp_uri(uri="hardcharger+http://127.0.0.1:31337")
        assert "hardcharger://host:port?tls=false for plain HTTP" in str(error.value)

    def test_plain_http_spelling_twin(self):
        translated = to_warp_uri(uri="hardcharger://127.0.0.1:31337?tls=false")
        assert translated == "warp://127.0.0.1:31337?tls=false"
        assert not uses_tls(warp_uri=translated)

    def test_no_host_is_refused(self):
        with pytest.raises(expected_exception=ValueError, match="no host"):
            to_warp_uri(uri="hardcharger:///base")

    def test_bad_port_is_refused(self):
        with pytest.raises(expected_exception=ValueError, match="Port could not be cast"):
            to_warp_uri(uri="hardcharger://h:notaport")

    def test_empty_uri_is_refused(self):
        with pytest.raises(expected_exception=ValueError, match="a uri is required"):
            to_warp_uri(uri="")

    def test_password_in_an_invalid_uri_is_redacted(self):
        with pytest.raises(expected_exception=ValueError) as error:
            to_warp_uri(uri="hardcharger://joe:secret@h:bad")
        assert "secret" not in str(error.value) and "xxxxx" in str(error.value)

    @pytest.mark.parametrize(
        argnames=("uri", "tls"),
        argvalues=[
            ("warp://h:1", True),
            ("warp://h:1?tls=false", False),
            ("warp://h:1?tls=0", False),
            ("warp://h:1?tls=true", True),
            ("https://h", True),
            ("http://h", False),
        ],
    )
    def test_uses_tls(self, uri, tls):
        assert uses_tls(warp_uri=uri) is tls


class TestSettingHelpers:
    @pytest.mark.parametrize(
        argnames="name", argvalues=["query_timeout", "hardcharger.query_timeout", "x_1"]
    )
    def test_valid_names(self, name):
        assert hardcharger._setting_name(name=name) in ("query_timeout", "x_1")

    @pytest.mark.parametrize(
        argnames="name",
        argvalues=["query_timeout; DROP TABLE t", "Query_Timeout", "", "a.b", "1abc", "a'b"],
    )
    def test_invalid_names_are_refused(self, name):
        with pytest.raises(expected_exception=ValueError, match="invalid Hardcharger setting name"):
            hardcharger._setting_name(name=name)

    @pytest.mark.parametrize(
        argnames=("value", "sql"),
        argvalues=[(True, "true"), (False, "false"), (30, "30"), ("x", "'x'"), ("it's", "'it''s'")],
    )
    def test_literals(self, value, sql):
        assert hardcharger._literal(value) == sql

    def test_other_value_types_are_refused(self):
        with pytest.raises(expected_exception=TypeError, match="str, int or bool, not float"):
            hardcharger._literal(1.5)

    def test_tag_text(self):
        assert hardcharger._tag_text(tag={"b": 2, "a": "x"}) == '{"a":"x","b":2}'
        assert hardcharger._tag_text(tag='{"raw": true}') == '{"raw": true}'

    def test_scope(self):
        assert hardcharger._scope_keyword(scope="global") == "GLOBAL"
        with pytest.raises(expected_exception=ValueError, match="scope must be"):
            hardcharger._scope_keyword(scope="local")


@patch(_PATCH_WARP_CONNECT)
class TestConnect:
    def test_hardcharger_uri_is_translated(self, warp_connect):
        warp_connect.return_value = MagicMock()
        hardcharger.connect(uri="hardcharger://h?tls=false", username="u", password="p:@")
        kwargs = warp_connect.call_args.kwargs
        assert kwargs["uri"] == "warp://h:31337?tls=false"
        assert kwargs["username"] == "u" and kwargs["password"] == "p:@"
        # Nothing Hardcharger-specific is forced on the driver (partitions, compression, ...).
        assert kwargs["db_kwargs"] == {} and kwargs["conn_kwargs"] is None

    def test_profile_uri_passes_through(self, warp_connect):
        warp_connect.return_value = MagicMock()
        hardcharger.connect(uri="profile://prod")
        assert warp_connect.call_args.kwargs["uri"] == "profile://prod"

    def test_invalid_auth_type(self, warp_connect):
        with pytest.raises(expected_exception=ValueError, match="invalid auth_type 'oauth'"):
            hardcharger.connect(uri="hardcharger://h", auth_type="oauth")
        warp_connect.assert_not_called()

    def test_external_auth_refuses_a_password(self, warp_connect):
        with pytest.raises(expected_exception=ValueError, match="drop username, password"):
            hardcharger.connect(uri="hardcharger://h", auth_type="external", password="p")
        warp_connect.assert_not_called()

    def test_external_auth_with_a_profile_needs_a_uri(self, warp_connect):
        with pytest.raises(expected_exception=ValueError, match="needs an explicit uri or oauth"):
            hardcharger.connect(profile="prod", auth_type="external")
        warp_connect.assert_not_called()

    def test_bad_setting_name_fails_before_connecting(self, warp_connect):
        with pytest.raises(expected_exception=ValueError, match="invalid Hardcharger setting"):
            hardcharger.connect(uri="hardcharger://h", settings={"bad name": 1})
        warp_connect.assert_not_called()

    def test_bad_tag_type_fails_before_connecting(self, warp_connect):
        with pytest.raises(expected_exception=TypeError, match="a tag must be a mapping"):
            hardcharger.connect(uri="hardcharger://h", query_tag=42)
        warp_connect.assert_not_called()


class TestManifest:
    def test_render(self):
        text = manifest.render(library="/opt/lib/libadbc_driver_warpsql.so")
        assert "name = 'Hardcharger'" in text
        assert "entrypoint = 'AdbcDriverWarpsqlInit'" in text
        assert f'{manifest._platform_key()} = "/opt/lib/libadbc_driver_warpsql.so"' in text

    def test_windows_paths_are_escaped(self):
        text = manifest.render(library="C:\\py\\libadbc_driver_warpsql.dll")
        assert '"C:\\\\py\\\\libadbc_driver_warpsql.dll"' in text

    def test_install(self, tmp_path):
        path = manifest.install(directory=tmp_path)
        assert path == tmp_path / "hardcharger.toml"
        assert "AdbcDriverWarpsqlInit" in path.read_text()


class FakeSignInServer:
    """Answers /oauth/initiate and then the polls with the answers queued in ``polls``."""

    def __init__(self, polls: list[tuple[int, dict]], initiate: tuple[int, dict] | None = None):
        self.polls = list(polls)
        self.requests: list[str] = []
        fake = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                pass

            def do_GET(self):
                fake.requests.append(self.path)
                if self.path == "/oauth/initiate":
                    status, body = initiate or (
                        200,
                        {"session_uuid": "u-1", "auth_url": "http://idp/authorize?state=s"},
                    )
                else:
                    status, body = fake.polls.pop(0) if fake.polls else (200, {"status": "pending"})
                data = json.dumps(obj=body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def sign_in_server():
    servers: list[FakeSignInServer] = []

    def make(**kwargs) -> FakeSignInServer:
        servers.append(FakeSignInServer(**kwargs))
        return servers[-1]

    yield make
    for server in servers:
        server.stop()


class TestBrowserSignIn:
    def test_completes_after_pending(self, sign_in_server):
        server = sign_in_server(
            polls=[
                (200, {"status": "pending"}),
                (200, {"status": "complete", "token": "id.token.jwt"}),
            ]
        )
        opened: list[str] = []
        result = get_oauth_token(
            oauth_url=server.url, open_browser=opened.append, poll_interval=0.01
        )
        assert result.token == "id.token.jwt" and result.session_uuid == "u-1"
        assert opened == ["http://idp/authorize?state=s"]
        assert server.requests == ["/oauth/initiate", "/oauth/token/u-1", "/oauth/token/u-1"]

    def test_provider_error_is_reported(self, sign_in_server):
        server = sign_in_server(
            polls=[(200, {"status": "error", "error": "invalid_grant: bad code"})]
        )
        with pytest.raises(
            expected_exception=HardchargerOAuthError,
            match="the browser sign-in failed: invalid_grant: bad code",
        ):
            get_oauth_token(oauth_url=server.url, open_browser=lambda url: None, poll_interval=0.01)

    def test_expired_sign_in(self, sign_in_server):
        server = sign_in_server(polls=[(404, {"status": "not_found"})])
        with pytest.raises(
            expected_exception=HardchargerOAuthError, match="expired or was already used"
        ):
            get_oauth_token(oauth_url=server.url, open_browser=lambda url: None, poll_interval=0.01)

    def test_timeout(self, sign_in_server):
        server = sign_in_server(polls=[])
        with pytest.raises(
            expected_exception=HardchargerOAuthError, match="timed out after 0.05 seconds"
        ):
            get_oauth_token(
                oauth_url=server.url,
                open_browser=lambda url: None,
                timeout=0.05,
                poll_interval=0.01,
            )

    def test_sign_in_not_configured(self, sign_in_server):
        server = sign_in_server(polls=[], initiate=(404, {"error": "not_found"}))
        with pytest.raises(
            expected_exception=HardchargerOAuthError, match="answered HTTP 404 to /oauth/initiate"
        ):
            get_oauth_token(oauth_url=server.url, open_browser=lambda url: None)

    def test_unreachable(self):
        with pytest.raises(
            expected_exception=HardchargerOAuthError,
            match="could not reach Hardcharger's sign-in server",
        ):
            get_oauth_token(host="127.0.0.1", port=1, tls=False, open_browser=lambda url: None)

    def test_default_url_follows_the_connection_tls(self, sign_in_server):
        server = sign_in_server(polls=[(200, {"status": "complete", "token": "t"})])
        port = int(server.url.rsplit(":", 1)[1])
        # Plain HTTP only when the connection is plain HTTP: no silent fallback.
        result = get_oauth_token(
            host="127.0.0.1", port=port, tls=False, open_browser=lambda url: None
        )
        assert result.token == "t"
        with pytest.raises(expected_exception=HardchargerOAuthError, match="could not reach"):
            get_oauth_token(host="127.0.0.1", port=port, tls=True, open_browser=lambda url: None)

    def test_without_a_browser_the_url_is_printed(self, sign_in_server, capsys):
        server = sign_in_server(polls=[(200, {"status": "complete", "token": "t"})])
        get_oauth_token(oauth_url=server.url, open_browser=False)
        assert "http://idp/authorize?state=s" in capsys.readouterr().err
