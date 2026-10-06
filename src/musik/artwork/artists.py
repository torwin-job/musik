"""Artist photos from the Deezer API (keyless).

iTunes has no artist images; Deezer does and keeps original spellings
("Сплин", "Би-2"). Only an exact (case/space-insensitive) name match is
accepted, the same rule as for album covers: a near match is another artist.
"""

from __future__ import annotations

import hashlib
import json
import logging
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from musik.config import get_settings
from musik.db.store import artist_photo_states, list_library_artists, save_artist_photo

logger = logging.getLogger(__name__)

DEEZER_SEARCH = "https://api.deezer.com/search/artist"
USER_AGENT = "musik/0.1.0 (self-hosted)"
SOURCE = "deezer"
# Deezer allows ~50 requests per 5 s; stay well below and back off on its
# quota error (HTTP 200 with {"error": {"code": 4}}).
_QUOTA_CODE = 4
_QUOTA_RETRIES = 3
# An artist without a match is not looked up again for this long.
MISS_RETRY_DAYS = 30
_sleep = time.sleep  # patched in tests


class RateLimited(RuntimeError):
    """Deezer keeps refusing requests; the batch should stop."""


def name_key(name: str) -> str:
    """Normalized artist name; the player and UI use the same rule."""
    return " ".join(str(name or "").split()).lower()


def _request(url: str, *, timeout: float = 30.0) -> tuple[bytes, str]:
    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read(), (resp.headers.get("Content-Type") or "")


def _get_json(url: str, *, timeout: float = 30.0) -> Any:
    raw, _ = _request(url, timeout=timeout)
    return json.loads(raw.decode("utf-8"))


def _search(name: str, *, timeout: float) -> list[dict[str, Any]]:
    url = f"{DEEZER_SEARCH}?{urllib.parse.urlencode({'q': name, 'limit': 10})}"
    for attempt in range(_QUOTA_RETRIES + 1):
        data = _get_json(url, timeout=timeout)
        error = data.get("error") if isinstance(data, dict) else None
        if not error:
            items = data.get("data") if isinstance(data, dict) else None
            return items if isinstance(items, list) else []
        if not isinstance(error, dict) or error.get("code") != _QUOTA_CODE:
            raise ValueError(f"deezer error: {error}")
        if attempt == _QUOTA_RETRIES:
            raise RateLimited("deezer quota exceeded")
        _sleep(5.0 * (attempt + 1))
    return []


def _picture_url(item: dict[str, Any]) -> str:
    url = str(item.get("picture_xl") or item.get("picture_big") or "").strip()
    # Artists without a photo get a placeholder with an empty hash: ".../artist//1000x1000-...".
    if not url or "/artist//" in url:
        return ""
    return url


def fetch_artist_photo(name: str, *, timeout: float = 30.0) -> bytes | None:
    """Return photo bytes for an artist, or None when nothing matches.

    Raises RateLimited when Deezer keeps throttling.
    """
    want = name_key(name)
    if not want:
        return None
    try:
        items = _search(name.strip(), timeout=timeout)
    except RateLimited:
        raise
    except (urllib.error.URLError, OSError, ValueError):
        logger.exception("deezer search failed for %s", name)
        return None
    # Among exact matches prefer the most followed one (the real artist page
    # over a fan-made duplicate with the same name).
    matches = [i for i in items if name_key(str(i.get("name") or "")) == want and _picture_url(i)]
    if not matches:
        return None
    best = max(matches, key=lambda i: int(i.get("nb_fan") or 0))
    try:
        raw, content_type = _request(_picture_url(best), timeout=timeout)
    except (urllib.error.URLError, OSError):
        logger.exception("deezer photo download failed for %s", name)
        return None
    if raw[:3] != b"\xff\xd8\xff" and raw[:8] != b"\x89PNG\r\n\x1a\n" and not content_type.startswith("image/"):
        return None
    return raw


def photo_dir() -> Path:
    out = get_settings().artwork_cache / "artists"
    out.mkdir(parents=True, exist_ok=True)
    return out


def save_photo(key: str, data: bytes) -> str:
    out = photo_dir() / f"{hashlib.md5(key.encode('utf-8')).hexdigest()}.jpg"
    out.write_bytes(data)
    return str(out)


@dataclass
class ArtistPhotoResult:
    total: int = 0
    found: int = 0
    missing: int = 0
    failed: int = 0
    rate_limited: bool = False
    errors: list[str] = field(default_factory=list)


def fetch_library_artist_photos(
    *, limit: int | None = None, force: bool = False, delay_sec: float = 0.5
) -> ArtistPhotoResult:
    """Look up photos for library artists that have none yet.

    Skips artists whose photo file exists and, unless force, those looked up
    without a match in the last MISS_RETRY_DAYS days.
    """
    states = artist_photo_states()
    cutoff = (datetime.now(timezone.utc) - timedelta(days=MISS_RETRY_DAYS)).isoformat()
    todo: list[str] = []
    for artist in list_library_artists():
        state = states.get(name_key(artist))
        if state and not force:
            if state["status"] == "found" and state["path"] and Path(state["path"]).is_file():
                continue
            if state["status"] == "missing" and str(state["checked_at"] or "") >= cutoff:
                continue
        todo.append(artist)
    if limit is not None:
        todo = todo[:limit]

    result = ArtistPhotoResult(total=len(todo))
    for index, artist in enumerate(todo):
        key = name_key(artist)
        try:
            photo = fetch_artist_photo(artist)
        except RateLimited as e:
            logger.warning("artist photos stopped: %s", e)
            result.rate_limited = True
            result.failed += len(todo) - index
            result.errors.append(f"{artist}: {e}")
            break
        except Exception as e:  # noqa: BLE001
            logger.exception("artist photo failed for %s", artist)
            result.failed += 1
            result.errors.append(f"{artist}: {e}")
            continue
        if photo is None:
            save_artist_photo(key, artist, None, SOURCE)
            result.missing += 1
        else:
            save_artist_photo(key, artist, save_photo(key, photo), SOURCE)
            result.found += 1
        if delay_sec > 0:
            _sleep(delay_sec)
    return result
