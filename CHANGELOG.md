# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The Hardcharger ADBC driver: a Go package (`github.com/gizmodata/hardcharger-adbc/go/hardcharger`)
  on the Warp SQL ADBC driver (warp-sql-adbc 33b29e9, `warpsql.NewDriverWithName`), compiled to the C shared library
  `libadbc_driver_hardcharger` (entry point `AdbcDriverHardchargerInit`), so every ADBC
  language gets:
  - `hardcharger://host[:port][/base-path][?tls=false]` URIs: Warp SQL's `warp://` with port
    31337 by default; `warp://`, `https://` and `http://` pass through; `hardcharger+http://`
    is refused with the spelling to use;
  - "ADBC Hardcharger Driver - Go" as GetInfo's driver name;
  - the `adbc.hardcharger.*` options: query and session tags, `hardcharger.*` session settings
    on every session (database) or one (connection; read back with GetOption), global
    settings, and browser sign-in (`auth_type=external`, exchanging the identity provider's ID
    token for a Warp SQL access token).
- The driver manifest for `driver = "hardcharger"` (`packaging/hardcharger.toml.in`,
  `python -m adbc_driver_hardcharger.manifest`), and C and Go examples.
- `adbc-driver-hardcharger`, the Python bindings: platform wheels carrying the library,
  `from adbc_driver_hardcharger import dbapi as hardcharger`, `hardcharger.connect(...)`, and
  helpers (`set_setting`, `get_setting`, `reset_setting`, `settings`, `set_query_tag`,
  `set_session_tag`, `current_user`, `current_session`, `server_version`) on the driver's options.
- Tests: Go unit and live suites (race detector) and Python unit and live suites against real
  `hardcharger-server` processes (plain HTTP, TLS, instrumentation, browser sign-in with a mock
  identity provider), the library loaded by manifest through the driver manager with a
  `hardcharger://` URI; all fail without a server. CI also runs the C example through the ADBC
  C driver manager and the Go example. `release.yml` (v* tags) gates on all of it, then builds
  the library for six platforms and wheels for five, a GitHub Release, and PyPI from the
  `pypi` environment.
