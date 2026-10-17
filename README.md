<p align="center">
  <a href="https://github.com/gizmodata/hardcharger">
    <img src="docs/assets/hardcharger-adbc-banner.svg" alt="Hardcharger ADBC: the Python driver for Hardcharger, on Warp SQL. By GizmoData." width="100%">
  </a>
</p>

<p align="center">
  <a href="https://pypi.org/project/adbc-driver-hardcharger/"><img src="https://img.shields.io/badge/PyPI-adbc--driver--hardcharger-blue.svg?logo=PyPI" alt="PyPI"></a>
  <a href="https://arrow.apache.org/adbc/"><img src="https://img.shields.io/badge/ADBC-1.12-f15a24.svg" alt="ADBC 1.12"></a>
  <a href="https://github.com/gizmodata/warp-sql"><img src="https://img.shields.io/badge/protocol-Warp%20SQL-31a8db.svg" alt="Warp SQL protocol"></a>
  <a href="https://github.com/gizmodata/warp-sql-adbc"><img src="https://img.shields.io/badge/built%20on-adbc--driver--warpsql-31a8db.svg?logo=go&logoColor=white" alt="Built on adbc-driver-warpsql"></a>
  <img src="https://img.shields.io/badge/python-3.10%2B-3776AB.svg?logo=python&logoColor=white" alt="Python 3.10+">
  <a href="https://github.com/gizmodata/hardcharger"><img src="https://img.shields.io/badge/server-Hardcharger-f15a24.svg" alt="Hardcharger"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-brightgreen.svg" alt="Apache 2.0 license"></a>
  <a href="https://gizmodata.com"><img src="https://img.shields.io/badge/by-GizmoData-f15a24.svg" alt="GizmoData"></a>
</p>

<h3 align="center">Connect to Hardcharger from Python, with Arrow all the way.</h3>

---

## 🔌 What is it?

