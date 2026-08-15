from __future__ import annotations

import logging
from pathlib import Path

from musik.config import Settings, get_settings

logger = logging.getLogger(__name__)


def iter_audio_files(root: Path | None = None, settings: Settings | None = None) -> list[Path]:
    settings = settings or get_settings()
    root = (root or settings.library).expanduser().resolve()
    if not root.exists():
        raise FileNotFoundError(f"Library path not found: {root}")
    files: list[Path] = []
    exts = {e.lower() for e in settings.extensions}
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix.lower() not in exts:
            continue
        # skip hidden / appledouble
        if any(part.startswith(".") for part in path.parts):
            continue
        files.append(path)
    files.sort()
    logger.info("Found %s audio files under %s", len(files), root)
    return files
