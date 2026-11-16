"""2D library map: UMAP over CLAP embeddings plus CLAP-text mood labels.

The map is a derived artifact that is always rebuilt and read whole, so it
lives as one JSON value in scan_state['library_map'] instead of its own table.
It covers every ready track; the player drops other profiles' private tracks
when it serves the map. Only what needs the embeddings or the CLAP text tower
is computed here (layout, moods, sound neighbours); plays, favourites,
transitions and k-means clusters already live in the database and the player
reads them per profile.
"""

from __future__ import annotations

import json
import logging
from collections import Counter
from dataclasses import dataclass

import numpy as np

from musik.db.schema import connect, utcnow

log = logging.getLogger(__name__)

MAP_KEY = "library_map"
MAP_VERSION = 2
LABEL_MIX_SIZE = 50
MAX_GENRES = 40
NEIGHBORS = 3
# A mood with fewer of its own tracks than this share of the library is dropped.
MIN_MOOD_SHARE = 0.01

# (id, label, colour, CLAP prompt). English prompts: the text tower of
# larger_clap_music_and_speech was trained on English captions.
MOODS: tuple[tuple[str, str, str, str], ...] = (
    ("workout", "Тренировка", "#e4675a", "energetic intense workout music with a driving beat"),
    ("party", "Вечеринка", "#d97bb0", "upbeat dance party music"),
    ("dance", "Танцы", "#e58fd0", "electronic club dance music with a four on the floor beat"),
    ("happy", "Радостное", "#e2b13c", "happy cheerful uplifting music"),
    ("romance", "Романтика", "#e88a8a", "romantic tender love song"),
    ("road", "Дорога", "#d9a05b", "driving road trip rock music"),
    ("epic", "Эпик", "#c96b4a", "epic orchestral cinematic music with choir and drums"),
    ("heavy", "Драйв", "#c9824a", "loud distorted electric guitar rock music"),
    ("rage", "Агрессия", "#b8443a", "aggressive angry heavy metal with screaming vocals"),
    ("focus", "Фокус", "#7fb27a", "calm instrumental music for focus and concentration"),
    ("chill", "Чилл", "#5fb3a8", "chill relaxed laid-back lo-fi beats"),
    ("night", "Ночь", "#6f7fd0", "dark atmospheric night city synth music"),
    ("meditation", "Медитация", "#8fc0b0", "slow meditative drone music with no beat"),
    ("sleep", "Сон", "#a28ad8", "soft quiet lullaby for sleep"),
    ("sad", "Грустное", "#5a9fd8", "sad slow piano music"),
    ("melancholy", "Меланхолия", "#7a95b8", "melancholic nostalgic song with vocals"),
)


@dataclass
class MapResult:
    n: int
    method: str
    labels: int
    genres: int


def _load_vectors() -> tuple[np.ndarray, np.ndarray]:
    """All ready embeddings regardless of profile (the player filters privacy)."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT t.id, f.embedding, f.embedding_dim
            FROM tracks t
            JOIN features f ON f.track_id = t.id
            WHERE t.is_active = 1
              AND t.is_duplicate_of IS NULL
              AND f.status = 'ready'
              AND f.embedding IS NOT NULL
            ORDER BY t.id
            """
        ).fetchall()
    if not rows:
        return np.zeros(0, dtype=np.int64), np.zeros((0, 0), dtype=np.float32)
    vectors = []
    for r in rows:
        vec = np.frombuffer(r["embedding"], dtype=np.float32)
        dim = int(r["embedding_dim"] or 0)
        if dim and vec.size > dim:
            vec = vec[:dim]
        vectors.append(vec)
    dim = max(v.size for v in vectors)
    mat = np.zeros((len(vectors), dim), dtype=np.float32)
    for i, v in enumerate(vectors):
        mat[i, : v.size] = v
    norms = np.linalg.norm(mat, axis=1, keepdims=True)
    mat /= np.maximum(norms, 1e-12)
    ids = np.array([int(r["id"]) for r in rows], dtype=np.int64)
    return ids, mat


def _track_genres(ids: np.ndarray) -> tuple[list[str], list[int]]:
    """One genre per track: of its tags, the one most common in the library."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT tg.track_id, g.name
            FROM track_genres tg
            JOIN genres g ON g.id = tg.genre_id
            """
        ).fetchall()
    wanted = set(ids.tolist())
    by_track: dict[int, list[str]] = {}
    counts: Counter[str] = Counter()
    for r in rows:
        tid = int(r["track_id"])
        name = (r["name"] or "").strip()
        if tid not in wanted or not name:
            continue
        by_track.setdefault(tid, []).append(name)
        counts[name] += 1
    genres = [name for name, _ in counts.most_common(MAX_GENRES)]
    slot = {name: i for i, name in enumerate(genres)}
    picks = []
    for tid in ids.tolist():
        names = [n for n in by_track.get(tid, []) if n in slot]
        picks.append(slot[max(names, key=lambda n: counts[n])] if names else -1)
    return genres, picks


def _pca(X: np.ndarray) -> np.ndarray:
    if X.shape[0] < 2:
        return np.zeros((X.shape[0], 2), dtype=np.float32)
    centered = X - X.mean(axis=0, keepdims=True)
    _u, _s, vt = np.linalg.svd(centered, full_matrices=False)
    return centered @ vt[:2].T


