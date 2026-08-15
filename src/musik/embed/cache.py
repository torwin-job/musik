"""File cache for embeddings keyed by file MD5 + model + segment strategy."""

from __future__ import annotations

from pathlib import Path

import numpy as np

from musik.config import get_settings
from musik.embed.segments import SEGMENT_STRATEGY


def cache_path(file_md5: str, model_id: str) -> Path:
    settings = get_settings()
    safe_model = model_id.replace("/", "__")
    name = f"{file_md5}.{safe_model}.{SEGMENT_STRATEGY}.npy"
    return settings.embeddings_cache / name


def load_cached(file_md5: str, model_id: str) -> np.ndarray | None:
    path = cache_path(file_md5, model_id)
    if not path.exists():
        return None
    arr = np.load(path)
    return np.asarray(arr, dtype=np.float32)


def save_cached(file_md5: str, model_id: str, embedding: np.ndarray) -> Path:
    path = cache_path(file_md5, model_id)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp.npy")
    np.save(tmp, np.asarray(embedding, dtype=np.float32))
    tmp.replace(path)
    return path
