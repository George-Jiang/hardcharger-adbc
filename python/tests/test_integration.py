"""Integration tests against real hardcharger-server processes (see conftest.py)."""

from __future__ import annotations

import pathlib
import subprocess
import sys
import time

import adbc_driver_manager
import adbc_driver_manager.dbapi
import pytest

from adbc_driver_hardcharger import dbapi as hardcharger
from adbc_driver_hardcharger import manifest
from conftest import USERNAME, free_port, make_certificates, start_server, stop_server

pytestmark = pytest.mark.integration


def warp_code(error: Exception) -> str | None:
    """The Warp SQL error code the driver put in the error's details."""
    for key, value in getattr(error, "details", None) or []:
        if (key.decode() if isinstance(key, bytes) else key) == "warp.code":
            return value.decode() if isinstance(value, bytes) else value
    return None


def eventually(conn, sql: str, predicate, timeout: float = 30):
    """Instrumentation is written asynchronously: poll until ``predicate(value)``."""
    deadline = time.monotonic() + timeout
    value = None
    while time.monotonic() < deadline:
        with conn.cursor() as cur:
            cur.execute(operation=sql)
            row = cur.fetchone()
        value = None if row is None else row[0]
        if predicate(value):
            return value
        time.sleep(0.1)
    raise AssertionError(f"{sql} -> {value!r}")


# --- connecting ----------------------------------------------------------------------------


def test_select_through_a_hardcharger_uri(conn):
    with conn.cursor() as cur:
        cur.execute(operation="SELECT 42 AS answer, ? AS greeting", parameters=["hello"])
        assert cur.fetch_arrow_table().to_pylist() == [{"answer": 42, "greeting": "hello"}]


def test_the_server_reports_itself_as_hardcharger(conn):
    info = conn.adbc_get_info()
    assert info["vendor_name"] == "Hardcharger"
    assert info["driver_name"] == "ADBC Hardcharger Driver - Go"


def test_connection_helpers(conn):
    assert isinstance(conn, hardcharger.Connection)
    assert conn.current_user() == USERNAME
    assert conn.server_version().startswith("v")
    assert len(conn.current_session()) == 36


def test_wrong_password_is_refused(hc_server):
    with pytest.raises(expected_exception=adbc_driver_manager.ProgrammingError) as error:
        hardcharger.connect(uri=hc_server.uri, username=USERNAME, password="not-the-password")
    assert warp_code(error=error.value) == "invalid_credentials"
    assert "the user name or password is not correct" in str(error.value)


def test_right_password_twin(hc_server):
    with hardcharger.connect(
        uri=hc_server.uri, username=USERNAME, password=hc_server.password
    ) as conn:
        assert conn.current_user() == USERNAME


def test_warp_uri_works_too(hc_server):
    uri = f"warp://{hc_server.host}:{hc_server.port}?tls=false"
    with hardcharger.connect(uri=uri, username=USERNAME, password=hc_server.password) as conn:
        assert isinstance(conn, hardcharger.Connection)
        assert conn.current_user() == USERNAME


def test_plain_http_spelling_must_be_tls_false(hc_server):
    with pytest.raises(expected_exception=adbc_driver_manager.Error) as error:
        hardcharger.connect(
            uri=f"hardcharger+http://127.0.0.1:{hc_server.port}",
            username=USERNAME,
            password=hc_server.password,
        )
    assert error.value.status_code == adbc_driver_manager.AdbcStatusCode.INVALID_ARGUMENT
    assert "hardcharger://host:port?tls=false for plain HTTP" in str(error.value)


def test_ddl_dml_and_bulk_ingest(conn):
    import pyarrow as pa

    with conn.cursor() as cur:
        cur.execute(operation="CREATE OR REPLACE TABLE trips (id INTEGER, city VARCHAR)")
        assert cur.description is None
        rows = cur.adbc_ingest(
            table_name="trips",
            data=pa.table({"id": [1, 2], "city": ["Boston", "Denver"]}),
            mode="append",
        )
        assert rows == 2
        cur.execute(operation="SELECT city FROM trips ORDER BY id")
        assert cur.fetchall() == [("Boston",), ("Denver",)]


# --- TLS -----------------------------------------------------------------------------------


@pytest.fixture(scope="module")
def tls_server(tmp_path_factory):
    workdir = tmp_path_factory.mktemp("tls")
    certs = make_certificates(out_dir=workdir)
    server = start_server(
        workdir=workdir,
        extra_args=["--tls-cert", str(certs["cert"]), "--tls-key", str(certs["key"])],
    )
    server.certs = certs
    yield server
    stop_server(server=server)


