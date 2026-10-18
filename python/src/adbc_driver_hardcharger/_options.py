"""Option keys: Hardcharger's (``adbc.hardcharger.*``) and the Warp SQL driver's (``adbc.warp.*``),
which the Hardcharger driver is built on."""

import enum


class DatabaseOptions(enum.Enum):
    """Database options. The standard ``uri``, ``username`` and ``password``
    options and the ``adbc.telemetry.*`` options apply too."""

    #: "password" (the default) or "external": browser sign-in when the first connection opens.
    AUTH_TYPE = "adbc.hardcharger.auth_type"
    #: Base URL of the server's browser sign-in server (``--oauth-base-url``).
    OAUTH_URL = "adbc.hardcharger.oauth.url"
    #: Port of the browser sign-in server (default 31339).
    OAUTH_PORT = "adbc.hardcharger.oauth.port"
    #: Seconds to wait for the browser sign-in (default 300).
    OAUTH_TIMEOUT_SECONDS = "adbc.hardcharger.oauth.timeout_seconds"
    #: Command that opens the sign-in page (URL appended); "none" prints the URL instead.
    OAUTH_BROWSER = "adbc.hardcharger.oauth.browser"
    #: A JSON object tagging every session (hardcharger.session_tag).
    SESSION_TAG = "adbc.hardcharger.session_tag"
    #: A JSON object recorded with every statement (hardcharger.query_tag).
    QUERY_TAG = "adbc.hardcharger.query_tag"
    #: Prefix: ``SETTING_PREFIX + "query_timeout"`` sets hardcharger.query_timeout on every session.
    SETTING_PREFIX = "adbc.hardcharger.setting."
    #: The most connections one bulk ingest is sent over at once (default 8; 1: one ordered
    #: stream): ``adbc.warp.ingest.parts`` under Hardcharger's name. Also a connection option.
    INGEST_PARTS = "adbc.hardcharger.ingest.parts"

    #: Do not verify the server's TLS certificate (development only).
    TLS_SKIP_VERIFY = "adbc.warp.tls.skip_verify"
    #: PEM-encoded CA certificates to trust instead of the system's.
    TLS_ROOT_CERTS = "adbc.warp.tls.root_certs"
    #: PEM certificate chain presented to servers that require mutual TLS.
    TLS_CLIENT_CERT = "adbc.warp.tls.client_cert"
    #: PEM private key of the client certificate.
    TLS_CLIENT_KEY = "adbc.warp.tls.client_key"
    #: Host name the server certificate is checked against, instead of the URI's.
    TLS_SERVER_NAME = "adbc.warp.tls.server_name"
    #: A Warp SQL access token obtained elsewhere (used as is, never renewed).
    ACCESS_TOKEN = "adbc.warp.auth.access_token"
    #: An identity provider's token, exchanged for an access token.
    SUBJECT_TOKEN = "adbc.warp.auth.subject_token"
    #: How often a request that is safe to repeat is sent, first try included.
    RETRY_MAX_ATTEMPTS = "adbc.warp.retry.max_attempts"
    #: Replace a session the server lost with a new one ("true", the default).
    SESSION_RECONNECT = "adbc.warp.session.reconnect"


class ConnectionOptions(enum.Enum):
    """Connection options."""

    #: The session's tag (read and write; "" removes it).
    SESSION_TAG = "adbc.hardcharger.session_tag"
    #: The tag of the session's following statements (read and write; "" removes it).
    QUERY_TAG = "adbc.hardcharger.query_tag"
    #: Prefix: read or SET SESSION hardcharger.NAME ("" resets it).
    SETTING_PREFIX = "adbc.hardcharger.setting."
    #: Prefix: SET GLOBAL hardcharger.NAME, admins only ("" resets it).
    GLOBAL_SETTING_PREFIX = "adbc.hardcharger.global_setting."
    #: The most connections one bulk ingest is sent over at once (``adbc.warp.ingest.parts``).
    INGEST_PARTS = "adbc.hardcharger.ingest.parts"

    #: The connection's current server session id.
    SESSION_ID = "adbc.warp.session.id"
    #: How often the connection's session was lost and replaced.
    SESSION_REPLACEMENTS = "adbc.warp.session.replacements"


class StatementOptions(enum.Enum):
    """Statement options. They can also be set on the database or the
    connection, as defaults for the statements created from it."""

    #: Run queries asynchronously, with a retained, resumable result.
    ASYNC = "adbc.warp.statement.async"
    #: Ask the server to keep results for re-reading ("true"/"false").
    RETAIN_RESULT = "adbc.warp.statement.retain_result"
    #: Seconds after which the server cancels a statement.
    TIMEOUT_SECONDS = "adbc.warp.statement.timeout_seconds"
    #: A JSON object recorded with every statement on the server.
    QUERY_TAG = "adbc.warp.statement.query_tag"
    #: Arrow buffer compression for results and uploads: "zstd" or "none".
    IPC_COMPRESSION = "adbc.warp.ipc_compression"
    #: The most partitions a result may be split into ("8", the default; "1": never). A
    #: server that offers it splits a large result, and the driver reads every partition at
    #: once, each over its own connection. A result of fewer than two batches comes as one
    #: stream either way.
    RESULT_PARTITIONS = "adbc.warp.statement.result_partitions"
    #: "preserve" (the default): a split result comes back in order; "any": in whatever
    #: order its batches arrive.
    RESULT_ORDER = "adbc.warp.statement.result_order"
    #: The most connections one bulk ingest is sent over at once ("8", the default; "1":
    #: one stream). An upload of two batches or more is then a partitioned ingest, committed
    #: as one; its rows land in no defined order, so set "1" to keep the upload's order.
    INGEST_PARTS = "adbc.warp.ingest.parts"
    #: The id of the statement's last execution (read-only).
    STATEMENT_ID = "adbc.warp.statement.id"
