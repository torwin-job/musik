"""Split a combined artist credit into collaborator segments.

Only explicit separators are used — a slash or semicolon between names, or a
``feat.``/``ft.`` marker. ``&`` and the Russian "и" are intentionally NOT
separators: they are part of many solo/band names ("Simon & Garfunkel",
"Король и Шут"), and a substring guess ("Би-2" inside "Би-2 & Сплин") pulls in
another release. Splitting is done once, during the scan, and the segments are
written to the database; the UI just shows what the API returns.
"""

from __future__ import annotations

import re

# "Thomas / БИ-2 / Сплин", "A;B", "Artist feat. Other". Spaces around a slash
# are optional; feat./ft. require surrounding spaces so "Fitz" is never cut.
_SEP_RE = re.compile(r"\s*[/;]\s*|\s+(?:feat\.?|ft\.?)\s+", re.IGNORECASE)


def split_collaborators(artist: str | None) -> list[str]:
    """Return the collaborator segments a track should be listed under.

    A label without an explicit separator stays whole. Empty pieces are
    dropped and duplicates are removed, preserving order (case-insensitive).
    """
    whole = (artist or "").strip()
    if not whole:
        return []
    parts = [p.strip() for p in _SEP_RE.split(whole)]
    parts = [p for p in parts if p]
    if len(parts) < 2:
        return [whole]
    out: list[str] = []
    seen: set[str] = set()
    for part in parts:
        key = part.casefold()
        if key in seen:
            continue
        seen.add(key)
        out.append(part)
    return out
