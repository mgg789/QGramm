import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

from run import wait_ready


class ReadinessTests(unittest.TestCase):
    def test_waits_through_unavailable_to_healthy(self):
        class Handler(BaseHTTPRequestHandler):
            requests = 0
            def do_GET(self):
                Handler.requests += 1
                self.send_response(200 if Handler.requests >= 3 else 503)
                self.end_headers()
            def log_message(self, *args):
                pass
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            elapsed = wait_ready('http://127.0.0.1:' + str(server.server_port), 2)
            self.assertGreaterEqual(Handler.requests, 3)
            self.assertGreaterEqual(elapsed, .2)
        finally:
            server.shutdown()
            worker.join()
            server.server_close()

    def test_failure_is_bounded(self):
        with patch('run.urllib.request.urlopen', side_effect=OSError('unavailable')):
            with self.assertRaises(TimeoutError):
                wait_ready('http://127.0.0.1:1', .15)


if __name__ == '__main__':
    unittest.main()
