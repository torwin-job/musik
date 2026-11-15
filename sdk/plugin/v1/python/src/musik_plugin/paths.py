"""Library-relative paths and atomic ``.part`` publishes."""

from __future__ import annotations

import os
from pathlib import Path
from typing import BinaryIO

from musik_plugin.errors import CODE_INVALID_REQUEST, CODE_WRITE_FAILED, PluginError
from musik_plugin.protocol import (
    MAX_PATH_CHARS,
    RELATIVE_DOT_RE,
    RELATIVE_PART_RE,
    RELATIVE_SEGMENT_RE,
)


def validate_relative_path(value: object) -> str:
    """Return a library-relative path that uses the v1 path grammar."""
    if (
        not isinstance(value, str)
        or len(value) > MAX_PATH_CHARS
        or RELATIVE_SEGMENT_RE.fullmatch(value) is None
        or RELATIVE_DOT_RE.search(value) is not None
        or RELATIVE_PART_RE.search(value) is not None
    ):
        raise PluginError(CODE_INVALID_REQUEST, "path is invalid")
    return value


def join_relative(parent: str, name: str) -> str:
    """Join two relative paths and apply the same grammar to the result."""
    return validate_relative_path(
        f"{validate_relative_path(parent)}/{validate_relative_path(name)}"
    )


def contained_path(library: Path, relative: str) -> Path:
    """Resolve ``relative`` and require it to stay inside ``library``."""
    relative = validate_relative_path(relative)
    root = Path(library).resolve()
    if not root.is_dir():
        raise PluginError(CODE_WRITE_FAILED, "library directory is missing")
    candidate = (root / relative).resolve()
    try:
        candidate.relative_to(root)
    except ValueError as exc:
        raise PluginError(CODE_INVALID_REQUEST, "path is outside the library") from exc
    if candidate == root:
        raise PluginError(CODE_INVALID_REQUEST, "path is invalid")
    return candidate


class AtomicFile:
    """Write a file as ``name.part`` and rename it into place.

    The ``.part`` file lives in the destination directory, so the rename stays
    on one filesystem. Failure and cancellation remove the partial file.
    """

    def __init__(self, library: Path, relative: str) -> None:
        self.relative = validate_relative_path(relative)
        self.library = Path(library)
        self.final = contained_path(self.library, self.relative)
        self.part = self.final.with_name(self.final.name + ".part")
        self._fh: BinaryIO | None = None
        self._published = False

    def open(self) -> BinaryIO:
        try:
            self._create()
        except PluginError:
            self.abort()
            raise
        except OSError as exc:
            self.abort()
            raise PluginError(CODE_WRITE_FAILED, "audio file could not be created") from exc
        if self._fh is None:
            self.abort()
            raise PluginError(CODE_WRITE_FAILED, "audio file could not be created")
        return self._fh

    def publish(self) -> None:
        if self._published:
            return
        contained_path(self.library, self.relative)
        if self._fh is not None and not self._fh.closed:
            self._fh.flush()
            os.fsync(self._fh.fileno())
            self._fh.close()
        os.replace(self.part, self.final)
        self._published = True
        _fsync_directory(self.final.parent)

    def abort(self) -> None:
        if self._fh is not None and not self._fh.closed:
            try:
                self._fh.close()
            except OSError:
                pass
        if self._published:
            return
        try:
            os.unlink(self.part)
        except OSError:
            pass

    def _create(self) -> None:
        parent = self.final.parent
        parent.mkdir(parents=True, exist_ok=True)
        contained_path(self.library, self.relative)
        if self.part.is_symlink() or self.part.exists():
            if self.part.is_dir() and not self.part.is_symlink():
                raise PluginError(CODE_WRITE_FAILED, "temporary file is occupied")
            self.part.unlink()
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        fd = os.open(self.part, flags, 0o644)
        self._fh = os.fdopen(fd, "wb")


def _fsync_directory(directory: Path) -> None:
    flags = os.O_RDONLY
    if hasattr(os, "O_DIRECTORY"):
        flags |= os.O_DIRECTORY
    try:
        fd = os.open(directory, flags)
    except OSError:
        return
    try:
        os.fsync(fd)
    except OSError:
        return
    finally:
        os.close(fd)
