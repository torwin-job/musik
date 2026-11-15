"""Parse and build protocol v1 messages.

Unknown fields are retained. The protocol version is the exact string ``1``.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any

from musik_plugin.errors import (
    CODE_INTERNAL,
    CODE_INVALID_REQUEST,
    CODE_UNSUPPORTED_ACTION,
    CODE_WRITE_FAILED,
    PluginError,
)
from musik_plugin.paths import validate_relative_path
from musik_plugin.protocol import (
    CAPABILITY_RE,
    DEFAULT_SEARCH_LIMIT,
    ERROR_CODE_RE,
    LIBRARY_IMPORT_URL,
    LIBRARY_RE,
    LIBRARY_SEARCH,
    MAX_CAPABILITIES,
    MAX_DESCRIPTION_CHARS,
    MAX_DURATION,
    MAX_ERROR_MESSAGE,
    MAX_ITEM_TEXT,
    MAX_LIBRARY_CHARS,
    MAX_NAME_CHARS,
    MAX_PROGRESS_MESSAGE,
    MAX_QUERY_CHARS,
    MAX_RESULT_PATHS,
    MAX_SEARCH_ITEMS,
    MAX_SEARCH_LIMIT,
    MAX_URL_CHARS,
    MAX_VERSION_CHARS,
    PHASE_RE,
    PLUGIN_ID_RE,
    PROTOCOL_VERSION,
    REQUEST_ID_RE,
    TEXT_RE,
    clean_text,
    describe_unknown_fields,
    peek_id,
    request_unknown_fields,
    require_protocol,
)


@dataclass(frozen=True)
class Request:
    id: str
    action: str
    input: Mapping[str, Any]
    extra: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class SearchRequest:
    id: str
    query: str
    limit: int
    input_extra: Mapping[str, Any] = field(default_factory=dict)
    extra: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class ImportURLRequest:
    id: str
    url: str
    library: str
    dest_dir: str
    input_extra: Mapping[str, Any] = field(default_factory=dict)
    extra: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Progress:
    id: str
    pct: float | None = None
    phase: str | None = None
    message: str | None = None
    extra: Mapping[str, Any] = field(default_factory=dict)
    progress_extra: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Result:
    id: str
    paths: tuple[str, ...]
    extra: Mapping[str, Any] = field(default_factory=dict)
    result_extra: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class ErrorMessage:
    id: str
    code: str
    message: str
    extra: Mapping[str, Any] = field(default_factory=dict)
    error_extra: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Describe:
    id: str
    capabilities: tuple[str, ...]
    name: str = ""
    version: str = ""
    description: str = ""
    extra: Mapping[str, Any] = field(default_factory=dict)

    @property
    def protocol(self) -> str:
        return PROTOCOL_VERSION


def _object(payload: object) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise PluginError(CODE_INVALID_REQUEST, "malformed request")
    return payload


def _require_type(payload: Mapping[str, Any], expected: str) -> None:
    if payload.get("type") != expected:
        raise PluginError(CODE_INVALID_REQUEST, "message type is invalid")


def _require_id(payload: Mapping[str, Any]) -> str:
    value = payload.get("id")
    if not isinstance(value, str) or REQUEST_ID_RE.fullmatch(value) is None:
        raise PluginError(CODE_INVALID_REQUEST, "request id is invalid")
    return value


def _optional_text(payload: Mapping[str, Any], key: str, *, limit: int) -> str:
    if key not in payload:
        return ""
    value = payload[key]
    if not isinstance(value, str) or TEXT_RE.fullmatch(value) is None or not 1 <= len(value) <= limit:
        raise PluginError(CODE_INVALID_REQUEST, "message field is invalid")
    return value


def _capabilities(payload: Mapping[str, Any]) -> tuple[str, ...]:
    value = payload.get("capabilities")
    if not isinstance(value, list) or not 1 <= len(value) <= MAX_CAPABILITIES or len(value) != len(set(value)):
        raise PluginError(CODE_INVALID_REQUEST, "capabilities are invalid")
    capabilities: list[str] = []
    for item in value:
        if not isinstance(item, str) or CAPABILITY_RE.fullmatch(item) is None:
            raise PluginError(CODE_INVALID_REQUEST, "capabilities are invalid")
        capabilities.append(item)
    return tuple(capabilities)


def parse_request(payload: object) -> Request:
    data = _object(payload)
    _require_type(data, "request")
    require_protocol(data.get("protocol"))
    action = data.get("action")
    if not isinstance(action, str) or CAPABILITY_RE.fullmatch(action) is None:
        raise PluginError(CODE_INVALID_REQUEST, "action is invalid")
    raw_input = data.get("input")
    if not isinstance(raw_input, dict):
        raise PluginError(CODE_INVALID_REQUEST, "input is invalid")
    return Request(
        id=_require_id(data),
        action=action,
        input=dict(raw_input),
        extra=request_unknown_fields(data),
    )


def bind_search_request(request: Request) -> SearchRequest:
    """Validate a ``library.search`` input and fill the default limit."""
    if request.action != LIBRARY_SEARCH:
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")
    query = request.input.get("query")
    if not isinstance(query, str) or TEXT_RE.fullmatch(query) is None or len(query) > MAX_QUERY_CHARS:
        raise PluginError(CODE_INVALID_REQUEST, "query is invalid")
    limit = DEFAULT_SEARCH_LIMIT
    if "limit" in request.input and request.input.get("limit") is not None:
        raw_limit = request.input["limit"]
        if isinstance(raw_limit, bool) or not isinstance(raw_limit, int) or not 1 <= raw_limit <= MAX_SEARCH_LIMIT:
            raise PluginError(CODE_INVALID_REQUEST, "limit is invalid")
        limit = raw_limit
    extra = {key: value for key, value in request.input.items() if key not in {"query", "limit"}}
    return SearchRequest(
        id=request.id,
        query=query,
        limit=limit,
        input_extra=extra,
        extra=dict(request.extra),
    )


def bind_import_request(request: Request, *, plugin_id: str) -> ImportURLRequest:
    """Validate a ``library.import_url`` input and fill the default directory."""
    if request.action != LIBRARY_IMPORT_URL:
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")
    if PLUGIN_ID_RE.fullmatch(plugin_id) is None:
        raise PluginError(CODE_INVALID_REQUEST, "plugin id is invalid")
    url = request.input.get("url")
    library = request.input.get("library")
    if not isinstance(url, str) or TEXT_RE.fullmatch(url) is None or len(url) > MAX_URL_CHARS:
        raise PluginError(CODE_INVALID_REQUEST, "url is invalid")
    if (
        not isinstance(library, str)
        or LIBRARY_RE.fullmatch(library) is None
        or len(library) > MAX_LIBRARY_CHARS
    ):
        raise PluginError(CODE_INVALID_REQUEST, "library path is invalid")
    if "dest_dir" in request.input:
        dest = request.input["dest_dir"]
        try:
            dest_dir = validate_relative_path(dest)
        except PluginError:
            raise PluginError(CODE_INVALID_REQUEST, "destination is invalid") from None
    else:
        dest_dir = validate_relative_path(f"imports/{plugin_id}")
    extra = {
        key: value
        for key, value in request.input.items()
        if key not in {"url", "library", "dest_dir"}
    }
    return ImportURLRequest(
        id=request.id,
        url=url,
        library=library,
        dest_dir=dest_dir,
        input_extra=extra,
        extra=dict(request.extra),
    )


def parse_progress(payload: object) -> Progress:
    data = _object(payload)
    _require_type(data, "progress")
    require_protocol(data.get("protocol"))
    body = data.get("progress")
    if not isinstance(body, dict):
        raise PluginError(CODE_INVALID_REQUEST, "progress is invalid")
    pct = body.get("pct") if "pct" in body else None
    if pct is not None:
        if isinstance(pct, bool) or not isinstance(pct, (int, float)) or pct != pct or not 0 <= pct <= 100:
            raise PluginError(CODE_INVALID_REQUEST, "progress is invalid")
    phase = body.get("phase") if "phase" in body else None
    if phase is not None and (not isinstance(phase, str) or PHASE_RE.fullmatch(phase) is None):
        raise PluginError(CODE_INVALID_REQUEST, "progress is invalid")
    message = body.get("message") if "message" in body else None
    if message is not None and (
        not isinstance(message, str) or TEXT_RE.fullmatch(message) is None or len(message) > MAX_PROGRESS_MESSAGE
    ):
        raise PluginError(CODE_INVALID_REQUEST, "progress is invalid")
    if pct is None and phase is None and message is None:
        raise PluginError(CODE_INVALID_REQUEST, "progress is invalid")
    return Progress(
        id=_require_id(data),
        pct=None if pct is None else float(pct),
        phase=phase,
        message=message,
        extra=_without(data, {"type", "protocol", "id", "progress"}),
        progress_extra=_without(body, {"pct", "phase", "message"}),
    )


def parse_result(payload: object) -> Result:
    data = _object(payload)
    _require_type(data, "result")
    require_protocol(data.get("protocol"))
    body = data.get("result")
    if not isinstance(body, dict):
        raise PluginError(CODE_INVALID_REQUEST, "result is invalid")
    raw_paths = body.get("paths")
    if not isinstance(raw_paths, list) or not 1 <= len(raw_paths) <= MAX_RESULT_PATHS:
        raise PluginError(CODE_INVALID_REQUEST, "result paths are invalid")
    paths = tuple(validate_relative_path(item) for item in raw_paths)
    return Result(
        id=_require_id(data),
        paths=paths,
        extra=_without(data, {"type", "protocol", "id", "result"}),
        result_extra=_without(body, {"paths"}),
    )


def parse_error(payload: object) -> ErrorMessage:
    data = _object(payload)
    _require_type(data, "error")
    require_protocol(data.get("protocol"))
    body = data.get("error")
    if not isinstance(body, dict):
        raise PluginError(CODE_INVALID_REQUEST, "error is invalid")
    code = body.get("code")
    message = body.get("message")
    if not isinstance(code, str) or ERROR_CODE_RE.fullmatch(code) is None:
        raise PluginError(CODE_INVALID_REQUEST, "error is invalid")
    if not isinstance(message, str) or TEXT_RE.fullmatch(message) is None or len(message) > MAX_ERROR_MESSAGE:
        raise PluginError(CODE_INVALID_REQUEST, "error is invalid")
    return ErrorMessage(
        id=_require_id(data),
        code=code,
        message=message,
        extra=_without(data, {"type", "protocol", "id", "error"}),
        error_extra=_without(body, {"code", "message"}),
    )


def parse_describe(payload: object) -> Describe:
    data = _object(payload)
    _require_type(data, "describe")
    require_protocol(data.get("protocol"))
    plugin_id = data.get("id")
    if not isinstance(plugin_id, str) or PLUGIN_ID_RE.fullmatch(plugin_id) is None:
        raise PluginError(CODE_INVALID_REQUEST, "plugin id is invalid")
    return Describe(
        id=plugin_id,
        capabilities=_capabilities(data),
        name=_optional_text(data, "name", limit=MAX_NAME_CHARS),
        version=_optional_text(data, "version", limit=MAX_VERSION_CHARS),
        description=_optional_text(data, "description", limit=MAX_DESCRIPTION_CHARS),
        extra=describe_unknown_fields(data),
    )


_PROGRESS_ITEM_STATUS = frozenset({"pending", "running", "done", "error"})


def normalize_progress_items(items: object) -> list[dict[str, Any]]:
    """Track rows a progress line may carry. A bad row is skipped."""
    if isinstance(items, (str, bytes)) or not isinstance(items, Sequence):
        raise PluginError(CODE_INTERNAL, "progress update is invalid")
    found: list[dict[str, Any]] = []
    for item in list(items)[:100]:
        if not isinstance(item, Mapping):
            continue
        title = item.get("title")
        status = item.get("status")
        if not isinstance(title, str) or not isinstance(status, str):
            continue
        if status not in _PROGRESS_ITEM_STATUS:
            continue
        try:
            text = clean_text(title, limit=MAX_PROGRESS_MESSAGE)
        except PluginError:
            continue
        row: dict[str, Any] = {"title": text, "status": status}
        pct = item.get("pct")
        if pct is not None and not isinstance(pct, bool) and isinstance(pct, (int, float)) and pct == pct and 0 <= float(pct) <= 100:
            row["pct"] = int(pct) if float(pct) == int(pct) else round(float(pct), 2)
        found.append(row)
    return found


def build_progress(
    *,
    request_id: str,
    pct: float | None = None,
    phase: str | None = None,
    message: str | None = None,
    items: Sequence[Mapping[str, Any]] | None = None,
) -> dict[str, Any]:
    if REQUEST_ID_RE.fullmatch(request_id) is None:
        raise PluginError(CODE_INTERNAL, "request id is invalid")
    body: dict[str, Any] = {}
    if pct is not None:
        if isinstance(pct, bool) or not isinstance(pct, (int, float)) or pct != pct or not 0 <= float(pct) <= 100:
            raise PluginError(CODE_INTERNAL, "progress update is invalid")
        body["pct"] = int(pct) if float(pct) == int(pct) else round(float(pct), 2)
    if phase is not None:
        if not isinstance(phase, str) or PHASE_RE.fullmatch(phase) is None:
            raise PluginError(CODE_INTERNAL, "progress update is invalid")
        body["phase"] = phase
    if message is not None:
        if not isinstance(message, str):
            raise PluginError(CODE_INTERNAL, "progress update is invalid")
        body["message"] = clean_text(message, limit=MAX_PROGRESS_MESSAGE)
    if items is not None:
        rows = normalize_progress_items(items)
        if rows:
            body["items"] = rows
    if not body:
        raise PluginError(CODE_INTERNAL, "progress update is invalid")
    return {"type": "progress", "protocol": PROTOCOL_VERSION, "id": request_id, "progress": body}


def normalize_search_items(items: object, *, code: str = CODE_INTERNAL) -> list[dict[str, Any]]:
    """Return search rows the protocol result may carry."""
    if isinstance(items, (str, bytes)) or not isinstance(items, Sequence):
        raise PluginError(code, "search result is invalid")
    if len(items) > MAX_SEARCH_ITEMS:
        raise PluginError(code, "search result is invalid")
    return [_search_item(item, code=code) for item in items]


def _search_item(item: object, *, code: str) -> dict[str, Any]:
    if not isinstance(item, Mapping):
        raise PluginError(code, "search result is invalid")
    title = item.get("title")
    url = item.get("url")
    if not isinstance(title, str) or TEXT_RE.fullmatch(title) is None or len(title) > MAX_ITEM_TEXT:
        raise PluginError(code, "search result is invalid")
    if not isinstance(url, str) or TEXT_RE.fullmatch(url) is None or len(url) > MAX_URL_CHARS:
        raise PluginError(code, "search result is invalid")
    result: dict[str, Any] = {"title": title, "url": url}
    if "artist" in item and item.get("artist") is not None:
        artist = item["artist"]
        if not isinstance(artist, str) or TEXT_RE.fullmatch(artist) is None or len(artist) > MAX_ITEM_TEXT:
            raise PluginError(code, "search result is invalid")
        result["artist"] = artist
    if "duration" in item and item.get("duration") is not None:
        duration = item["duration"]
        if isinstance(duration, bool) or not isinstance(duration, int) or not 0 <= duration <= MAX_DURATION:
            raise PluginError(code, "search result is invalid")
        result["duration"] = duration
    return result


def parse_search_result(payload: object) -> tuple[str, tuple[dict[str, Any], ...]]:
    """Validate a ``library.search`` result line and return its id and items."""
    data = _object(payload)
    _require_type(data, "result")
    require_protocol(data.get("protocol"))
    body = data.get("result")
    if not isinstance(body, dict) or "items" not in body:
        raise PluginError(CODE_INVALID_REQUEST, "result is invalid")
    return _require_id(data), tuple(normalize_search_items(body.get("items"), code=CODE_INVALID_REQUEST))


def build_search_result(*, request_id: str, items: object) -> dict[str, Any]:
    if REQUEST_ID_RE.fullmatch(request_id) is None:
        raise PluginError(CODE_INTERNAL, "request id is invalid")
    return {
        "type": "result",
        "protocol": PROTOCOL_VERSION,
        "id": request_id,
        "result": {"items": normalize_search_items(items)},
    }


def build_result(*, request_id: str, paths: Sequence[str]) -> dict[str, Any]:
    if REQUEST_ID_RE.fullmatch(request_id) is None:
        raise PluginError(CODE_INTERNAL, "request id is invalid")
    if isinstance(paths, str) or not 1 <= len(paths) <= MAX_RESULT_PATHS:
        raise PluginError(CODE_WRITE_FAILED, "import produced no files")
    return {
        "type": "result",
        "protocol": PROTOCOL_VERSION,
        "id": request_id,
        "result": {"paths": [validate_relative_path(item) for item in paths]},
    }


def build_error(*, request_id: str, code: str, message: str) -> dict[str, Any]:
    safe_id = request_id if REQUEST_ID_RE.fullmatch(request_id) else peek_id({})
    safe_code = code if ERROR_CODE_RE.fullmatch(code) else CODE_INTERNAL
    return {
        "type": "error",
        "protocol": PROTOCOL_VERSION,
        "id": safe_id,
        "error": {"code": safe_code, "message": clean_text(message, limit=MAX_ERROR_MESSAGE)},
    }


def _without(payload: Mapping[str, Any], known: set[str]) -> dict[str, Any]:
    return {key: value for key, value in payload.items() if key not in known}
