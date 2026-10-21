# adbc-driver-hardcharger

The [ADBC](https://arrow.apache.org/adbc/) driver for
[Hardcharger™](https://github.com/gizmodata/hardcharger), GizmoData™'s DuckDB 2.0 server: a
native Go driver (built on the [Warp SQL ADBC driver](https://github.com/gizmodata/warp-sql-adbc))
whose library ships in this wheel, with Python DB-API bindings. `hardcharger://` URIs (port
31337 by default), query and session tags, `hardcharger.*` settings, browser sign-in, and an
ADBC driver manifest so `driver = "hardcharger"` works in every ADBC language.

```bash
pip install adbc-driver-hardcharger     # this client
pip install hardcharger                 # the Hardcharger server, started from Python
pip install "hardcharger[adbc]"         # both
```

```python
from adbc_driver_hardcharger import dbapi as hardcharger

with hardcharger.connect(uri="hardcharger://localhost?tls=false", username="hardcharger", password="change-me") as conn:
    with conn.cursor() as cur:
        cur.execute(operation="SELECT 42 AS answer, ? AS greeting", parameters=["hello"])
        print(cur.fetch_arrow_table())
```

`hardcharger://host[:port]` is HTTPS; `?tls=false` is plain HTTP for development. See the
[repository README](https://github.com/gizmodata/hardcharger-adbc#readme) for tags, settings,
browser sign-in, client certificate sign-in (`auth_type="client_certificate"`), token exchange
with your identity provider's tokens (`subject_token=`), the TLS server name
(`tls_server_name=`), the driver manifest, and the C/C++ and Go examples.

Apache License 2.0. Built by [GizmoData](https://gizmodata.com).

## Connection profiles

Install the manifest with `python -m adbc_driver_hardcharger.manifest`, then reuse a standard
ADBC TOML profile (`driver = "hardcharger"`) with `hardcharger.connect(profile="prod")`,
`hardcharger.connect(uri="profile://prod")`, or an absolute profile path. The generic
`adbc_driver_manager.dbapi.connect(profile="prod")` uses the same file. See the
[profile setup and complete examples](https://github.com/gizmodata/hardcharger-adbc#-adbc-connection-profiles)
for search paths, environment-variable credentials, browser SSO and option overrides.
