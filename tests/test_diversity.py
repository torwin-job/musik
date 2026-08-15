from __future__ import annotations

import numpy as np

from musik.brain.generators import _interleave, _pick_far
from musik.index.brute import EmbeddingIndex


def test_pick_far_and_interleave():
    rng = np.random.default_rng(0)
    n, d = 40, 16
    mat = rng.normal(size=(n, d)).astype(np.float32)
    mat /= np.linalg.norm(mat, axis=1, keepdims=True)
    meta = [
        {"id": i, "artist": f"A{i % 7}", "title": f"T{i}", "path": "", "file_md5": str(i)}
        for i in range(n)
    ]
    idx = EmbeddingIndex(
        track_ids=np.arange(n, dtype=np.int64),
        matrix=mat,
        meta=meta,
        md5s=[str(i) for i in range(n)],
    )
    q = idx.centroid()
    sims = idx.sims_to_vector(q)
    near = list(np.argsort(-sims)[:10])
    far = _pick_far(idx, sims, k=3, forbidden=set(near))
    assert len(far) == 3
    assert not set(far) & set(near)
    # far should be on average less similar than near
    assert float(sims[far].mean()) < float(sims[near].mean())
    merged = _interleave(near, far)
    assert len(merged) == 13
    assert sum(1 for _, ex in merged if ex) == 3
