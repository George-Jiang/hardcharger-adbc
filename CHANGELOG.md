# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Documentation

- Trademark notices: Hardcharger™ and GizmoData™ on the README titles and first mentions, package
  metadata and the footer, which says both are trademarks of GizmoData LLC.

### Added

- Client certificate sign-in (mutual TLS, Warp SQL §4.4): `adbc.hardcharger.auth_type=client_certificate`
  (also `mtls`), Python `connect(auth_type="client_certificate", tls_client_cert=..., tls_client_key=...)`.
  It is the Warp SQL driver's `adbc.warp.auth.type`: the certificate is the credential, the token is
  bound to it, and result partitions and ingest parts present it too. Needs a server with
  `--mtls-ca-cert` and `--mtls-certificate-sign-in`. Live tests in Go and Python against
  `hardcharger-server`: sign-in (an e-mail identity matching `--mtls-admin-identities` ignoring
  case), a split result and a partitioned ingest (row count, distinct keys and sum checked
  exactly), an unknown identity (`unknown_certificate_identity`), a bound token over another
  certificate (`certificate_mismatch`) and its twin, a password beside the certificate, and a server
  with certificate sign-in off (capability `mtls_identity`) with its password twin.

### Changed

- Built on warp-sql-adbc `b74dfe2` (client certificate sign-in), from `b3d2ab8`.

- Python 3.11 is the minimum (`requires-python = ">=3.11"`): pyarrow 26, the floor,
  needs 3.11, so on 3.10 the package could never be installed.

### Fixed

- In Go, Warp SQL options set on a database after it was created (`db.SetOption`,
  `db.SetOptions`, e.g. `adbc.warp.retry.max_attempts`) were refused as unknown: they went to
  driverbase's `SetOptions`, which knows none of them. They now reach the Warp SQL driver one by one.
  (Options given at creation, and every option set through the C API or Python, were not affected.)

- The `dbapi` module exports PEP 249's exception classes (`Error`, `Warning`, `InterfaceError`,
  `DatabaseError`, `DataError`, `OperationalError`, `IntegrityError`, `InternalError`,
  `ProgrammingError`, `NotSupportedError`), as PEP 249 requires of a driver module. They are
  `adbc_driver_manager`'s own classes, so `except dbapi.ProgrammingError` and
  `except adbc_driver_manager.ProgrammingError` catch the same errors.

### Added

- The Hardcharger ADBC driver: a Go package (`github.com/gizmodata/hardcharger-adbc/go/hardcharger`)
  on the Warp SQL ADBC driver (warp-sql-adbc b3d2ab8, `warpsql.NewDriverWithName`), compiled to the C shared library
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
- The Warp SQL driver's defaults, inherited: a large result is split into up to 8 partitions
  read at once (`adbc.warp.statement.result_partitions`), and a bulk ingest of two batches or
  more goes in up to 8 parts (`adbc.warp.ingest.parts`, also `adbc.hardcharger.ingest.parts`
  on the database or a connection). Tests count the partitions (`ExecutePartitions`) and the
  ingest's part uploads (through a recording proxy), in Go and Python, each with a twin set to 1.
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
