"""Fetch lyrics from LRCLIB (https://lrclib.net)."""

from __future__ import annotations

import logging
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

logger = logging.getLogger(__name__)

LRCLIB_BASE = "https://lrclib.net/api"
USER_AGENT = "musik/0.1.0 (self-hosted; https://lrclib.net)"


def _get_json(url: str, *, timeout: float = 30.0) -> Any:
    req = urllib.request.Request(
        url, headers={"User-Agent": USER_AGENT, "Accept": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        import json

        return json.loads(resp.read().decode("utf-8"))


def fetch_lyrics(
    *,
    artist: str,
    title: str,
    album: str = "",
    duration_sec: float | None = None,
) -> dict[str, Any] | None:
    artist = (artist or "").strip()
    title = (title or "").strip()
    if not artist or not title:
        return None

    if duration_sec and duration_sec > 0:
        q = urllib.parse.urlencode(
            {
                "artist_name": artist,
                "track_name": title,
                "album_name": album or title,
                "duration": int(round(duration_sec)),
            }
        )
        try:
            return _normalize(_get_json(f"{LRCLIB_BASE}/get?{q}"))
        except urllib.error.HTTPError as e:
            if e.code != 404:
                logger.warning("lrclib get HTTP %s for %s — %s", e.code, title, artist)
        except Exception:
            logger.exception("lrclib get failed")

    q = urllib.parse.urlencode({"track_name": title, "artist_name": artist})
    try:
        results = _get_json(f"{LRCLIB_BASE}/search?{q}")
    except Exception:
        logger.exception("lrclib search failed")
        return None
    if not isinstance(results, list) or not results:
        return None
    return _normalize(_pick_best(results, artist=artist, title=title, duration_sec=duration_sec))


def _normalize(data: dict[str, Any] | None) -> dict[str, Any] | None:
    if not data:
        return None
    plain = (data.get("plainLyrics") or "").strip()
    synced = (data.get("syncedLyrics") or "").strip()
    instrumental = bool(data.get("instrumental"))
    if not plain and not synced and not instrumental:
        return None
    return {
        "plain_lyrics": plain,
        "synced_lyrics": synced,
        "instrumental": instrumental,
        "source": "lrclib",
        "source_id": str(data.get("id") or ""),
    }


def _pick_best(
    results: list[dict[str, Any]],
    *,
    artist: str,
    title: str,
    duration_sec: float | None,
) -> dict[str, Any]:
    def score(r: dict[str, Any]) -> float:
        s = 0.0
        if (r.get("artistName") or "").casefold() == artist.casefold():
            s += 3
        if (r.get("trackName") or "").casefold() == title.casefold():
            s += 3
        if duration_sec and r.get("duration"):
            diff = abs(float(r["duration"]) - float(duration_sec))
            if diff <= 2:
                s += 5
            elif diff <= 5:
                s += 2
        if r.get("syncedLyrics"):
            s += 0.5
        if r.get("plainLyrics"):
            s += 0.5
        return s

    return sorted(results, key=score, reverse=True)[0]


def throttle(sec: float = 0.35) -> None:
    time.sleep(sec)
