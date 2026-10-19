"""A recording HTTP proxy for the ingest tests, run as its own process (the test process is
inside the driver library during an ingest): ``python recording_proxy.py TARGET_PORT LOG``
forwards 127.0.0.1:<free port> to 127.0.0.1:TARGET_PORT, appends "METHOD PATH" per request to
LOG, and prints its port."""

import http.client
import http.server
import sys
import threading

TARGET_PORT = int(sys.argv[1])
LOG = sys.argv[2]
lock = threading.Lock()
HOP = {
    "connection",
    "keep-alive",
    "transfer-encoding",
    "te",
    "trailer",
    "upgrade",
    "proxy-authorization",
    "proxy-authenticate",
}


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, format, *args):
        pass

    def forward(self):
        with lock, open(file=LOG, mode="a", encoding="utf-8") as log:
            log.write(f"{self.command} {self.path.split('?')[0]}\n")
        length = int(self.headers.get("Content-Length") or 0)
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            body = b""
            while True:
                size = int(self.rfile.readline().strip(), 16)
                if size == 0:
                    self.rfile.readline()
                    break
                body += self.rfile.read(size)
                self.rfile.readline()
        else:
            body = self.rfile.read(length) if length else None
        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP}
        headers.pop("Content-Length", None)
        upstream = http.client.HTTPConnection(host="127.0.0.1", port=TARGET_PORT, timeout=300)
        upstream.request(method=self.command, url=self.path, body=body, headers=headers)
        response = upstream.getresponse()
        data = response.read()
        self.send_response(response.status)
        for key, value in response.getheaders():
            if key.lower() not in HOP and key.lower() != "content-length":
                self.send_header(key, value)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
        upstream.close()

    do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = forward


class Server(http.server.ThreadingHTTPServer):
    # The default listen backlog (5) is smaller than the connections a partitioned ingest
    # opens at once (8 parts); on macOS the overflow was reset mid-upload and the driver sent
    # that part again, so the test counted 9 part requests.
    request_queue_size = 64


server = Server(("127.0.0.1", 0), Handler)
print(server.server_address[1], flush=True)
server.serve_forever()
