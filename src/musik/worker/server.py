"""HTTP worker service (stdlib ThreadingHTTPServer)."""

from __future__ import annotations

import json
import logging
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import urlparse
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from musik.config import get_settings
from musik.db import ensure_db
from musik.jobs.queue import enqueue_job, enqueue_job_once, get_job, list_recent
from musik.jobs.runner import run_pending

log = logging.getLogger(__name__)

_poll_stop = threading.Event()
_poll_thread: threading.Thread | None = None


def enqueue_nightly_mixes_if_due(now: datetime | None = None) -> dict[str, Any] | None:
    """Queue one mix_pack per local calendar night after the configured hour."""
    settings = get_settings()
    if not settings.nightly_mixes_enabled:
        return None
    current = now or datetime.now(timezone.utc)
    tz_name = settings.nightly_mixes_timezone.strip()
    if tz_name and tz_name.lower() != "local":
        try:
            current = current.astimezone(ZoneInfo(tz_name))
        except ZoneInfoNotFoundError:
            log.warning("unknown MUSIK_NIGHTLY_MIXES_TIMEZONE=%s; using local timezone", tz_name)
            current = current.astimezone()
    else:
        current = current.astimezone()
    if current.hour < settings.nightly_mixes_hour:
        return None
    night = current.date().isoformat()
    job = enqueue_job_once(
        "mix_pack",
        {"night": night, "trigger": "nightly"},
    )
    if job is not None:
        log.info("queued nightly mix_pack #%s for %s", job["id"], night)
    return job


def _json_response(handler: BaseHTTPRequestHandler, status: int, body: Any) -> None:
    data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(data)))
    handler.end_headers()
    handler.wfile.write(data)


def _read_json(handler: BaseHTTPRequestHandler) -> dict[str, Any]:
    length = int(handler.headers.get("Content-Length", 0))
    if length <= 0:
        return {}
    raw = handler.rfile.read(length)
    if not raw:
        return {}
    parsed = json.loads(raw.decode("utf-8"))
    if not isinstance(parsed, dict):
        raise ValueError("JSON body must be an object")
    return parsed


def _job_public(job: dict[str, Any] | None) -> dict[str, Any] | None:
    if job is None:
        return None
    out: dict[str, Any] = {
        "id": job["id"],
        "kind": job["kind"],
        "status": job["status"],
        "payload": job.get("payload"),
        "result": job.get("result"),
        "error": job.get("error"),
        "created_at": job.get("created_at"),
        "updated_at": job.get("updated_at"),
    }
    res = job.get("result")
    if isinstance(res, dict) and "progress" in res:
        out["progress"] = res["progress"]
    return out


class WorkerHandler(BaseHTTPRequestHandler):
    server_version = "musik-worker/0.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        log.info("%s - %s", self.address_string(), fmt % args)

    def do_GET(self) -> None:  # noqa: N802
        path = urlparse(self.path).path.rstrip("/") or "/"
        try:
            if path == "/jobs":
                jobs = [_job_public(j) for j in list_recent()]
                _json_response(self, 200, {"jobs": jobs})
                return
            if path.startswith("/jobs/"):
                job_id = int(path.split("/")[-1])
                job = _job_public(get_job(job_id))
                if job is None:
                    _json_response(self, 404, {"error": "not found"})
                    return
                _json_response(self, 200, job)
                return
            _json_response(self, 404, {"error": "not found"})
        except Exception as exc:
            log.exception("GET %s failed", path)
            _json_response(self, 500, {"error": str(exc)})

    def do_POST(self) -> None:  # noqa: N802
        path = urlparse(self.path).path.rstrip("/") or "/"
        try:
            if path == "/jobs":
                body = _read_json(self)
                kind = body.get("kind")
                if not kind or not isinstance(kind, str):
                    _json_response(self, 400, {"error": "kind required"})
                    return
                payload = body.get("payload")
                if payload is not None and not isinstance(payload, dict):
                    _json_response(self, 400, {"error": "payload must be object"})
                    return
                job = enqueue_job(kind, payload if isinstance(payload, dict) else None)
                pub = _job_public(job)
                assert pub is not None
                _json_response(self, 201, {"id": pub["id"], "status": pub["status"]})
                return
            if path == "/run":
                body = _read_json(self)
                max_jobs = body.get("max_jobs")
                results = run_pending(
                    max_jobs=int(max_jobs) if max_jobs is not None else None
                )
                _json_response(self, 200, {"processed": len(results), "results": results})
                return
            _json_response(self, 404, {"error": "not found"})
        except json.JSONDecodeError:
            _json_response(self, 400, {"error": "invalid JSON"})
        except Exception as exc:
            log.exception("POST %s failed", path)
            _json_response(self, 500, {"error": str(exc)})


def _poll_loop(poll_sec: float) -> None:
    while not _poll_stop.is_set():
        try:
            enqueue_nightly_mixes_if_due()
            run_pending(max_jobs=1)
        except Exception:
            log.exception("background poll failed")
        _poll_stop.wait(poll_sec)


def start_background_poll(poll_sec: float | None = None) -> None:
    global _poll_thread
    settings = get_settings()
    interval = poll_sec if poll_sec is not None else settings.worker_poll_sec
    if _poll_thread is not None and _poll_thread.is_alive():
        return
    _poll_stop.clear()
    _poll_thread = threading.Thread(
        target=_poll_loop,
        args=(interval,),
        name="musik-worker-poll",
        daemon=True,
    )
    _poll_thread.start()


def stop_background_poll() -> None:
    _poll_stop.set()
    if _poll_thread is not None:
        _poll_thread.join(timeout=2.0)


def parse_addr(addr: str) -> tuple[str, int]:
    if addr.startswith(":"):
        return "", int(addr[1:])
    if ":" in addr:
        host, port = addr.rsplit(":", 1)
        return host, int(port)
    return addr, 8790


def serve(*, addr: str | None = None, poll: bool = True) -> None:
    """Start the worker HTTP server (blocking)."""
    ensure_db()
    settings = get_settings()
    host, port = parse_addr(addr or settings.worker_addr)
    if poll:
        start_background_poll()
    httpd = ThreadingHTTPServer((host, port), WorkerHandler)
    log.info("musik worker listening on http://%s:%s", host or "0.0.0.0", port)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        log.info("shutting down worker")
    finally:
        stop_background_poll()
        httpd.server_close()
