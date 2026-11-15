"""Protocol v1 constants and JSONL framing.

The version is the exact string ``1``. Callers keep unknown object fields and
ignore the ones they do not implement.
"""

from __future__ import annotations

import json
import re
from collections.abc import Mapping
from typing import Any

from musik_plugin.errors import (
    CODE_INTERNAL,
    CODE_INVALID_REQUEST,
    CODE_PROTOCOL_MISMATCH,
    PluginError,
)

PROTOCOL_VERSION = "1"
LIBRARY_IMPORT_URL = "library.import_url"
LIBRARY_SEARCH = "library.search"
KNOWN_CAPABILITIES = frozenset({LIBRARY_IMPORT_URL, LIBRARY_SEARCH})

MAX_LINE_BYTES = 1024 * 1024
MAX_ID_LENGTH = 128
MAX_PLUGIN_ID_LENGTH = 63
MAX_CAPABILITIES = 32
MAX_PATH_CHARS = 1024
MAX_LIBRARY_CHARS = 4096
MAX_URL_CHARS = 4096
MAX_EXECUTABLE_CHARS = 4096
MAX_ERROR_MESSAGE = 500
MAX_PROGRESS_MESSAGE = 200
MAX_NAME_CHARS = 120
MAX_VERSION_CHARS = 64
MAX_DESCRIPTION_CHARS = 300
MAX_RESULT_PATHS = 100
MAX_QUERY_CHARS = 500
MAX_SEARCH_ITEMS = 20
DEFAULT_SEARCH_LIMIT = 8
MAX_SEARCH_LIMIT = 20
MAX_ITEM_TEXT = 500
MAX_DURATION = 864000

PLUGIN_ID_PATTERN = r"^[a-z][a-z0-9-]{0,62}$"
CAPABILITY_PATTERN = r"^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+$"
REQUEST_ID_PATTERN = r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$"
PHASE_PATTERN = r"^[a-z][a-z0-9_-]{0,63}$"
ERROR_CODE_PATTERN = r"^[a-z][a-z0-9_]{0,63}$"
TEXT_PATTERN = r"^[^\x00-\x1f]+$"
LIBRARY_PATTERN = r"^/[^\x00-\x1f]*$"
RELATIVE_SEGMENT_PATTERN = r"^[^/\\\x00-\x1f]+(?:/[^/\\\x00-\x1f]+)*$"
RELATIVE_DOT_PATTERN = r"(^|/)\.\.?(?:/|$)"
RELATIVE_PART_PATTERN = r"\.part$"

PLUGIN_ID_RE = re.compile(PLUGIN_ID_PATTERN)
CAPABILITY_RE = re.compile(CAPABILITY_PATTERN)
REQUEST_ID_RE = re.compile(REQUEST_ID_PATTERN)
PHASE_RE = re.compile(PHASE_PATTERN)
ERROR_CODE_RE = re.compile(ERROR_CODE_PATTERN)
TEXT_RE = re.compile(TEXT_PATTERN)
LIBRARY_RE = re.compile(LIBRARY_PATTERN)
RELATIVE_SEGMENT_RE = re.compile(RELATIVE_SEGMENT_PATTERN)
RELATIVE_DOT_RE = re.compile(RELATIVE_DOT_PATTERN)
RELATIVE_PART_RE = re.compile(RELATIVE_PART_PATTERN)

MISSING_REQUEST_ID = "0"
_REQUEST_KEYS = frozenset({"type", "protocol", "id", "action", "input"})
_DESCRIBE_KEYS = frozenset(
    {"type", "protocol", "id", "capabilities", "name", "version", "description"}
)


def require_protocol(value: object) -> None:
    """Accept only the exact version string for this directory."""
    if value != PROTOCOL_VERSION:
        raise PluginError(CODE_PROTOCOL_MISMATCH, "unsupported protocol version")


def redact(text: str, secrets: list[str] | tuple[str, ...]) -> str:
    """Remove request secrets from a message before it leaves the process."""
    redacted = text
    for secret in secrets:
        if secret:
            redacted = redacted.replace(secret, "[redacted]")
    return redacted


def clean_text(text: str, *, limit: int) -> str:
    """Return a single-line message that fits the protocol message schema."""
    cleaned = "".join(ch for ch in text if ch >= " " and ch != "\x7f").strip()
    if not cleaned:
        return "plugin error"
    if len(cleaned) > limit:
        return cleaned[:limit]
    return cleaned


def dumps_message(payload: Mapping[str, Any]) -> bytes:
    """Encode one JSON object as a single UTF-8 line ending in ``\\n``."""
    text = json.dumps(dict(payload), ensure_ascii=False, separators=(",", ":"))
    if "\n" in text or "\r" in text:
        raise PluginError(CODE_INTERNAL, "message is invalid")
    data = (text + "\n").encode("utf-8")
    if len(data) > MAX_LINE_BYTES:
        raise PluginError(CODE_INTERNAL, "message is too long")
    return data


def loads_object(line: bytes | str) -> dict[str, Any]:
    """Decode one JSON object. The raw line stays out of the error text."""
    if isinstance(line, bytes):
        try:
            text = line.decode("utf-8")
        except UnicodeError as exc:
            raise PluginError(CODE_INVALID_REQUEST, "malformed request") from exc
    else:
        text = line
    if text.endswith("\n"):
        text = text[:-1]
    if text.endswith("\r"):
        text = text[:-1]
    if "\n" in text or "\r" in text:
        raise PluginError(CODE_INVALID_REQUEST, "malformed request")
    try:
        payload = json.loads(text)
    except json.JSONDecodeError as exc:
        raise PluginError(CODE_INVALID_REQUEST, "malformed request") from exc
    if not isinstance(payload, dict):
        raise PluginError(CODE_INVALID_REQUEST, "malformed request")
    return payload


def peek_id(payload: Mapping[str, Any]) -> str:
    value = payload.get("id")
    if isinstance(value, str) and REQUEST_ID_RE.fullmatch(value):
        return value
    return MISSING_REQUEST_ID


def query_secret(payload: Mapping[str, Any]) -> str:
    raw_input = payload.get("input")
    if not isinstance(raw_input, dict):
        return ""
    query = raw_input.get("query")
    if isinstance(query, str):
        return query
    return ""


def request_secrets(payload: Mapping[str, Any]) -> list[str]:
    """Return request values that must stay out of messages, logs, and argv."""
    found: list[str] = []
    for secret in (url_secret(payload), query_secret(payload)):
        if secret and secret not in found:
            found.append(secret)
    return found


def url_secret(payload: Mapping[str, Any]) -> str:
    raw_input = payload.get("input")
    if not isinstance(raw_input, dict):
        return ""
    url = raw_input.get("url")
    if isinstance(url, str):
        return url
    return ""


def unknown_fields(payload: Mapping[str, Any], known: frozenset[str]) -> dict[str, Any]:
    return {key: value for key, value in payload.items() if key not in known}


def request_unknown_fields(payload: Mapping[str, Any]) -> dict[str, Any]:
    return unknown_fields(payload, _REQUEST_KEYS)


def describe_unknown_fields(payload: Mapping[str, Any]) -> dict[str, Any]:
    return unknown_fields(payload, _DESCRIBE_KEYS)