def test_tls_is_the_default_and_verifies(tls_server):
    assert tls_server.uri == f"hardcharger://127.0.0.1:{tls_server.port}"
    with hardcharger.connect(
        uri=tls_server.uri,
        username=USERNAME,
        password=tls_server.password,
        tls_root_certs=tls_server.certs["ca"],
    ) as conn:
        assert conn.current_user() == USERNAME


def test_tls_refuses_an_untrusted_certificate(tls_server):
    with pytest.raises(expected_exception=adbc_driver_manager.Error) as error:
        hardcharger.connect(uri=tls_server.uri, username=USERNAME, password=tls_server.password)
    assert "certificate" in str(error.value)


# --- settings ------------------------------------------------------------------------------


def test_session_setting_set_get_reset(conn):
    assert conn.get_setting(name="query_timeout") == "0"
    conn.set_setting(name="query_timeout", value=30)
    assert conn.get_setting(name="hardcharger.query_timeout") == "30"
    conn.reset_setting(name="query_timeout")
    assert conn.get_setting(name="query_timeout") == "0"


def test_invalid_setting_value_is_refused(conn):
    with pytest.raises(expected_exception=adbc_driver_manager.ProgrammingError) as error:
        conn.set_setting(name="query_timeout", value="soon")
    assert "Invalid value for query_timeout: 'soon' is not a whole number" in str(error.value)
    assert warp_code(error=error.value) == "sql_error"


def test_valid_setting_value_twin(conn):
    conn.set_setting(name="query_timeout", value="45")
    assert conn.get_setting(name="query_timeout") == "45"


def test_unknown_setting_is_refused(conn):
    with pytest.raises(
        expected_exception=adbc_driver_manager.ProgrammingError,
        match="Unknown Hardcharger setting: hardcharger.no_such_setting",
    ):
        conn.set_setting(name="no_such_setting", value=1)
    with pytest.raises(expected_exception=KeyError, match="hardcharger.no_such_setting"):
        conn.get_setting(name="no_such_setting")


def test_global_setting_needs_global_scope(conn):
    with pytest.raises(
        expected_exception=adbc_driver_manager.ProgrammingError,
        match="hardcharger.graceful_shutdown can only be set with SET GLOBAL",
    ):
        conn.set_setting(name="graceful_shutdown", value=False)


def test_global_setting_with_global_scope_twin(conn):
    try:
        conn.set_setting(name="graceful_shutdown", value=False, scope="global")
        assert conn.get_setting(name="graceful_shutdown") == "false"
    finally:
        conn.reset_setting(name="graceful_shutdown", scope="global")
    assert conn.get_setting(name="graceful_shutdown") == "true"


def test_settings_table(conn):
    table = conn.settings()
    names = table.column("name").to_pylist()  # column() takes no keywords
    assert "hardcharger.query_tag" in names and "hardcharger.query_timeout" in names


def test_connect_applies_settings(hc_server):
    with hardcharger.connect(
        uri=hc_server.uri,
        username=USERNAME,
        password=hc_server.password,
        settings={"query_timeout": 12, "bypass_queue": True},
    ) as conn:
        assert conn.get_setting(name="query_timeout") == "12"
        assert conn.get_setting(name="bypass_queue") == "true"


def test_connect_with_a_bad_setting_fails_and_leaves_no_session(hc_server):
    def sessions() -> int:
        with hardcharger.connect(
            uri=hc_server.uri, username=USERNAME, password=hc_server.password
        ) as probe:
            with probe.cursor() as cur:
                cur.execute(operation="SELECT count(*) FROM hardcharger_sessions()")
                return cur.fetchone()[0]

    before = sessions()
    with pytest.raises(
        expected_exception=adbc_driver_manager.ProgrammingError,
        match="'soon' is not a whole number",
    ):
        hardcharger.connect(
            uri=hc_server.uri,
            username=USERNAME,
            password=hc_server.password,
            settings={"query_timeout": "soon"},
        )
    # The failed connection's session is closed, not left to time out.
    assert (
        eventually(
            conn=_admin(hc_server),
            sql="SELECT count(*) FROM hardcharger_sessions()",
            predicate=lambda n: n <= before,
            timeout=10,
        )
        <= before
    )


def _admin(server):
    return hardcharger.connect(uri=server.uri, username=USERNAME, password=server.password)


