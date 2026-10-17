"""Unit tests (no server): how connect() maps keyword arguments to driver options, the helpers'
value handling, the manifest, and the driver library through the C API."""

from __future__ import annotations

from unittest.mock import patch

import adbc_driver_manager
import pytest

from adbc_driver_hardcharger import DEFAULT_OAUTH_PORT, DEFAULT_PORT, manifest
from adbc_driver_hardcharger import dbapi as hardcharger
from adbc_driver_hardcharger._driver import _driver_path

_PATCH_DB = "adbc_driver_hardcharger.dbapi.adbc_driver_manager.AdbcDatabase"
_PATCH_CONN = "adbc_driver_hardcharger.dbapi.adbc_driver_manager.AdbcConnection"
_PATCH_CLS = "adbc_driver_hardcharger.dbapi.Connection"
PEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"


def _db_kwargs(mock_db) -> dict:
    kwargs = dict(mock_db.call_args.kwargs)
    kwargs.pop("driver", None)
    return kwargs


@patch(_PATCH_CLS)
@patch(_PATCH_CONN)
@patch(_PATCH_DB)
class TestConnectOptions:
    def test_password(self, mock_db, mock_conn, mock_cls):
        hardcharger.connect(uri="hardcharger://h?tls=false", username="u", password="p:@")
        # The URI goes to the driver as it is: the library reads hardcharger://. Nothing else
        # (partitions, compression, ...) is forced on it.
        assert _db_kwargs(mock_db=mock_db) == {
            "uri": "hardcharger://h?tls=false",
            "username": "u",
            "password": "p:@",
        }
        assert mock_db.call_args.kwargs["driver"] == _driver_path()

    def test_hardcharger_options(self, mock_db, mock_conn, mock_cls, tmp_path):
        ca = tmp_path / "ca.pem"
        ca.write_text(data=PEM)
        hardcharger.connect(
            uri="hardcharger://h",
            auth_type="external",
            oauth_url="https://sso.example.com",
            oauth_port=4000,
            oauth_timeout=2.5,
            open_browser=False,
            session_tag={"app": "etl"},
            query_tag='{"job": "x"}',
            settings={"query_timeout": 60, "hardcharger.bypass_queue": True},
            tls_root_certs=ca,
            catalog="memory",
        )
        assert _db_kwargs(mock_db=mock_db) == {
            "uri": "hardcharger://h",
            "adbc.hardcharger.auth_type": "external",
            "adbc.hardcharger.oauth.url": "https://sso.example.com",
            "adbc.hardcharger.oauth.port": "4000",
            "adbc.hardcharger.oauth.timeout_seconds": "2.5",
            "adbc.hardcharger.oauth.browser": "none",
            "adbc.hardcharger.session_tag": '{"app":"etl"}',
            "adbc.hardcharger.query_tag": '{"job": "x"}',
            "adbc.hardcharger.setting.query_timeout": "60",
            "adbc.hardcharger.setting.bypass_queue": "true",
            "adbc.warp.tls.root_certs": PEM,
        }
        assert mock_conn.call_args.kwargs["adbc.connection.catalog"] == "memory"

    def test_browser_command(self, mock_db, mock_conn, mock_cls):
        hardcharger.connect(uri="hardcharger://h", auth_type="external", open_browser="firefox")
        assert _db_kwargs(mock_db=mock_db)["adbc.hardcharger.oauth.browser"] == "firefox"

    def test_profile_uri(self, mock_db, mock_conn, mock_cls):
        hardcharger.connect(uri="profile://prod")
        assert _db_kwargs(mock_db=mock_db) == {"profile": "prod"}

    def test_invalid_auth_type(self, mock_db, mock_conn, mock_cls):
        with pytest.raises(expected_exception=ValueError, match="invalid auth_type 'oauth'"):
            hardcharger.connect(uri="hardcharger://h", auth_type="oauth")
        mock_db.assert_not_called()

    def test_external_auth_refuses_a_password(self, mock_db, mock_conn, mock_cls):
        with pytest.raises(expected_exception=ValueError, match="drop username, password"):
            hardcharger.connect(uri="hardcharger://h", auth_type="external", password="p")
        mock_db.assert_not_called()

    def test_bad_tag_type(self, mock_db, mock_conn, mock_cls):
        with pytest.raises(expected_exception=TypeError, match="a tag must be a mapping"):
            hardcharger.connect(uri="hardcharger://h", query_tag=42)
        mock_db.assert_not_called()

    def test_needs_a_uri_or_profile(self, mock_db, mock_conn, mock_cls):
        with pytest.raises(expected_exception=ValueError, match="at least one of 'uri' or"):
            hardcharger.connect()


def test_value_helpers():
    assert hardcharger._tag_text(tag={"b": 2, "a": "x"}) == '{"a":"x","b":2}'
    assert hardcharger._tag_text(tag=None) == ""
    assert hardcharger._setting_text(value=True) == "true"
    assert hardcharger._setting_text(value=30) == "30"
    with pytest.raises(expected_exception=TypeError, match="str, int or bool, not float"):
        hardcharger._setting_text(value=1.5)
    assert DEFAULT_PORT == 31337 and DEFAULT_OAUTH_PORT == 31339


class TestManifest:
    def test_render(self):
        text = manifest.render(library="/opt/lib/libadbc_driver_hardcharger.so")
        assert "name = 'Hardcharger'" in text
        assert "entrypoint = 'AdbcDriverHardchargerInit'" in text
        assert f'{manifest._platform_key()} = "/opt/lib/libadbc_driver_hardcharger.so"' in text

    def test_install_points_at_the_bundled_library(self, tmp_path):
        path = manifest.install(directory=tmp_path)
        assert path == tmp_path / "hardcharger.toml"
        assert _driver_path().replace("\\", "\\\\") in path.read_text()


class TestLibrary:
    """The driver library through the ADBC driver manager's C API, with no server."""

    def test_keeps_the_hardcharger_uri_and_options(self):
        with adbc_driver_manager.AdbcDatabase(
            driver=_driver_path(),
            uri="hardcharger://127.0.0.1?tls=false",
            username="u",
            password="p",
            **{"adbc.hardcharger.query_tag": '{"a":1}', "adbc.hardcharger.oauth.port": "4000"},
        ) as db:
            assert db.get_option(key="uri") == "hardcharger://127.0.0.1?tls=false"
            assert db.get_option(key="adbc.hardcharger.query_tag") == '{"a":1}'
            assert db.get_option(key="adbc.hardcharger.oauth.port") == "4000"

    def test_plus_http_is_refused(self):
        with pytest.raises(expected_exception=adbc_driver_manager.Error) as error:
            adbc_driver_manager.AdbcDatabase(
                driver=_driver_path(), uri="hardcharger+http://127.0.0.1:31337"
            )
        assert error.value.status_code == adbc_driver_manager.AdbcStatusCode.INVALID_ARGUMENT
        assert "hardcharger://host:port?tls=false for plain HTTP" in str(error.value)

    @pytest.mark.parametrize(
        argnames=("key", "value", "text"),
        argvalues=[
            ("adbc.hardcharger.auth_type", "oauth", "expected password or external"),
            ("adbc.hardcharger.setting.bad name", "1", "invalid Hardcharger setting name"),
            ("adbc.hardcharger.nope", "1", "unknown option"),
        ],
    )
    def test_bad_options_are_refused(self, key, value, text):
        with pytest.raises(expected_exception=adbc_driver_manager.Error, match=text):
            adbc_driver_manager.AdbcDatabase(
                driver=_driver_path(), uri="hardcharger://h", **{key: value}
            )
