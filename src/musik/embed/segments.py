"""Load three fixed windows from a track: start / middle / end."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import librosa
import numpy as np

# Strategy id — bump when windowing changes so cache invalidates.
SEGMENT_STRATEGY = "clap_3x30_v1"
DEFAULT_SEGMENT_SEC = 30.0
DEFAULT_SR = 48_000


@dataclass(frozen=True)
class SegmentWindow:
    name: str
    offset_sec: float
    duration_sec: float


def plan_windows(duration_sec: float, segment_sec: float = DEFAULT_SEGMENT_SEC) -> list[SegmentWindow]:
    """
    Plan up to three non-identical windows.

    - short track (<= segment): whole file once
    - otherwise: start, middle (centered), end
    - drop duplicates when offsets collapse on short songs
    """
    if duration_sec <= 0:
        return []

    if duration_sec <= segment_sec + 0.05:
        return [SegmentWindow("full", 0.0, duration_sec)]

    candidates = [
        SegmentWindow("start", 0.0, segment_sec),
        SegmentWindow(
            "middle",
            max(0.0, duration_sec / 2.0 - segment_sec / 2.0),
            segment_sec,
        ),
        SegmentWindow("end", max(0.0, duration_sec - segment_sec), segment_sec),
    ]

    unique: list[SegmentWindow] = []
    for win in candidates:
        if any(abs(win.offset_sec - u.offset_sec) < 0.5 for u in unique):
            continue
        unique.append(win)
    return unique


def load_segment_audio(
    path: Path,
    *,
    sample_rate: int = DEFAULT_SR,
    segment_sec: float = DEFAULT_SEGMENT_SEC,
) -> list[tuple[SegmentWindow, np.ndarray]]:
    """Load mono float32 arrays for each planned window."""
    duration = float(librosa.get_duration(path=str(path)))
    windows = plan_windows(duration, segment_sec=segment_sec)
    out: list[tuple[SegmentWindow, np.ndarray]] = []
    for win in windows:
        y, _ = librosa.load(
            str(path),
            sr=sample_rate,
            mono=True,
            offset=win.offset_sec,
            duration=win.duration_sec,
        )
        y = np.asarray(y, dtype=np.float32)
        if y.size == 0:
            continue
        out.append((win, y))
    return out