# --- query and session tags ----------------------------------------------------------------


@pytest.fixture(scope="module")
def instrumented_server(tmp_path_factory):
    server = start_server(
        workdir=tmp_path_factory.mktemp("instr"), extra_args=["--enable-instrumentation"]
    )
    yield server
    stop_server(server=server)


def test_query_and_session_tags_are_recorded(instrumented_server):
    server = instrumented_server
    with hardcharger.connect(
        uri=server.uri,
        username=USERNAME,
        password=server.password,
        session_tag={"app": "hardcharger-adbc-tests"},
        query_tag={"job": "nightly-load"},
    ) as conn:
        with conn.cursor() as cur:
            cur.execute(operation="SELECT 1 AS tagged_by_connect")
            cur.fetchall()
        session = conn.current_session()
        tag = eventually(
            conn=conn,
            predicate=lambda v: v is not None,
            sql=(
                "SELECT query_tag->>'job' FROM _hardcharger_instr.sql_statements "
                "WHERE sql_text LIKE '%tagged_by_connect%' AND sql_text NOT LIKE '%sql_statements%'"
            ),
        )
        assert tag == "nightly-load"
        app = eventually(
            conn=conn,
            predicate=lambda v: v is not None,
            sql=(
                "SELECT session_tag->>'app' FROM _hardcharger_instr.sessions "
                f"WHERE session_id = '{session}'"
            ),
        )
        assert app == "hardcharger-adbc-tests"

        conn.set_query_tag(tag={"job": "second"})
        with conn.cursor() as cur:
            cur.execute(operation="SELECT 2 AS tagged_second")
            cur.fetchall()
        assert (
            eventually(
                conn=conn,
                predicate=lambda v: v is not None,
                sql=(
                    "SELECT query_tag->>'job' FROM _hardcharger_instr.sql_statements "
                    "WHERE sql_text LIKE '%tagged_second%' AND sql_text NOT LIKE '%sql_statements%'"
                ),
            )
            == "second"
        )

        conn.set_query_tag(tag=None)
        assert conn.get_setting(name="query_tag") == ""
        with conn.cursor() as cur:
            cur.execute(operation="SELECT 3 AS untagged_third")
            cur.fetchall()
        assert (
            eventually(
                conn=conn,
                predicate=lambda v: v is not None,
                sql=(
                    "SELECT CAST(query_tag IS NULL AS VARCHAR) "
                    "FROM _hardcharger_instr.sql_statements WHERE sql_text LIKE '%untagged_third%' "
                    "AND sql_text NOT LIKE '%sql_statements%'"
                ),
            )
            == "true"
        )


def test_a_tag_that_is_not_json_is_refused(conn):
    with pytest.raises(
        expected_exception=adbc_driver_manager.ProgrammingError, match="Invalid JSON for query_tag"
    ):
        conn.set_query_tag(tag="not json")


def test_a_json_text_tag_twin(conn):
    conn.set_query_tag(tag='{"source": "text"}')
    assert conn.get_setting(name="query_tag") == '{"source": "text"}'


# --- browser sign-in -----------------------------------------------------------------------

AUDIENCE = "hardcharger-adbc-test"
CLIENT_ID = "hardcharger-adbc-client"
CLIENT_SECRET = "hardcharger-adbc-secret"


class IdP:
    """The mock identity provider, running in its own process (see mock_idp.py)."""

    email = "analyst@gizmodata.com"
    valid_code = "code-ok"

    def __init__(self, issuer: str, process: subprocess.Popen):
        self.issuer = issuer
        self.process = process


@pytest.fixture(scope="module")
def idp():
    process = subprocess.Popen(
        args=[
            sys.executable,
            str(pathlib.Path(__file__).with_name("mock_idp.py")),
            AUDIENCE,
            CLIENT_ID,
            CLIENT_SECRET,
        ],
        stdout=subprocess.PIPE,
        text=True,
    )
    issuer = process.stdout.readline().strip()
    if not issuer.startswith("http://127.0.0.1:"):
        process.kill()
        pytest.fail(reason=f"the mock identity provider did not start: {issuer!r}")
    yield IdP(issuer=issuer, process=process)
    process.kill()
    process.wait()


