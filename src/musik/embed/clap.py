"""CLAP audio encoder: three windows → mean L2-normalized embedding."""

from __future__ import annotations

import logging
import threading
from functools import lru_cache
from pathlib import Path

import numpy as np

from musik.embed.segments import DEFAULT_SEGMENT_SEC, DEFAULT_SR, load_segment_audio

logger = logging.getLogger(__name__)

# Music-oriented CLAP from LAION (HuggingFace transformers).
DEFAULT_CLAP_MODEL = "laion/larger_clap_music_and_speech"
_INFER_LOCK = threading.Lock()


def device_info() -> tuple[str, bool]:
    """Return (device_name, has_cuda)."""
    try:
        import torch

        if torch.cuda.is_available():
            name = torch.cuda.get_device_name(0)
            return f"cuda ({name})", True
    except Exception:
        pass
    return "cpu", False


@lru_cache(maxsize=1)
def _load_model(model_id: str):
    import torch
    from transformers import ClapModel, ClapProcessor

    device = "cuda" if torch.cuda.is_available() else "cpu"
    logger.info("Loading CLAP model %s on %s", model_id, device)
    try:
        processor = ClapProcessor.from_pretrained(model_id, local_files_only=True)
        model = ClapModel.from_pretrained(model_id, local_files_only=True)
    except (OSError, ValueError):
        processor = ClapProcessor.from_pretrained(model_id)
        model = ClapModel.from_pretrained(model_id)
    model.eval()
    model.to(device)
    return processor, model, device


def _l2_normalize(vec: np.ndarray) -> np.ndarray:
    n = float(np.linalg.norm(vec))
    if n < 1e-12:
        return vec.astype(np.float32)
    return (vec / n).astype(np.float32)


def embed_waveform(y: np.ndarray, *, model_id: str = DEFAULT_CLAP_MODEL) -> np.ndarray:
    """Embed a single mono waveform (already at CLAP sample rate)."""
    import torch

    processor, model, device = _load_model(model_id)
    # transformers>=5: kw is `audio` (plural `audios` raises);
    # get_audio_features returns BaseModelOutputWithPooling — take pooler_output (already L2-normed 512-d).
    with _INFER_LOCK:
        inputs = processor(
            audio=[y],
            sampling_rate=DEFAULT_SR,
            return_tensors="pt",
            padding=True,
        )
        inputs = {k: v.to(device) for k, v in inputs.items() if torch.is_tensor(v)}
        with torch.no_grad():
            out = model.get_audio_features(**inputs)
            if hasattr(out, "pooler_output") and out.pooler_output is not None:
                feats = out.pooler_output
            else:
                feats = out
        vec = feats[0].detach().cpu().float().numpy().reshape(-1)
    if vec.size < 32:
        raise RuntimeError(f"CLAP returned unexpected embedding size {vec.size}")
    return _l2_normalize(vec)


def embed_file(
    path: Path,
    *,
    model_id: str = DEFAULT_CLAP_MODEL,
    segment_sec: float = DEFAULT_SEGMENT_SEC,
) -> np.ndarray:
    """
    Listen to start / middle / end (default 30s each), embed each window,
    average and L2-normalize → one track vector.
    """
    segments = load_segment_audio(path, sample_rate=DEFAULT_SR, segment_sec=segment_sec)
    if not segments:
        raise ValueError(f"No audio segments loaded from {path}")

    vectors: list[np.ndarray] = []
    for win, y in segments:
        logger.debug("%s window=%s offset=%.1fs samples=%d", path.name, win.name, win.offset_sec, y.size)
        vectors.append(embed_waveform(y, model_id=model_id))

    mean = np.mean(np.stack(vectors, axis=0), axis=0)
    return _l2_normalize(mean)
