"""The helper stays free of site-specific downloader dependencies."""

from __future__ import annotations

import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
# Built from pieces so this file is not itself a match for the scan.
BANNED = ("yt" + "-dlp", "yt_" + "dlp", "you" + "tube")


def test_contract_tree_has_no_downloader_dependency() -> None:
    for path in ROOT.rglob("*"):
        if not path.is_file() or "__pycache__" in path.parts:
            continue
        if path.suffix not in {".py", ".json", ".md", ".toml"}:
            continue
        text = path.read_text(encoding="utf-8").lower()
        for word in BANNED:
            assert word not in text, f"{word} found in {path}"


def test_helper_package_has_no_runtime_dependencies() -> None:
    project = tomllib.loads((ROOT / "python" / "pyproject.toml").read_text(encoding="utf-8"))
    assert project["project"]["dependencies"] == []
