"""Taste profile helpers.

Authority rules:
- context ``global`` — Go EMA online only (player writes; Python must NOT overwrite).
- context ``offline_report`` — Python rebuild from history for CLI/reports.
- ``resolve_taste()`` prefers Go ``global``, then offline snapshot, then fresh rebuild
  without persisting into ``global``.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from musik.db.schema import connect, utcnow
from musik.index.brute import EmbeddingIndex, load_index

CONTEXT_ONLINE = "global"
CONTEXT_OFFLINE = "offline_report"

# Weights for profile update
ACTION_WEIGHT = {
    "finish": 1.0,
    "like": 1.5,
    "start": 0.15,
    "skip": -1.0,
    "dislike": -1.5,
    "track_end": 0.0,  # handled via reason in Go; ignore bare action here
}


@dataclass
class TasteProfile:
    embedding: np.ndarray  # unit vector
    n_positive: int
    n_negative: int
    context: str = CONTEXT_OFFLINE

    @property
    def ready(self) -> bool:
        return self.embedding.size > 0 and self.n_positive > 0


def build_profile(*, context: str = CONTEXT_OFFLINE, persist: bool = True) -> TasteProfile:
    """
    Weighted average of track embeddings by listen actions.
    Never persists into the online ``global`` context (reserved for Go EMA).
    """
    write_context = CONTEXT_OFFLINE if context in (CONTEXT_ONLINE, "", "global") else context

    index = load_index()
    if index.size == 0:
        return TasteProfile(np.zeros(0, dtype=np.float32), 0, 0, write_context)

    with connect() as conn:
        rows = conn.execute(
            """
            SELECT track_id, action FROM listening_history
            ORDER BY id DESC
            LIMIT 500
            """
        ).fetchall()

    if not rows:
        return TasteProfile(index.centroid(), 0, 0, write_context)

    acc = np.zeros(index.dim, dtype=np.float64)
    w_sum = 0.0
    n_pos = 0
    n_neg = 0
    for r in rows:
        w = ACTION_WEIGHT.get(r["action"], 0.0)
        if w == 0.0:
            continue
        row = index.row_of(int(r["track_id"]))
        if row is None:
            continue
        acc += w * index.matrix[row]
        w_sum += abs(w)
        if w > 0:
            n_pos += 1
        else:
            n_neg += 1

    if w_sum < 1e-9 or n_pos == 0:
        return TasteProfile(index.centroid(), n_pos, n_neg, write_context)

    vec = acc.astype(np.float32)
    n = float(np.linalg.norm(vec))
    if n > 1e-12:
        vec = vec / n
    else:
        vec = index.centroid()

    if persist:
        blob = vec.astype(np.float32).tobytes()
        with connect() as conn:
            conn.execute(
                """
                INSERT INTO user_profile_snapshots(context, embedding, created_at)
                VALUES (?,?,?)
                """,
                (write_context, blob, utcnow()),
            )
    return TasteProfile(vec, n_pos, n_neg, write_context)


def latest_profile(context: str = CONTEXT_ONLINE) -> TasteProfile | None:
    with connect() as conn:
        row = conn.execute(
            """
            SELECT embedding FROM user_profile_snapshots
            WHERE context = ?
            ORDER BY id DESC LIMIT 1
            """,
            (context,),
        ).fetchone()
    if not row:
        return None
    vec = np.frombuffer(row["embedding"], dtype=np.float32).copy()
    n = float(np.linalg.norm(vec))
    if n > 1e-12:
        vec = vec / n
    # n_positive unknown from blob alone — mark ready if vector exists
    return TasteProfile(vec, 1, 0, context)


def resolve_taste(index: EmbeddingIndex | None = None) -> tuple[np.ndarray, str]:
    """
    Taste vector for recommendations / tips.
    Prefer Go online snapshot, then offline report, then rebuild (no global write).
    """
    idx = index or load_index()
    online = latest_profile(CONTEXT_ONLINE)
    if online is not None and online.embedding.size == idx.dim:
        return online.embedding.astype(np.float32), "go_ema_global"
    offline = latest_profile(CONTEXT_OFFLINE)
    if offline is not None and offline.embedding.size == idx.dim:
        return offline.embedding.astype(np.float32), "offline_report"
    built = build_profile(context=CONTEXT_OFFLINE, persist=False)
    if built.embedding.size:
        return built.embedding.astype(np.float32), "history_rebuild"
    return idx.centroid(), "library_centroid"


def epsilon_explore_mask(size: int, explore_ratio: float, rng: np.random.Generator) -> np.ndarray:
    """Boolean mask of length `size`: True = exploration slot."""
    n_ex = int(round(size * explore_ratio))
    n_ex = max(0, min(size, n_ex))
    if size >= 5:
        n_ex = max(1, n_ex) if explore_ratio > 0 else 0
    mask = np.zeros(size, dtype=bool)
    if n_ex == 0:
        return mask
    positions = np.linspace(2, size - 1, num=n_ex, dtype=int)
    mask[positions] = True
    if n_ex > 1:
        extras = rng.choice(size, size=min(n_ex, size), replace=False)
        mask[:] = False
        mask[extras] = True
    return mask
