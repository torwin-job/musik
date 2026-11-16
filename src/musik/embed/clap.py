"""CLAP audio encoder: three windows → mean L2-normalized embedding."""

from __future__ import annotations

import logging
import threading
from functools import lru_cache
from pathlib import Path

import numpy as np

from musik.embed.segments import (
    DEFAULT_SEGMENT_SEC,
    DEFAULT_SPAN_SEC,
    DEFAULT_SR,
    load_segment_audio,
)

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


def model_window_samples(model_id: str = DEFAULT_CLAP_MODEL) -> int:
    """
    Audio samples the CLAP feature extractor keeps per input.

    This matters for correctness, not just for speed: the extractor of
    `larger_clap_music_and_speech` is configured with `max_length_s=10` and
    `truncation="rand_trunc"`, so anything longer is cropped to a **random**
    10s chunk. Feeding exactly this many samples means no cropping happens and
    the embedding becomes reproducible.
    """
    processor, _model, _device = _load_model(model_id)
    extractor = processor.feature_extractor
    nb = getattr(extractor, "nb_max_samples", None)
    if nb:
        return int(nb)
    return int(float(getattr(extractor, "max_length_s", 10.0)) * int(extractor.sampling_rate))


def _trim_to_model_window(y: np.ndarray, *, model_id: str) -> np.ndarray:
    """Cut a window down to the extractor's input length (no-op when shorter)."""
    limit = model_window_samples(model_id)
    if y.size > limit:
        return y[:limit]
    return y


def embed_waveforms(ys: list[np.ndarray], *, model_id: str = DEFAULT_CLAP_MODEL) -> list[np.ndarray]:
    """
    Embed several mono waveforms (already at CLAP sample rate) in one pass.

    Mel feature extraction runs on the CPU inside the processor, so it is kept
    outside the inference lock — only the forward pass needs exclusive access to
    the shared model. That is what makes more than one embed worker useful.
    """
    import torch

    processor, model, device = _load_model(model_id)
    if not ys:
        return []
    ys = [_trim_to_model_window(np.asarray(y, dtype=np.float32), model_id=model_id) for y in ys]
    # transformers>=5: kw is `audio` (plural `audios` raises).
    inputs = processor(
        audio=ys,
        sampling_rate=DEFAULT_SR,
        return_tensors="pt",
        padding=True,
    )
    inputs = {k: v.to(device) for k, v in inputs.items() if torch.is_tensor(v)}
    with _INFER_LOCK:
        with torch.no_grad():
            # get_audio_features returns BaseModelOutputWithPooling —
            # take pooler_output (already L2-normed 512-d).
            out = model.get_audio_features(**inputs)
    if hasattr(out, "pooler_output") and out.pooler_output is not None:
        feats = out.pooler_output
    else:
        feats = out
    arr = feats.detach().cpu().float().numpy()
    vectors = []
    for i in range(arr.shape[0]):
        vec = arr[i].reshape(-1)
        if vec.size < 32:
            raise RuntimeError(f"CLAP returned unexpected embedding size {vec.size}")
        vectors.append(_l2_normalize(vec))
    return vectors


def embed_texts(texts: list[str], *, model_id: str = DEFAULT_CLAP_MODEL) -> list[np.ndarray]:
    """Embed text prompts into the same space as the audio embeddings."""
    import torch

    processor, model, device = _load_model(model_id)
    if not texts:
        return []
    inputs = processor(text=texts, return_tensors="pt", padding=True)
    inputs = {k: v.to(device) for k, v in inputs.items() if torch.is_tensor(v)}
    with _INFER_LOCK:
        with torch.no_grad():
            out = model.get_text_features(**inputs)
    if hasattr(out, "pooler_output") and out.pooler_output is not None:
        out = out.pooler_output
    arr = out.detach().cpu().float().numpy()
    return [_l2_normalize(arr[i].reshape(-1)) for i in range(arr.shape[0])]


def embed_waveform(y: np.ndarray, *, model_id: str = DEFAULT_CLAP_MODEL) -> np.ndarray:
    """Embed a single mono waveform (already at CLAP sample rate)."""
    return embed_waveforms([y], model_id=model_id)[0]


def embed_file(
    path: Path,
    *,
    model_id: str = DEFAULT_CLAP_MODEL,
    segment_sec: float = DEFAULT_SEGMENT_SEC,
    span_sec: float = DEFAULT_SPAN_SEC,
) -> np.ndarray:
    """
    Listen to start / middle / end, embed every window in one batch,
    average and L2-normalize → one track vector.

    Each listening point is covered by windows of the model's own input length,
    so the whole span is heard instead of one chunk cropped out of it at random.
    """
    segments = load_segment_audio(
        path, sample_rate=DEFAULT_SR, segment_sec=segment_sec, span_sec=span_sec
    )
    if not segments:
        raise ValueError(f"No audio segments loaded from {path}")

    for win, y in segments:
        logger.debug("%s window=%s offset=%.1fs samples=%d", path.name, win.name, win.offset_sec, y.size)

    vectors = embed_waveforms([y for _win, y in segments], model_id=model_id)
    mean = np.mean(np.stack(vectors, axis=0), axis=0)
    return _l2_normalize(mean)
