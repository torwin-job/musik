"""Manifest documents and the directories the host scans for them."""

from __future__ import annotations

import json
import os
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import Any

from musik_plugin.errors import CODE_INVALID_REQUEST, ManifestError, PluginError
from musik_plugin.messages import Describe
from musik_plugin.protocol import (
    CAPABILITY_RE,
    KNOWN_CAPABILITIES,
    LIBRARY_IMPORT_URL,
    MAX_CAPABILITIES,
    MAX_DESCRIPTION_CHARS,
    MAX_EXECUTABLE_CHARS,
    MAX_NAME_CHARS,
    MAX_VERSION_CHARS,
    PLUGIN_ID_RE,
    PROTOCOL_VERSION,
    TEXT_RE,
    require_protocol,
)

_MANIFEST_KEYS = frozenset(
    {"id", "executable", "protocol", "capabilities", "name", "version", "description"}
)


@dataclass(frozen=True)
class Manifest:
    id: str
    executable: str
    capabilities: tuple[str, ...]
    name: str = ""
    version: str = ""
    description: str = ""
    extra: Mapping[str, Any] = field(default_factory=dict)
    source: Path | None = None
    resolved_executable: Path | None = None

    @property
    def protocol(self) -> str:
        return PROTOCOL_VERSION


def parse_manifest(payload: object) -> Manifest:
    """Validate a manifest object. The executable file is not opened."""
    if not isinstance(payload, dict):
        raise ManifestError("manifest is invalid")
    try:
        require_protocol(payload.get("protocol"))
        plugin_id = payload.get("id")
        executable = payload.get("executable")
        if not isinstance(plugin_id, str) or PLUGIN_ID_RE.fullmatch(plugin_id) is None:
            raise PluginError(CODE_INVALID_REQUEST, "plugin id is invalid")
        if (
            not isinstance(executable, str)
            or TEXT_RE.fullmatch(executable) is None
            or len(executable) > MAX_EXECUTABLE_CHARS
        ):
            raise PluginError(CODE_INVALID_REQUEST, "executable path is invalid")
        capabilities = _capabilities(payload.get("capabilities"))
        name = _optional_text(payload, "name", limit=MAX_NAME_CHARS)
        version = _optional_text(payload, "version", limit=MAX_VERSION_CHARS)
        description = _optional_text(payload, "description", limit=MAX_DESCRIPTION_CHARS)
    except PluginError as exc:
        raise ManifestError(exc.message) from None
    return Manifest(
        id=plugin_id,
        executable=executable,
        capabilities=capabilities,
        name=name,
        version=version,
        description=description,
        extra={key: value for key, value in payload.items() if key not in _MANIFEST_KEYS},
    )


def load_manifest(path: Path) -> Manifest:
    """Read a manifest file and resolve its executable."""
    manifest_path = Path(path)
    try:
        payload = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ManifestError("manifest is unreadable") from exc
    manifest = parse_manifest(payload)
    resolved = resolve_executable(manifest_path, manifest.executable)
    return replace(manifest, source=manifest_path, resolved_executable=resolved)


def resolve_executable(manifest_file: Path, executable: str) -> Path:
    """Resolve ``executable`` relative to the manifest directory."""
    if TEXT_RE.fullmatch(executable) is None or len(executable) > MAX_EXECUTABLE_CHARS:
        raise ManifestError("executable path is invalid")
    raw = Path(executable)
    candidate = raw if raw.is_absolute() else Path(manifest_file).parent / raw
    try:
        resolved = candidate.resolve()
    except OSError as exc:
        raise ManifestError("executable path is invalid") from exc
    if not resolved.is_file():
        raise ManifestError("executable is missing")
    if not os.access(resolved, os.X_OK):
        raise ManifestError("executable is not runnable")
    return resolved


def compatible_capabilities(manifest: Manifest, describe: Describe) -> tuple[str, ...]:
    """Return capabilities both sides advertise and this protocol can run.

    The manifest is the operator allowlist. ``describe`` is what the executable
    reports. Capabilities outside protocol v1 stay in the documents and are
    omitted here.
    """
    if manifest.protocol != PROTOCOL_VERSION or describe.protocol != PROTOCOL_VERSION:
        raise ManifestError("unsupported protocol version")
    if manifest.id != describe.id:
        raise ManifestError("plugin id does not match the manifest")
    advertised = set(describe.capabilities)
    usable = tuple(
        capability
        for capability in manifest.capabilities
        if capability in advertised and capability in KNOWN_CAPABILITIES
    )
    if LIBRARY_IMPORT_URL not in usable:
        raise ManifestError("plugin has no usable capabilities")
    return usable


def default_plugin_dirs(
    environ: Mapping[str, str] | None = None,
    *,
    home: Path | None = None,
) -> tuple[Path, ...]:
    """Return the manifest directories a host scans.

    ``MUSIK_PLUGIN_DIRS``, when it contains a path, replaces the default. The
    default is ``$XDG_DATA_HOME/musik/plugins``, or ``~/.local/share/musik/plugins``
    when ``XDG_DATA_HOME`` is empty.
    """
    env = os.environ if environ is None else environ
    override = env.get("MUSIK_PLUGIN_DIRS", "")
    if override.strip():
        directories = [Path(part).expanduser() for part in override.split(os.pathsep) if part.strip()]
        if directories:
            return tuple(directories)
    xdg = env.get("XDG_DATA_HOME", "").strip()
    if xdg:
        base = Path(xdg).expanduser()
    else:
        base = (home if home is not None else Path.home()) / ".local" / "share"
    return (base / "musik" / "plugins",)


def manifest_files(directories: Sequence[Path]) -> tuple[Path, ...]:
    """Return ``*.json`` files placed directly in each existing directory."""
    found: list[Path] = []
    for directory in directories:
        if not directory.is_dir():
            continue
        found.extend(path for path in sorted(directory.glob("*.json")) if path.is_file())
    return tuple(found)


def conflicting_ids(manifests: Sequence[Manifest]) -> tuple[str, ...]:
    """Return ids that appear on more than one manifest, in first-seen order."""
    seen: set[str] = set()
    conflicts: list[str] = []
    for manifest in manifests:
        if manifest.id in seen and manifest.id not in conflicts:
            conflicts.append(manifest.id)
        seen.add(manifest.id)
    return tuple(conflicts)


def _capabilities(value: object) -> tuple[str, ...]:
    if not isinstance(value, list) or not 1 <= len(value) <= MAX_CAPABILITIES or len(value) != len(set(value)):
        raise PluginError(CODE_INVALID_REQUEST, "capabilities are invalid")
    capabilities: list[str] = []
    for item in value:
        if not isinstance(item, str) or CAPABILITY_RE.fullmatch(item) is None:
            raise PluginError(CODE_INVALID_REQUEST, "capabilities are invalid")
        capabilities.append(item)
    return tuple(capabilities)


def _optional_text(payload: Mapping[str, Any], key: str, *, limit: int) -> str:
    if key not in payload:
        return ""
    value = payload[key]
    if not isinstance(value, str) or TEXT_RE.fullmatch(value) is None or not 1 <= len(value) <= limit:
        raise PluginError(CODE_INVALID_REQUEST, "manifest field is invalid")
    return value
