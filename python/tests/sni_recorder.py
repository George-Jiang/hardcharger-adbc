"""A TCP proxy that records each connection's TLS server name (SNI), run as its own process
(the test process may be inside the driver library while it connects):
``python sni_recorder.py TARGET_PORT LOG`` forwards 127.0.0.1:<free port> to
127.0.0.1:TARGET_PORT byte for byte, and for every connection appends one JSON line to LOG with
the ``server_name`` its ClientHello carried (``null``: no server_name extension), read from the
bytes on the wire. It prints its port."""

import json
import socket
import struct
import sys
import threading

TARGET_PORT = int(sys.argv[1])
LOG = sys.argv[2]
lock = threading.Lock()


def server_name(hello: bytes):
    """The host_name in a ClientHello's server_name extension (RFC 6066 §3), or None.

    ``hello`` is the handshake message: type (1), length (3), then the ClientHello body.
    """
    if hello[0] != 1:
        raise ValueError(f"handshake type {hello[0]}, not a ClientHello")
    at = 4 + 2 + 32  # header, legacy_version, random
    at += 1 + hello[at]  # legacy_session_id
    (length,) = struct.unpack_from(">H", hello, at)
    at += 2 + length  # cipher_suites
    at += 1 + hello[at]  # legacy_compression_methods
    if at == len(hello):
        return None  # no extensions
    (extensions_length,) = struct.unpack_from(">H", hello, at)
    at += 2
    end = at + extensions_length
    while at < end:
        kind, length = struct.unpack_from(">HH", hello, at)
        at += 4
        if kind == 0:  # server_name: ServerNameList
            entry = at + 2
            name_type, name_length = struct.unpack_from(">BH", hello, entry)
            if name_type != 0:
                raise ValueError(f"server name type {name_type}, not host_name")
            return hello[entry + 3 : entry + 3 + name_length].decode(encoding="ascii")
        at += length
    return None


def read_exactly(sock: socket.socket, n: int) -> bytes:
    data = b""
    while len(data) < n:
        chunk = sock.recv(n - len(data))
        if not chunk:
            raise EOFError("the client closed the connection")
        data += chunk
    return data


def read_client_hello(sock: socket.socket) -> tuple[bytes, bytes]:
    """Read TLS handshake records until a whole ClientHello is in: (raw bytes, the message)."""
    raw = b""
    message = b""
    while len(message) < 4 or len(message) < 4 + int.from_bytes(message[1:4], "big"):
        header = read_exactly(sock=sock, n=5)
        if header[0] != 22:
            raise ValueError(f"TLS record type {header[0]}, not handshake")
        (length,) = struct.unpack_from(">H", header, 3)
        body = read_exactly(sock=sock, n=length)
        raw += header + body
        message += body
    return raw, message


def pipe(src: socket.socket, dst: socket.socket) -> None:
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        for sock in (src, dst):
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def handle(client: socket.socket) -> None:
    try:
        raw, hello = read_client_hello(sock=client)
        record = {"server_name": server_name(hello=hello)}
    except (OSError, EOFError, ValueError, struct.error) as error:
        record = {"error": str(error)}
        raw = None
    with lock, open(file=LOG, mode="a") as log:
        log.write(json.dumps(obj=record) + "\n")
    if raw is None:
        client.close()
        return
    try:
        upstream = socket.create_connection(address=("127.0.0.1", TARGET_PORT))
        upstream.sendall(raw)
    except OSError:
        client.close()
        return
    threading.Thread(target=pipe, args=(client, upstream), daemon=True).start()
    threading.Thread(target=pipe, args=(upstream, client), daemon=True).start()


listener = socket.socket(family=socket.AF_INET, type=socket.SOCK_STREAM)
listener.bind(("127.0.0.1", 0))
listener.listen(64)
print(listener.getsockname()[1], flush=True)
while True:
    connection, _ = listener.accept()
    threading.Thread(target=handle, args=(connection,), daemon=True).start()
