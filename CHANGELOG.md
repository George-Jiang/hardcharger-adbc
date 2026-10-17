# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `adbc-driver-hardcharger`, the Python ADBC driver for Hardcharger, on `adbc-driver-warpsql`:
  `from adbc_driver_hardcharger import dbapi as hardcharger` and
  `hardcharger.connect(uri="hardcharger://host", username=..., password=...)`.
- `hardcharger://host[:port][/base-path][?tls=false]` URIs: Warp SQL's `warp://` with port
  31337 by default (HTTPS unless `?tls=false`). `warp://`, `https://` and `http://` pass
  through; `hardcharger+http://` is refused with the spelling to use.
- Query and session tags (`query_tag=`, `session_tag=`, `Connection.set_query_tag()`,
  `set_session_tag()`), recorded in Hardcharger's instrumentation.
- `hardcharger.*` settings: `Connection.set_setting()`, `get_setting()`, `reset_setting()`,
  `settings()`, and `connect(settings=...)`; `current_user()`, `current_session()`,
  `server_version()`.
- Browser sign-in (`auth_type="external"`) through the server's sign-in server, exchanging the
  identity provider's ID token for a Warp SQL access token.
- The ADBC driver manifest for `driver = "hardcharger"`
  (`python -m adbc_driver_hardcharger.manifest`, `packaging/hardcharger.toml.in`).
- Tests against real `hardcharger-server` processes (plain HTTP, TLS, instrumentation, browser
  sign-in with a mock identity provider, the manifest), failing without a server; CI; a
  tag-triggered release to GitHub Releases and PyPI, gated on the full suite.
