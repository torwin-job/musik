from __future__ import annotations

import numpy as np

from musik.db import ensure_db, save_embedding, upsert_track
from musik.index.brute import EmbeddingIndex, load_index
from musik.index.clusters import _kmeans


def test_neighbors_self_excluded(tmp_path, monkeypatch):
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("MUSIK_DB_PATH", str(tmp_path / "data" / "db" / "t.db"))
    monkeypatch.setenv("MUSIK_EMBEDDINGS_CACHE", str(tmp_path / "data" / "cache" / "e"))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(tmp_path / "data" / "cache" / "a"))
    from musik.config import get_settings

    get_settings.cache_clear()
    ensure_db()

    # three nearly-orthogonal / similar unit vectors
    rng = np.random.default_rng(0)
    base = rng.normal(size=32).astype(np.float32)
    base /= np.linalg.norm(base)
    near = base + 0.05 * rng.normal(size=32).astype(np.float32)
    near /= np.linalg.norm(near)
    far = rng.normal(size=32).astype(np.float32)
    far /= np.linalg.norm(far)

    ids = []
    for i, (vec, md5) in enumerate([(base, "aaa"), (near, "bbb"), (far, "ccc")]):
        path = str(tmp_path / f"t{i}.wav")
        tid = upsert_track(
            {
                "path": path,
                "file_md5": md5,
                "file_mtime": 0.0,
                "file_size": 1,
                "title": f"T{i}",
                "artist": "A",
                "album": None,
                "year": None,
                "track_number": i,
                "duration": 1.0,
                "bitrate": 128,
                "sample_rate": 44100,
                "channels": 2,
                "fingerprint": None,
                "lufs": None,
                "artwork_path": None,
            }
        )
        save_embedding(tid, vec)
        ids.append(tid)

    idx = load_index()
    assert idx.size == 3
    nb = idx.neighbors(ids[0], k=2)
    assert nb[0].track_id == ids[1]
    assert nb[0].cosine > nb[1].cosine
    get_settings.cache_clear()


def test_kmeans_labels():
    rng = np.random.default_rng(1)
    a = rng.normal(size=(20, 8)).astype(np.float32)
    a /= np.linalg.norm(a, axis=1, keepdims=True)
    labels, centroids, inertia = _kmeans(a, k=3, seed=1)
    assert labels.shape == (20,)
    assert centroids.shape == (3, 8)
    assert 0.0 <= inertia <= 2.0
