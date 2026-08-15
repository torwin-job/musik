from __future__ import annotations

import logging
import subprocess
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)


def compute_fingerprint(path: Path) -> str | None:
    """Chromaprint fingerprint via fpcalc (system) or pyacoustid."""
    try:
        import acoustid  # type: ignore

        duration, fp = acoustid.fingerprint_file(str(path))
        _ = duration
        if isinstance(fp, bytes):
            return fp.decode("ascii", errors="ignore")
        return str(fp) if fp else None
    except Exception:
        pass

    try:
        proc = subprocess.run(
            ["fpcalc", "-raw", str(path)],
            capture_output=True,
            text=True,
            timeout=120,
            check=False,
        )
        if proc.returncode == 0:
            for line in proc.stdout.splitlines():
                if line.startswith("FINGERPRINT="):
                    return line.split("=", 1)[1].strip() or None
    except FileNotFoundError:
        logger.debug("fpcalc not installed — fingerprint skipped")
    except Exception as exc:
        logger.warning("fingerprint failed for %s: %s", path, exc)
    return None


def compute_lufs(path: Path, max_seconds: float = 60.0) -> float | None:
    """EBU R128 integrated loudness via pyloudnorm."""
    try:
        import librosa
        import pyloudnorm as pyln
        import soundfile as sf

        # Prefer soundfile; fall back to librosa load
        try:
            info = sf.info(str(path))
            sr = int(info.samplerate)
            frames = int(min(info.frames, max_seconds * sr))
            audio, _ = sf.read(str(path), frames=frames, dtype="float64", always_2d=True)
            audio = np.mean(audio, axis=1)
        except Exception:
            audio, sr = librosa.load(str(path), sr=None, mono=True, duration=max_seconds)
            audio = audio.astype(np.float64)

        if audio.size < sr * 0.5:
            return None
        meter = pyln.Meter(sr)
        loudness = float(meter.integrated_loudness(audio))
        if np.isnan(loudness) or np.isinf(loudness):
            return None
        return loudness
    except Exception as exc:
        logger.warning("LUFS failed for %s: %s", path, exc)
        return None


def compute_bpm(path: Path, max_seconds: float = 60.0) -> float | None:
    """BPM via librosa beat_track (madmom optional later)."""
    try:
        import librosa

        y, sr = librosa.load(str(path), sr=22050, mono=True, duration=max_seconds)
        tempo, _ = librosa.beat.beat_track(y=y, sr=sr)
        # librosa may return ndarray
        if hasattr(tempo, "__len__"):
            tempo = float(np.asarray(tempo).ravel()[0])
        return float(tempo)
    except Exception as exc:
        logger.warning("BPM failed for %s: %s", path, exc)
        return None


def compute_key_mode(path: Path, max_seconds: float = 45.0) -> tuple[str | None, str | None]:
    """
    Key/mode extraction.
    Prefers essentia if installed; otherwise chroma-based heuristic via librosa.
    """
    try:
        import essentia.standard as es  # type: ignore

        audio = es.MonoLoader(filename=str(path), sampleRate=44100)()
        # Truncate
        max_samples = int(44100 * max_seconds)
        if audio.shape[0] > max_samples:
            audio = audio[:max_samples]
        key, scale, _strength = es.KeyExtractor()(audio)
        return str(key), str(scale)
    except Exception:
        pass

    try:
        import librosa

        y, sr = librosa.load(str(path), sr=22050, mono=True, duration=max_seconds)
        chroma = librosa.feature.chroma_cqt(y=y, sr=sr)
        chroma_mean = chroma.mean(axis=1)
        idx = int(np.argmax(chroma_mean))
        keys = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"]
        # Crude major/minor: compare major vs minor triad energy
        major = chroma_mean[idx] + chroma_mean[(idx + 4) % 12] + chroma_mean[(idx + 7) % 12]
        minor = chroma_mean[idx] + chroma_mean[(idx + 3) % 12] + chroma_mean[(idx + 7) % 12]
        mode = "major" if major >= minor else "minor"
        return keys[idx], mode
    except Exception as exc:
        logger.warning("key extract failed for %s: %s", path, exc)
        return None, None
