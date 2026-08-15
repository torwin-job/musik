#!/usr/bin/env python3
"""E2E contract shared by the Flutter and browser refresh implementations."""

from __future__ import annotations

import argparse
import json
import time
import urllib.error
import urllib.request
from typing import Any


def _json(
    base_url: str,
    path: str,
    *,
    token: str = "",
    method: str = "GET",
    body: dict[str, Any] | None = None,
) -> dict[str, Any]:
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    request = urllib.request.Request(
        base_url.rstrip("/") + path,
        headers=headers,
        data=data,
        method=method,
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        parsed = json.load(response)
    if not isinstance(parsed, dict):
        raise RuntimeError(f"{path} returned non-object JSON")
    return parsed


def _playlist_ids(payload: dict[str, Any]) -> dict[str, int]:
    return {
        str(item["kind"]): int(item["playlist_id"])
        for item in payload.get("mixes", [])
        if isinstance(item, dict) and item.get("playlist_id") is not None
    }


def refresh_and_wait(
    base_url: str,
    *,
    token: str = "",
    timeout: float = 180.0,
    poll_interval: float = 2.0,
) -> dict[str, Any]:
    """Run enqueue → poll terminal status → reload, the contract used by both clients."""
    before = _playlist_ids(_json(base_url, "/api/mixes", token=token))
    queued = _json(
        base_url,
        "/api/jobs/mix_pack",
        token=token,
        method="POST",
        body={},
    )
    raw_id = queued.get("id", queued.get("job_id"))
    if raw_id is None:
        raise RuntimeError("enqueue response has neither id nor job_id")
    job_id = int(raw_id)
    deadline = time.monotonic() + timeout
    job: dict[str, Any] = {}
    while time.monotonic() < deadline:
        job = _json(base_url, f"/api/jobs/{job_id}", token=token)
        status = job.get("status")
        if status == "done":
            break
        if status == "failed":
            raise RuntimeError(str(job.get("error") or f"job {job_id} failed"))
        time.sleep(poll_interval)
    else:
        raise TimeoutError(f"job {job_id} did not finish within {timeout}s")

    after_payload = _json(base_url, "/api/mixes", token=token)
    after = _playlist_ids(after_payload)
    if before.get("for_you") == after.get("for_you"):
        raise AssertionError("for_you playlist_id did not change after completed mix_pack")
    return {"job": job, "before": before, "after": after, "mixes": after_payload}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", required=True)
    parser.add_argument("--token", default="")
    parser.add_argument("--timeout", type=float, default=180.0)
    args = parser.parse_args()
    result = refresh_and_wait(args.base, token=args.token, timeout=args.timeout)
    print(
        json.dumps(
            {
                "ok": True,
                "job_id": result["job"].get("id"),
                "for_you_before": result["before"].get("for_you"),
                "for_you_after": result["after"].get("for_you"),
            },
            ensure_ascii=False,
        )
    )


if __name__ == "__main__":
    try:
        main()
    except (OSError, urllib.error.URLError, ValueError, RuntimeError) as exc:
        raise SystemExit(str(exc)) from exc
