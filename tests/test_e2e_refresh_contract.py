from __future__ import annotations

import threading
import importlib.util
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

_SCRIPT = Path(__file__).parents[1] / "scripts" / "e2e_refresh_contract.py"
_SPEC = importlib.util.spec_from_file_location("e2e_refresh_contract", _SCRIPT)
assert _SPEC is not None and _SPEC.loader is not None
_MODULE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MODULE)
refresh_and_wait = _MODULE.refresh_and_wait


class RefreshContractHandler(BaseHTTPRequestHandler):
    polls = 0
    completed = False
    paths: list[str] = []

    def log_message(self, _format: str, *_args: object) -> None:
        pass

    def _send(self, payload: str) -> None:
        raw = payload.encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self) -> None:  # noqa: N802
        type(self).paths.append(f"POST {self.path}")
        assert self.headers["Authorization"] == "Bearer test-token"
        self._send('{"job_id": 17, "status": "pending"}')

    def do_GET(self) -> None:  # noqa: N802
        type(self).paths.append(f"GET {self.path}")
        assert self.headers["Authorization"] == "Bearer test-token"
        if self.path == "/api/jobs/17":
            type(self).polls += 1
            if type(self).polls >= 2:
                type(self).completed = True
                self._send('{"id": 17, "status": "done"}')
            else:
                self._send('{"id": 17, "status": "running"}')
            return
        playlist_id = 2 if type(self).completed else 1
        self._send(
            '{"mixes":[{"kind":"for_you","playlist_id":%d}]}' % playlist_id
        )


def test_shared_mobile_browser_refresh_contract() -> None:
    RefreshContractHandler.polls = 0
    RefreshContractHandler.completed = False
    RefreshContractHandler.paths = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), RefreshContractHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        result = refresh_and_wait(
            f"http://127.0.0.1:{server.server_port}",
            token="test-token",
            timeout=2,
            poll_interval=0.001,
        )
    finally:
        server.shutdown()
        thread.join(timeout=2)
        server.server_close()

    assert result["before"]["for_you"] == 1
    assert result["after"]["for_you"] == 2
    assert RefreshContractHandler.paths == [
        "GET /api/mixes",
        "POST /api/jobs/mix_pack",
        "GET /api/jobs/17",
        "GET /api/jobs/17",
        "GET /api/mixes",
    ]
