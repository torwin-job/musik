"""Fetch album cover art from the iTunes Search API (keyless)."""

from __future__ import annotations

import json
import logging
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

logger = logging.getLogger(__name__)

ITUNES_SEARCH = "https://itunes.apple.com/search"
USER_AGENT = "musik/0.1.0 (self-hosted)"
# iTunes returns a 100px thumbnail; the same URL scales to 1900px.
_THUMB = "100x100bb.jpg"
_LARGE = "1900x1900bb.jpg"


def _request(url: str, *, timeout: float = 30.0) -> tuple[bytes, str]:
    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read(), (resp.headers.get("Content-Type") or "")


def _get_json(url: str, *, timeout: float = 30.0) -> Any:
    raw, _ = _request(url, timeout=timeout)
    return json.loads(raw.decode("utf-8"))


def _large_artwork_url(url: str) -> str:
    return url.replace(_THUMB, _LARGE)


def _norm(value: Any) -> str:
    return " ".join(str(value or "").split()).casefold()


# Trailing edition marker, e.g. "(Remastered)", "[Deluxe Edition]".
_EDITION_RE = re.compile(r"\s*[(\[][^)\]]*[)\]]\s*$")


def _edition_base(value: str) -> str:
    base = _EDITION_RE.sub("", value).strip()
    return base or value


def _artist_match(item: dict[str, Any], artist: str) -> bool:
    """Only an exact artist match is accepted.

    A partial/substring match ("Би-2" inside "Би-2 & Сплин") pulls in another
    release, so it is rejected outright.
    """
    got = _norm(item.get("artistName"))
    want = _norm(artist)
    return bool(got) and got == want


def _album_match(item: dict[str, Any], album: str) -> int:
    """0 = no match, 1 = same album with a different edition suffix, 2 = exact."""
    got = _norm(item.get("collectionName"))
    want = _norm(album)
    if not got or not want:
        return 0
    if got == want:
        return 2
    if _edition_base(got) == _edition_base(want):
        return 1
    return 0


def _looks_like_image(raw: bytes, content_type: str) -> bool:
    if raw[:3] == b"\xff\xd8\xff":
        return True  # JPEG
    if raw[:8] == b"\x89PNG\r\n\x1a\n":
        return True  # PNG
    if raw[:6] in (b"GIF87a", b"GIF89a"):
        return True
    return content_type.startswith("image/")


def fetch_cover(*, artist: str, album: str, timeout: float = 30.0) -> bytes | None:
    """Return cover art bytes for an album, or None when nothing matches."""
    artist = (artist or "").strip()
    album = (album or "").strip()
    if not artist or not album:
        return None

    query = urllib.parse.urlencode(
        {"term": f"{artist} {album}", "entity": "album", "limit": 10}
    )
    try:
        data = _get_json(f"{ITUNES_SEARCH}?{query}", timeout=timeout)
    except (urllib.error.URLError, OSError, ValueError):
        logger.exception("itunes search failed for %s — %s", artist, album)
        return None

    results = data.get("results") if isinstance(data, dict) else None
    if not isinstance(results, list) or not results:
        return None
    # Require both an exact artist and an album match (an edition suffix on
    # either side is tolerated). Never fall back to a different artist.
    candidates = [
        item
        for item in results
        if _artist_match(item, artist) and _album_match(item, album) > 0
    ]
    if not candidates:
        return None
    best = max(candidates, key=lambda item: _album_match(item, album))
    url = _large_artwork_url(
        (best.get("artworkUrl100") or best.get("artworkUrl60") or "").strip()
    )
    if not url:
        return None
    try:
        raw, content_type = _request(url, timeout=timeout)
    except (urllib.error.URLError, OSError):
        logger.exception("itunes artwork download failed: %s", url)
        return None
    if not _looks_like_image(raw, content_type):
        return None
    return raw


def throttle(sec: float = 0.35) -> None:
    time.sleep(sec)
