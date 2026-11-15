"""Manifest discovery rules."""

from __future__ import annotations

import json
import stat
from pathlib import Path

import pytest

from musik_plugin.errors import ManifestError
from musik_plugin.manifest import (
    compatible_capabilities,
    conflicting_ids,
    default_plugin_dirs,
    load_manifest,
    manifest_files,
    parse_manifest,
    resolve_executable,
)
from musik_plugin.messages import parse_describe


def _manifest(**overrides):
    document = {
        "id": "example",
        "executable": "bin/plugin",
        "protocol": "1",
        "capabilities": ["library.import_url"],
    }
    document.update(overrides)
    return document


def _describe(**overrides):
    document = {
        "type": "describe",
        "protocol": "1",
        "id": "example",
        "capabilities": ["library.import_url"],
    }
    document.update(overrides)
    return parse_describe(document)


def test_default_directory_uses_home_share(tmp_path: Path) -> None:
    home = tmp_path / "home"
    assert default_plugin_dirs({}, home=home) == (home / ".local" / "share" / "musik" / "plugins",)


def test_xdg_data_home_replaces_the_default(tmp_path: Path) -> None:
    directories = default_plugin_dirs(
        {"XDG_DATA_HOME": str(tmp_path / "xdg")},
        home=tmp_path / "home",
    )
    assert directories == (tmp_path / "xdg" / "musik" / "plugins",)


def test_plugin_dirs_replace_the_default(tmp_path: Path) -> None:
    env = {
        "MUSIK_PLUGIN_DIRS": f"{tmp_path / 'a'}:{tmp_path / 'b'}",
        "XDG_DATA_HOME": str(tmp_path / "ignored"),
    }
    assert default_plugin_dirs(env) == (tmp_path / "a", tmp_path / "b")


def test_manifest_files_are_one_level_deep(tmp_path: Path) -> None:
    nested = tmp_path / "plugins" / "nested"
    nested.mkdir(parents=True)
    top = tmp_path / "plugins" / "example.json"
    top.write_text("{}", encoding="utf-8")
    (nested / "hidden.json").write_text("{}", encoding="utf-8")
    (tmp_path / "plugins" / "notes.txt").write_text("nope", encoding="utf-8")
    assert manifest_files([tmp_path / "plugins", tmp_path / "missing"]) == (top,)


def test_duplicate_ids_are_reported_once() -> None:
    manifests = [
        parse_manifest(_manifest(id="alpha")),
        parse_manifest(_manifest(id="beta")),
        parse_manifest(_manifest(id="alpha")),
    ]
    assert conflicting_ids(manifests) == ("alpha",)


def test_compatible_capabilities_are_the_known_intersection() -> None:
    manifest = parse_manifest(
        _manifest(capabilities=["library.import_url", "library.future_tool"])
    )
    describe = _describe(capabilities=["library.future_tool", "library.import_url"])
    assert compatible_capabilities(manifest, describe) == ("library.import_url",)


def test_id_mismatch_is_incompatible() -> None:
    manifest = parse_manifest(_manifest())
    with pytest.raises(ManifestError):
        compatible_capabilities(manifest, _describe(id="other"))


def test_search_joins_the_intersection_when_import_is_present() -> None:
    manifest = parse_manifest(_manifest(capabilities=["library.import_url", "library.search"]))
    describe = _describe(capabilities=["library.search", "library.import_url"])
    assert compatible_capabilities(manifest, describe) == ("library.import_url", "library.search")


def test_search_without_import_is_not_active() -> None:
    manifest = parse_manifest(_manifest(capabilities=["library.search"]))
    with pytest.raises(ManifestError):
        compatible_capabilities(manifest, _describe(capabilities=["library.search"]))


def test_future_only_plugin_has_no_usable_capability() -> None:
    manifest = parse_manifest(_manifest(capabilities=["library.future_tool"]))
    with pytest.raises(ManifestError):
        compatible_capabilities(manifest, _describe(capabilities=["library.future_tool"]))


def test_load_manifest_resolves_a_relative_executable(tmp_path: Path) -> None:
    executable = tmp_path / "bin" / "plugin"
    executable.parent.mkdir()
    executable.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    executable.chmod(executable.stat().st_mode | stat.S_IEXEC)
    path = tmp_path / "example.json"
    path.write_text(json.dumps(_manifest()), encoding="utf-8")
    loaded = load_manifest(path)
    assert loaded.resolved_executable == executable.resolve()
    assert loaded.source == path


def test_missing_executable_is_rejected(tmp_path: Path) -> None:
    path = tmp_path / "example.json"
    path.write_text(json.dumps(_manifest()), encoding="utf-8")
    with pytest.raises(ManifestError):
        resolve_executable(path, "bin/plugin")


def test_integer_protocol_is_rejected() -> None:
    with pytest.raises(ManifestError):
        parse_manifest(_manifest(protocol=1))