`adbc-driver-hardcharger` is the Python [ADBC](https://arrow.apache.org/adbc/) driver for
[Hardcharger](https://github.com/gizmodata/hardcharger), GizmoData's DuckDB 2.0 server.

It is a thin layer on the generic [Warp SQL ADBC driver](https://github.com/gizmodata/warp-sql-adbc)
(`adbc-driver-warpsql`, a native Go driver), which does all the work: Arrow IPC streaming,
bind parameters, bulk ingest, partitioned results, cancel, retries and TLS. This package adds
what is particular to Hardcharger:

- 🏎️ **`hardcharger://` URIs**, with Hardcharger's port 31337 as the default;
- 🏷️ **query and session tags**, recorded with every statement in Hardcharger's instrumentation;
- ⚙️ **`hardcharger.*` session settings**: read, set and reset them from Python;
- 🌐 **browser sign-in** through the server's identity provider (SSO);
- 🧩 **an ADBC driver manifest**, so `driver = "hardcharger"` works in any ADBC driver manager.

> **Status: in development, unreleased (0.1.0).** Tested against `hardcharger-server` 0.2.0.

| Package | What it is |
|---|---|
| `pip install adbc-driver-hardcharger` | this client driver |
| `pip install hardcharger` | the Hardcharger **server**, started from Python |
| `pip install "hardcharger[adbc]"` | both |

## 🚀 Quick start

```bash
pip install adbc-driver-hardcharger
```

```python
from adbc_driver_hardcharger import dbapi as hardcharger

with hardcharger.connect(uri="hardcharger://localhost?tls=false", username="hardcharger", password="change-me") as conn:
    with conn.cursor() as cur:
        cur.execute(operation="SELECT 42 AS answer, ? AS greeting", parameters=["hello"])
        print(cur.fetch_arrow_table())
```

The connection and cursor are the Warp SQL driver's DB-API 2.0 objects: `fetch_arrow_table()`,
`fetchall()`, `adbc_ingest()` for bulk loads, `adbc_get_objects()` and the rest.

## 🔗 URIs

| URI | Meaning |
|---|---|
| `hardcharger://host` | HTTPS to `host:31337` |
| `hardcharger://host:port` | HTTPS to another port |
| `hardcharger://host?tls=false` | Plain HTTP (development, loopback) |
| `hardcharger://host:port/custom/prefix` | The server mounts Warp SQL under `/custom/prefix` |
| `warp://…`, `https://…`, `http://…` | The Warp SQL driver's own spellings, passed through as they are |

`hardcharger://` means exactly what Warp SQL's `warp://` means, so TLS is on unless the URI
says `?tls=false`. There is deliberately no `hardcharger+http://`: one spelling for plain HTTP
(`?tls=false`) keeps URIs comparable across Python, JDBC and the `hardcharger` command-line
client, and the driver refuses `hardcharger+http://` with a message naming it.

TLS options are the Warp SQL driver's: `tls_root_certs=` (PEM text or a file) to trust a
private CA, `tls_client_cert=`/`tls_client_key=` for mutual TLS, `tls_skip_verify=True` for
development only.

## 🏷️ Query and session tags

Tags are JSON objects Hardcharger records with every statement and session in its
instrumentation tables (`hardcharger-server --enable-instrumentation`), for cost attribution
and tracing:

```python
with hardcharger.connect(uri="hardcharger://localhost?tls=false", username="hardcharger", password="change-me",
                         session_tag={"app": "nightly-etl"}, query_tag={"job": "load-orders"}) as conn:
    with conn.cursor() as cur:
        cur.execute(operation="SELECT 1 AS tagged")
        cur.fetchall()
    conn.set_query_tag(tag={"job": "refresh-rollups"})  # every later statement
    conn.set_query_tag(tag=None)                          # no tag
```

The server stores them in `_hardcharger_instr.sql_statements.query_tag` and
`_hardcharger_instr.sessions.session_tag`. A tag that isn't a JSON object is refused with
`Invalid JSON for query_tag`.

## ⚙️ Session settings

Every `hardcharger.<name>` setting that `hardcharger_settings()` lists:

```python
conn.set_setting(name="query_timeout", value=60)        # SET SESSION hardcharger.query_timeout = 60
print(conn.get_setting(name="query_timeout"))            # '60'
conn.reset_setting(name="query_timeout")                 # RESET SESSION hardcharger.query_timeout
conn.set_setting(name="max_concurrent_statements", value=8, scope="global")  # admins only
print(conn.settings())                                   # every setting, as an Arrow table
```

`connect(settings={"query_timeout": 60})` applies settings as the connection opens; a bad name
or value fails `connect()` and closes the session it opened. Also on the connection:
`current_user()`, `current_session()` and `server_version()`.

## 🌐 Browser sign-in (SSO)

When the server runs its browser sign-in (`--oauth-client-id`, `--oauth-client-secret`,
`--oauth-port`, with the identity provider in `--token-allowed-issuer`), sign in as yourself
instead of with a password:

```python
with hardcharger.connect(uri="hardcharger://db.example.com", auth_type="external") as conn:
    print(conn.current_user())  # the e-mail address you signed in with
```

The driver asks the server's sign-in server (`https://HOST:31339` by default; `oauth_port=`,
or `oauth_url=` for the server's `--oauth-base-url`) for a sign-in, opens it in your browser,
waits for you, and signs in to Warp SQL with the identity provider's ID token (token exchange).
`open_browser=False` prints the URL instead (for a remote shell); a callable receives it.
The sign-in server is reached over HTTPS when the connection is (no fallback to plain HTTP).

## 🧩 Other ADBC languages (driver manifest)

Install the manifest once, and any ADBC driver manager (C/C++, Go, R, Rust, Python) loads the
driver as `hardcharger`:

```bash
python -m adbc_driver_hardcharger.manifest          # writes hardcharger.toml for this user
python -m adbc_driver_hardcharger.manifest --print  # or print it
```

```python
import adbc_driver_manager.dbapi

conn = adbc_driver_manager.dbapi.connect(driver="hardcharger", db_kwargs={
    "uri": "warp://localhost:31337?tls=false", "username": "hardcharger", "password": "change-me"})
```

The manifest points at the Warp SQL driver library, which reads Warp SQL URIs: through the
manifest, write `warp://host:31337` (`?tls=false` for plain HTTP), not `hardcharger://`.
[packaging/hardcharger.toml.in](packaging/hardcharger.toml.in) is the template for installing it
by hand. Go programs use [warp-sql-adbc](https://github.com/gizmodata/warp-sql-adbc) directly.

## 🧪 Testing

The integration tests start real `hardcharger-server` processes (plain HTTP, TLS,
instrumentation, browser sign-in against a mock identity provider) on free loopback ports.
Without a server binary they fail, never skip.

```bash
git clone --recurse-submodules https://github.com/gizmodata/hardcharger-adbc.git
cd hardcharger-adbc
scripts/build_warpsql_wheel.sh                     # adbc-driver-warpsql, from third_party/warp-sql-adbc
cd python
uv venv .venv && uv pip install --python .venv/bin/python \
  --override ../build/overrides.txt -e ".[test,dev]"
HARDCHARGER_ADBC_TEST_SERVER_BINARY=/path/to/hardcharger-server .venv/bin/python -m pytest
```

`adbc-driver-warpsql` isn't on PyPI yet, so `scripts/build_warpsql_wheel.sh` builds it from the
pinned `third_party/warp-sql-adbc` submodule (Go and, on Linux, `zig`) and writes the override
that points the dependency at that wheel. CI does the same, and downloads the server from
Hardcharger's GitHub Releases.

## 📦 Releasing

Releases follow [semantic versioning](https://semver.org) and
[CHANGELOG.md](CHANGELOG.md). Pushing a `vX.Y.Z` tag runs the whole test suite on the tagged
commit, then builds the wheel and sdist, attaches them to a GitHub Release, and publishes to
PyPI from the `pypi` environment (trusted publishing, after an approval). The publish refuses
to run while no `adbc-driver-warpsql` release that satisfies the dependency is on PyPI.

## 📄 License

Apache License 2.0; see [LICENSE](LICENSE).

---

<p align="center">
  <a href="https://gizmodata.com"><img src="docs/assets/gizmodata-logo.png" alt="GizmoData" width="56"></a><br>
  <sub>Built by <a href="https://gizmodata.com">GizmoData</a></sub>
</p>
