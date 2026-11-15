"""Atomic publishes stay inside the library."""

from __future__ import annotations

from pathlib import Path

import pytest

from musik_plugin.errors import PluginError
from musik_plugin.paths import AtomicFile, contained_path, validate_relative_path


def test_publish_replaces_the_part_file(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    atomic = AtomicFile(library, "imports/demo/song.mp3")
    handle = atomic.open()
    handle.write(b"audio")
    atomic.publish()
    final = library / "imports" / "demo" / "song.mp3"
    part = library / "imports" / "demo" / "song.mp3.part"
    assert final.read_bytes() == b"audio"
    assert final.stat().st_mode & 0o600 == 0o600
    assert not part.exists()
    assert contained_path(library, "imports/demo/song.mp3") == final.resolve()


def test_abort_removes_the_part_file(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    atomic = AtomicFile(library, "imports/demo/song.mp3")
    handle = atomic.open()
    handle.write(b"partial")
    handle.flush()
    part = Path(atomic.part)
    assert part.is_file()
    atomic.abort()
    assert not part.exists()
    assert not (library / "imports" / "demo" / "song.mp3").exists()


def test_symlink_escape_writes_nothing(tmp_path: Path) -> None:
    library = tmp_path / "library"
    outside = tmp_path / "outside"
    (library / "imports").mkdir(parents=True)
    outside.mkdir()
    (library / "imports" / "demo").symlink_to(outside)
    with pytest.raises(PluginError):
        AtomicFile(library, "imports/demo/song.mp3").open()
    assert list(outside.iterdir()) == []


def test_parent_traversal_is_invalid() -> None:
    with pytest.raises(PluginError):
        validate_relative_path("imports/../../outside.mp3")