def _project(X: np.ndarray, *, seed: int) -> tuple[np.ndarray, str]:
    n = X.shape[0]
    if n >= 16:
        try:
            import umap
        except ImportError:
            log.warning("umap-learn is not installed; library map falls back to PCA")
        else:
            reducer = umap.UMAP(
                n_neighbors=min(15, n - 1),
                min_dist=0.1,
                metric="cosine",
                # A fixed seed keeps the layout stable between rebuilds, at
                # the cost of a single thread (minutes on 50k tracks).
                random_state=seed,
                n_jobs=1,
            )
            return np.asarray(reducer.fit_transform(X), dtype=np.float32), "umap"
    return _pca(X).astype(np.float32), "pca"


def _normalize(xy: np.ndarray) -> np.ndarray:
    """Fit into [0, 1]² keeping the aspect ratio, centred."""
    if xy.shape[0] == 0:
        return xy
    lo = xy.min(axis=0)
    span = xy.max(axis=0) - lo
    scale = float(span.max())
    if scale < 1e-12:
        return np.full_like(xy, 0.5)
    out = (xy - lo) / scale
    out += (1.0 - span / scale) / 2.0
    return out


def _mood_labels(X: np.ndarray, ids: np.ndarray, xy: np.ndarray) -> list[dict]:
    """Give every track one mood, then label each mood where its tracks gather."""
    if X.shape[0] < 8:
        return []
    try:
        from musik.embed.clap import embed_texts

        T = np.stack(embed_texts([prompt for *_rest, prompt in MOODS]))
    except Exception as exc:  # noqa: BLE001
        log.warning("library map: CLAP text labels unavailable: %s", exc)
        return []
    if T.shape[1] != X.shape[1]:
        log.warning("library map: text dim %s != audio dim %s", T.shape[1], X.shape[1])
        return []
    sims = X @ T.T
    # CLAP text/audio cosines sit in a narrow band and differ by prompt, so
    # compare moods on a standardized score. Ranking every mood on its own
    # sent all the calm ones to the same quiet tracks; picking each track's
    # best mood keeps them apart.
    z = (sims - sims.mean(axis=0)) / np.maximum(sims.std(axis=0), 1e-6)
    own = np.argmax(z, axis=1)
    n = X.shape[0]
    min_size = max(5, int(n * MIN_MOOD_SHARE))
    labels = []
    for j, (mood_id, name, color, _prompt) in enumerate(MOODS):
        members = np.flatnonzero(own == j)
        if members.size < min_size:
            continue
        members = members[np.argsort(-z[members, j])]
        pts = xy[members[: min(members.size, 80)]]
        # The best matches can sit on two islands; a plain mean would land
        # between them. Anchor on the point with the most close neighbours.
        dist = np.linalg.norm(pts[:, None, :] - pts[None, :, :], axis=2)
        seed = int(np.argmax((dist < 0.08).sum(axis=1)))
        near = pts[dist[seed] < 0.12]
        x, y = near.mean(axis=0)
        labels.append(
            {
                "id": mood_id,
                "name": name,
                "color": color,
                "x": round(float(x), 4),
                "y": round(float(y), 4),
                "track_ids": [int(t) for t in ids[members[:LABEL_MIX_SIZE]]],
            }
        )
    return labels


def _neighbors(X: np.ndarray, *, k: int = NEIGHBORS, chunk: int = 1024) -> np.ndarray:
    """Row indices of each track's k closest tracks by cosine, -1 when fewer exist."""
    n = X.shape[0]
    out = np.full((n, k), -1, dtype=np.int64)
    if n < 2:
        return out
    kk = min(k, n - 1)
    for start in range(0, n, chunk):
        sims = X[start : start + chunk] @ X.T
        rows = np.arange(sims.shape[0])
        sims[rows, start + rows] = -np.inf
        part = np.argpartition(-sims, kk - 1, axis=1)[:, :kk]
        order = np.argsort(-sims[rows[:, None], part], axis=1)
        out[start : start + sims.shape[0], :kk] = part[rows[:, None], order]
    return out


def build_library_map(*, seed: int = 42) -> MapResult:
    ids, X = _load_vectors()
    if ids.size:
        xy, method = _project(X, seed=seed)
        xy = _normalize(xy)
    else:
        xy, method = np.zeros((0, 2), dtype=np.float32), "none"
    genres, picks = _track_genres(ids)
    labels = _mood_labels(X, ids, xy)
    nbr = _neighbors(X) if ids.size else np.zeros((0, NEIGHBORS), dtype=np.int64)
    nbr_ids = np.where(nbr >= 0, ids[np.maximum(nbr, 0)], -1) if ids.size else nbr
    doc = {
        "version": MAP_VERSION,
        "method": method,
        "updated_at": utcnow(),
        "genres": genres,
        # [track_id, x, y, genre index or -1, neighbour track ids...]
        "points": [
            [int(tid), round(float(x), 4), round(float(y), 4), int(g), *map(int, nb)]
            for tid, (x, y), g, nb in zip(ids.tolist(), xy.tolist(), picks, nbr_ids.tolist(), strict=True)
        ],
        "labels": labels,
    }
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO scan_state(key, value) VALUES(:key, :value)
            ON CONFLICT(key) DO UPDATE SET value = excluded.value
            """,
            {"key": MAP_KEY, "value": json.dumps(doc, ensure_ascii=False, separators=(",", ":"))},
        )
    log.info("library map: %d points via %s, %d labels", ids.size, method, len(labels))
    return MapResult(n=int(ids.size), method=method, labels=len(labels), genres=len(genres))