@pytest.fixture(scope="module")
def oauth_server(tmp_path_factory, idp):
    oauth_port = free_port()
    server = start_server(
        workdir=tmp_path_factory.mktemp("oauth"),
        oauth_port=oauth_port,
        extra_args=[
            "--token-allowed-issuer",
            idp.issuer,
            "--token-allowed-audience",
            AUDIENCE,
            "--token-default-role",
            "admin",
            "--token-authorized-emails",
            "*@gizmodata.com",
            "--oauth-client-id",
            CLIENT_ID,
            "--oauth-client-secret",
            CLIENT_SECRET,
            "--oauth-port",
            str(oauth_port),
        ],
    )
    yield server
    stop_server(server=server)


FAKE_BROWSER = f"{sys.executable} {pathlib.Path(__file__).with_name('fake_browser.py')}"


def browser(code: str) -> str:
    """The fake browser command, handing ``code`` to the server's callback."""
    return f"{FAKE_BROWSER} {code}"


def test_browser_sign_in(oauth_server, idp):
    with hardcharger.connect(
        uri=oauth_server.uri,
        auth_type="external",
        oauth_port=oauth_server.oauth_port,
        open_browser=browser(code="code-ok"),
        oauth_timeout=30,
    ) as conn:
        assert conn.current_user() == idp.email
        with conn.cursor() as cur:
            cur.execute(operation="SELECT 1 AS one")
            assert cur.fetchall() == [(1,)]


def test_browser_sign_in_with_a_rejected_code(oauth_server, idp):
    with pytest.raises(expected_exception=adbc_driver_manager.Error) as error:
        hardcharger.connect(
            uri=oauth_server.uri,
            auth_type="external",
            oauth_port=oauth_server.oauth_port,
            open_browser=browser(code="code-bad"),
            oauth_timeout=30,
        )
    assert error.value.status_code == adbc_driver_manager.AdbcStatusCode.UNAUTHENTICATED
    assert "browser sign-in: the sign-in failed" in str(error.value)
    assert "invalid_grant" in str(error.value)


def test_browser_sign_in_with_an_explicit_oauth_url(oauth_server, idp):
    with hardcharger.connect(
        uri=oauth_server.uri,
        auth_type="external",
        oauth_url=f"http://127.0.0.1:{oauth_server.oauth_port}",
        open_browser=browser(code="code-ok"),
        oauth_timeout=30,
    ) as conn:
        assert conn.current_user() == idp.email


# --- the ADBC driver manifest: other languages load the same library by name ----------------


def test_manifest_loads_the_driver_by_name_with_a_hardcharger_uri(hc_server, tmp_path, monkeypatch):
    manifest.install(directory=tmp_path)
    monkeypatch.setenv(name="ADBC_DRIVER_PATH", value=str(tmp_path))
    with adbc_driver_manager.dbapi.connect(
        driver="hardcharger",
        db_kwargs={
            "uri": f"hardcharger://127.0.0.1:{hc_server.port}?tls=false",
            "username": USERNAME,
            "password": hc_server.password,
            "adbc.hardcharger.query_tag": '{"via": "manifest"}',
        },
        autocommit=True,
    ) as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT HARDCHARGER_USER()")
            assert cur.fetchone() == (USERNAME,)
        assert conn.adbc_get_info()["driver_name"] == "ADBC Hardcharger Driver - Go"
        assert conn.adbc_connection.get_option(key="adbc.hardcharger.query_tag") == (
            '{"via": "manifest"}'
        )


def test_manifest_refuses_a_bad_scheme(hc_server, tmp_path, monkeypatch):
    manifest.install(directory=tmp_path)
    monkeypatch.setenv(name="ADBC_DRIVER_PATH", value=str(tmp_path))
    with pytest.raises(
        expected_exception=adbc_driver_manager.Error, match=r"\?tls=false for plain HTTP"
    ):
        adbc_driver_manager.dbapi.connect(
            driver="hardcharger",
            db_kwargs={
                "uri": f"hardcharger+http://127.0.0.1:{hc_server.port}",
                "username": USERNAME,
                "password": hc_server.password,
            },
        )


def test_a_prepared_settings_query_re_executes_with_new_parameters(conn):
    """hardcharger-server before 3fbd36b answered no rows the second time: its table functions
    built their rows at bind time. get_setting() relies on this."""
    with conn.cursor() as cur:
        for name, want in (
            ("hardcharger.query_timeout", "0"),
            ("hardcharger.graceful_shutdown", "true"),
            ("hardcharger.bypass_queue", "false"),
        ):
            cur.execute(
                operation="SELECT CAST(value AS VARCHAR) FROM hardcharger_settings() "
                "WHERE name = ?",
                parameters=[name],
            )
            assert cur.fetchall() == [(want,)], name
