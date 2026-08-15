from __future__ import annotations

import numpy as np

from musik.brain.generators import _mmr_select
from musik.index.brute import EmbeddingIndex


def test_mmr_respects_size():
    rng = np.random.default_rng(0)
    n, d = 20, 16
    mat = rng.normal(size=(n, d)).astype(np.float32)
    mat /= np.linalg.norm(mat, axis=1, keepdims=True)
    meta = [{"id": i, "artist": f"A{i % 5}", "title": f"T{i}", "path": "", "file_md5": str(i)} for i in range(n)]
    idx = EmbeddingIndex(
        track_ids=np.arange(n, dtype=np.int64),
        matrix=mat,
        meta=meta,
        md5s=[str(i) for i in range(n)],
    )
    q = idx.centroid()
    sims = idx.sims_to_vector(q)
    picked = _mmr_select(idx, sims, size=8)
    assert len(picked) == 8
    assert len(set(picked)) == 8
