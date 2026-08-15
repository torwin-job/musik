"""Simple k-means over CLAP embeddings; writes features.cluster_id."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from musik.db.schema import connect, utcnow
from musik.index.brute import EmbeddingIndex, load_index


@dataclass
class ClusterResult:
    k: int
    n: int
    inertia: float
    counts: dict[int, int]


def _kmeans(
    X: np.ndarray,
    k: int,
    *,
    max_iter: int = 50,
    seed: int = 42,
) -> tuple[np.ndarray, np.ndarray, float]:
    """Return (labels, centroids, inertia). Centroids are L2-normalized."""
    rng = np.random.default_rng(seed)
    n = X.shape[0]
    k = max(1, min(k, n))
    # init: random distinct rows
    pick = rng.choice(n, size=k, replace=False)
    centroids = X[pick].copy()
    labels = np.zeros(n, dtype=np.int32)

    for _ in range(max_iter):
        # assign by cosine = dot (unit vectors)
        sims = X @ centroids.T  # (n, k)
        new_labels = np.argmax(sims, axis=1).astype(np.int32)
        if np.array_equal(new_labels, labels):
            break
        labels = new_labels
        for c in range(k):
            members = X[labels == c]
            if members.size == 0:
                centroids[c] = X[int(rng.integers(0, n))]
            else:
                mean = members.mean(axis=0)
                nrm = float(np.linalg.norm(mean))
                centroids[c] = mean / nrm if nrm > 1e-12 else mean

    sims = X @ centroids.T
    # inertia as mean (1 - cos_to_centroid)
    cos_own = sims[np.arange(n), labels]
    inertia = float((1.0 - cos_own).mean()) if n else 0.0
    return labels, centroids, inertia


def assign_clusters(k: int = 8, *, seed: int = 42) -> ClusterResult:
    index = load_index()
    if index.size == 0:
        return ClusterResult(k=0, n=0, inertia=0.0, counts={})

    labels, _centroids, inertia = _kmeans(index.matrix, k, seed=seed)
    now = utcnow()
    with connect() as conn:
        for tid, lab in zip(index.track_ids.tolist(), labels.tolist(), strict=True):
            conn.execute(
                "UPDATE features SET cluster_id = ? WHERE track_id = ?",
                (int(lab), int(tid)),
            )
        conn.execute(
            """
            INSERT INTO scan_state(key, value) VALUES('cluster_k', ?)
            ON CONFLICT(key) DO UPDATE SET value = excluded.value
            """,
            (str(int(labels.max()) + 1 if labels.size else 0),),
        )
        conn.execute(
            """
            INSERT INTO scan_state(key, value) VALUES('cluster_updated_at', ?)
            ON CONFLICT(key) DO UPDATE SET value = excluded.value
            """,
            (now,),
        )

    counts: dict[int, int] = {}
    for lab in labels.tolist():
        counts[int(lab)] = counts.get(int(lab), 0) + 1
    return ClusterResult(k=len(counts), n=index.size, inertia=inertia, counts=counts)


def cluster_members(cluster_id: int, *, limit: int = 50) -> list[dict]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT t.id, t.artist, t.title, f.cluster_id
            FROM tracks t
            JOIN features f ON f.track_id = t.id
            WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
              AND f.cluster_id = ?
            ORDER BY t.artist, t.title
            LIMIT ?
            """,
            (cluster_id, limit),
        ).fetchall()
        return [dict(r) for r in rows]


def list_cluster_summary() -> list[dict]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT f.cluster_id AS cluster_id, COUNT(*) AS n,
                   MIN(t.artist) AS sample_artist
            FROM features f
            JOIN tracks t ON t.id = f.track_id
            WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
              AND f.cluster_id IS NOT NULL
            GROUP BY f.cluster_id
            ORDER BY f.cluster_id
            """
        ).fetchall()
        return [dict(r) for r in rows]
