"""Run a plugin process: ``describe`` or one JSONL ``run`` session."""

from __future__ import annotations

import io
import os
import signal
import sys
import time
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import BinaryIO

from musik_plugin.errors import (
    CODE_CANCELLED,
    CODE_INTERNAL,
    CODE_INVALID_REQUEST,
    CODE_UNSUPPORTED_ACTION,
    CODE_WRITE_FAILED,
    PluginConfigError,
    PluginError,
)
from musik_plugin.messages import (
    ImportURLRequest,
    Request,
    SearchRequest,
    bind_import_request,
    bind_search_request,
    build_error,
    build_progress,
    build_result,
    build_search_result,
    parse_request,
)
from musik_plugin.paths import AtomicFile, contained_path, join_relative
from musik_plugin.protocol import (
    KNOWN_CAPABILITIES,
    LIBRARY_IMPORT_URL,
    LIBRARY_SEARCH,
    MAX_DESCRIPTION_CHARS,
    MAX_LINE_BYTES,
    MAX_NAME_CHARS,
    MAX_RESULT_PATHS,
    MAX_VERSION_CHARS,
    PLUGIN_ID_RE,
    PROTOCOL_VERSION,
    TEXT_RE,
    dumps_message,
    loads_object,
    peek_id,
    redact,
    request_secrets,
)

_ACTIVE: RunState | None = None


@dataclass
class RunState:
    cancelled: bool = False
    finished: bool = False
    parts: list[Path] = field(default_factory=list)
    request_id: str = "0"
    secrets: list[str] = field(default_factory=list)


class Plugin:
    """Base class for a protocol v1 executable.

    Subclasses set ``id`` and ``capabilities`` and override the actions they list.
    """

    id: str
    capabilities: tuple[str, ...] = (LIBRARY_IMPORT_URL,)
    name: str = ""
    version: str = ""
    description: str = ""

    def import_url(self, request: ImportURLRequest, session: Session) -> Sequence[str]:
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")

    def search(self, request: SearchRequest, session: Session) -> Sequence[Mapping[str, object]]:
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")


def _redact_progress_items(
    items: Sequence[Mapping[str, object]],
    secrets: list[str] | tuple[str, ...],
) -> list[dict[str, object]]:
    cleaned: list[dict[str, object]] = []
    for item in items:
        if not isinstance(item, Mapping):
            continue
        row = dict(item)
        title = row.get("title")
        if isinstance(title, str):
            row["title"] = redact(title, secrets)
        cleaned.append(row)
    return cleaned


class Session:
    """Progress, cancellation, and atomic writes for one request."""

    def __init__(
        self,
        *,
        library: Path | None,
        dest_dir: str,
        state: RunState,
        stdout: BinaryIO,
        request_id: str,
    ) -> None:
        self.library = library
        self.dest_dir = dest_dir
        self._state = state
        self._stdout = stdout
        self.request_id = request_id

    @property
    def cancelled(self) -> bool:
        return self._state.cancelled

    def check_cancelled(self) -> None:
        if self._state.cancelled:
            raise PluginError(CODE_CANCELLED, "cancelled")

    def sleep(self, seconds: float) -> None:
        """Wait up to ``seconds``, returning early when the host cancels."""
        if isinstance(seconds, bool) or not isinstance(seconds, (int, float)) or seconds < 0:
            raise PluginError(CODE_INTERNAL, "sleep duration is invalid")
        deadline = time.monotonic() + float(seconds)
        while True:
            self.check_cancelled()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            time.sleep(min(0.05, remaining))

    def progress(
        self,
        *,
        pct: float | None = None,
        phase: str | None = None,
        message: str | None = None,
        items: Sequence[Mapping[str, object]] | None = None,
    ) -> None:
        self.check_cancelled()
        if message is not None:
            message = redact(message, self._state.secrets)
        if items is not None:
            items = _redact_progress_items(items, self._state.secrets)
        write_message(
            self._stdout,
            build_progress(request_id=self.request_id, pct=pct, phase=phase, message=message, items=items),
        )

    def destination(self, name: str) -> str:
        """Return the library-relative path for a file under the destination directory."""
        return join_relative(self.dest_dir, name)

    @contextmanager
    def open_part(self, name: str) -> Iterator[BinaryIO]:
        """Yield a binary file. A clean exit renames it into the library."""
        self.check_cancelled()
        if self.library is None:
            raise PluginError(CODE_WRITE_FAILED, "library directory is missing")
        atomic = AtomicFile(self.library, self.destination(name))
        handle = atomic.open()
        self._state.parts.append(atomic.part)
        try:
            yield handle
            self.check_cancelled()
            atomic.publish()
        except Exception:
            atomic.abort()
            raise
        finally:
            _forget(self._state, atomic.part)

    def publish_bytes(self, name: str, data: bytes) -> str:
        """Write ``data`` atomically and return the library-relative path."""
        if not isinstance(data, (bytes, bytearray)):
            raise PluginError(CODE_WRITE_FAILED, "audio payload is invalid")
        with self.open_part(name) as handle:
            handle.write(data)
        return self.destination(name)


def describe_payload(plugin: Plugin) -> dict[str, object]:
    validate_plugin(plugin)
    payload: dict[str, object] = {
        "type": "describe",
        "protocol": PROTOCOL_VERSION,
        "id": plugin.id,
        "capabilities": list(tuple(plugin.capabilities)),
    }
    if plugin.name:
        payload["name"] = plugin.name
    if plugin.version:
        payload["version"] = plugin.version
    if plugin.description:
        payload["description"] = plugin.description
    return payload


def validate_plugin(plugin: Plugin) -> None:
    if not isinstance(plugin.id, str) or PLUGIN_ID_RE.fullmatch(plugin.id) is None:
        raise PluginConfigError("plugin id is invalid")
    raw_capabilities = plugin.capabilities
    if isinstance(raw_capabilities, str) or not isinstance(raw_capabilities, Sequence):
        raise PluginConfigError("plugin capabilities are invalid")
    capabilities = tuple(raw_capabilities)
    if not capabilities or len(capabilities) != len(set(capabilities)):
        raise PluginConfigError("plugin capabilities are invalid")
    for capability in capabilities:
        if capability not in KNOWN_CAPABILITIES:
            raise PluginConfigError("plugin capability is unsupported")
    if LIBRARY_IMPORT_URL in capabilities and type(plugin).import_url is Plugin.import_url:
        raise PluginConfigError("library.import_url is not implemented")
    if LIBRARY_SEARCH in capabilities and type(plugin).search is Plugin.search:
        raise PluginConfigError("library.search is not implemented")
    for label, limit in (
        (plugin.name, MAX_NAME_CHARS),
        (plugin.version, MAX_VERSION_CHARS),
        (plugin.description, MAX_DESCRIPTION_CHARS),
    ):
        if not isinstance(label, str) or (label and (TEXT_RE.fullmatch(label) is None or len(label) > limit)):
            raise PluginConfigError("plugin metadata is invalid")


def execute(
    plugin: Plugin,
    argv: Sequence[str] | None = None,
    *,
    stdin: BinaryIO | None = None,
    stdout: BinaryIO | None = None,
) -> int:
    """Run ``describe`` or ``run``. ``argv`` is the argument list after the program name."""
    args = list(sys.argv[1:] if argv is None else argv)
    input_stream = sys.stdin.buffer if stdin is None else stdin
    output_stream = sys.stdout.buffer if stdout is None else stdout
    if args == ["describe"]:
        try:
            write_message(output_stream, describe_payload(plugin))
        except PluginConfigError as exc:
            sys.stderr.write(f"{exc}\n")
            return 2
        return 0
    if args == ["run"]:
        try:
            validate_plugin(plugin)
        except PluginConfigError as exc:
            sys.stderr.write(f"{exc}\n")
            return 2
        state = RunState()
        try:
            previous = _install_signals(state)
        except PluginConfigError as exc:
            sys.stderr.write(f"{exc}\n")
            return 2
        try:
            return _run(plugin, input_stream, output_stream, state)
        finally:
            state.finished = True
            for part in list(state.parts):
                try:
                    os.unlink(part)
                except OSError:
                    pass
            _restore_signals(previous)
    sys.stderr.write("usage: plugin describe|run\n")
    return 2


def main(plugin: Plugin, argv: Sequence[str] | None = None) -> None:
    """Entry point for a plugin executable. Exits with the protocol status code."""
    raise SystemExit(execute(plugin, argv))


def write_message(stream: BinaryIO, payload: dict[str, object]) -> None:
    data = dumps_message(payload)
    try:
        fd = stream.fileno()
    except (AttributeError, io.UnsupportedOperation):
        stream.write(data)
        stream.flush()
        return
    view = memoryview(data)
    while view:
        try:
            written = os.write(fd, view)
        except InterruptedError:
            continue
        if written <= 0:
            raise PluginError(CODE_INTERNAL, "stdout is closed")
        view = view[written:]


def _run(plugin: Plugin, stdin: BinaryIO, stdout: BinaryIO, state: RunState) -> int:
    try:
        raw = _read_request_line(stdin)
        payload = loads_object(raw)
        state.request_id = peek_id(payload)
        _remember_secrets(state, request_secrets(payload))
        request = parse_request(payload)
        state.request_id = request.id
        if request.action == LIBRARY_IMPORT_URL:
            return _run_import(plugin, stdout, state, request)
        if request.action == LIBRARY_SEARCH:
            return _run_search(plugin, stdout, state, request)
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")
    except PluginError as exc:
        _write_failure(stdout, state, exc.code, exc.message)
        return 1
    except Exception:
        sys.stderr.write("plugin failed\n")
        _write_failure(stdout, state, CODE_INTERNAL, "internal error")
        return 1


def _run_import(plugin: Plugin, stdout: BinaryIO, state: RunState, request: Request) -> int:
    if LIBRARY_IMPORT_URL not in tuple(plugin.capabilities):
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")
    bound = bind_import_request(request, plugin_id=plugin.id)
    _remember_secrets(state, [bound.url])
    root = Path(bound.library).resolve()
    if not root.is_dir():
        raise PluginError(CODE_WRITE_FAILED, "library directory is missing")
    session = Session(
        library=root,
        dest_dir=bound.dest_dir,
        state=state,
        stdout=stdout,
        request_id=bound.id,
    )
    returned = plugin.import_url(bound, session)
    paths = _normalize_returned(
        returned,
        library=root,
        dest_dir=bound.dest_dir,
        secrets=state.secrets,
    )
    state.finished = True
    write_message(stdout, build_result(request_id=bound.id, paths=paths))
    return 0


def _run_search(plugin: Plugin, stdout: BinaryIO, state: RunState, request: Request) -> int:
    if LIBRARY_SEARCH not in tuple(plugin.capabilities):
        raise PluginError(CODE_UNSUPPORTED_ACTION, "unsupported action")
    bound = bind_search_request(request)
    _remember_secrets(state, [bound.query])
    session = Session(
        library=None,
        dest_dir="",
        state=state,
        stdout=stdout,
        request_id=bound.id,
    )
    returned = plugin.search(bound, session)
    state.finished = True
    write_message(stdout, build_search_result(request_id=bound.id, items=returned))
    return 0


def _remember_secrets(state: RunState, secrets: list[str]) -> None:
    for secret in secrets:
        if secret and secret not in state.secrets:
            state.secrets.append(secret)


def _normalize_returned(
    returned: object,
    *,
    library: Path,
    dest_dir: str,
    secrets: list[str],
) -> tuple[str, ...]:
    if isinstance(returned, str) or not isinstance(returned, Sequence) or not returned:
        raise PluginError(CODE_WRITE_FAILED, "import produced no files")
    if len(returned) > MAX_RESULT_PATHS:
        raise PluginError(CODE_WRITE_FAILED, "import produced too many files")
    dest_root = contained_path(library, dest_dir)
    paths: list[str] = []
    for item in returned:
        if not isinstance(item, str) or any(secret and secret in item for secret in secrets):
            raise PluginError(CODE_WRITE_FAILED, "result path is invalid")
        try:
            final = contained_path(library, item)
            final.relative_to(dest_root)
        except (PluginError, ValueError):
            raise PluginError(CODE_WRITE_FAILED, "result path is outside the destination") from None
        if not final.is_file():
            raise PluginError(CODE_WRITE_FAILED, "result file is missing")
        paths.append(item)
    return tuple(paths)


def _write_failure(stdout: BinaryIO, state: RunState, code: str, message: str) -> None:
    state.finished = True
    try:
        write_message(
            stdout,
            build_error(
                request_id=state.request_id,
                code=code,
                message=redact(message, state.secrets),
            ),
        )
    except Exception:
        sys.stderr.write("plugin failed\n")


def _read_request_line(stream: BinaryIO) -> bytes:
    line = stream.readline(MAX_LINE_BYTES + 1)
    if line == b"":
        raise PluginError(CODE_INVALID_REQUEST, "missing request")
    if len(line) > MAX_LINE_BYTES:
        raise PluginError(CODE_INVALID_REQUEST, "request line is too long")
    return line


def _forget(state: RunState, part: Path) -> None:
    state.parts = [item for item in state.parts if item != part]


def _install_signals(state: RunState) -> dict[int, signal.Handlers]:
    global _ACTIVE
    if _ACTIVE is not None:
        raise PluginConfigError("plugin session is already running")
    previous: dict[int, signal.Handlers] = {}
    try:
        for number in _cancel_signals():
            previous[number] = signal.signal(number, _on_signal)
    except Exception:
        for number, handler in previous.items():
            signal.signal(number, handler)
        raise
    _ACTIVE = state
    return previous


def _restore_signals(previous: dict[int, signal.Handlers]) -> None:
    global _ACTIVE
    for number, handler in previous.items():
        signal.signal(number, handler)
    _ACTIVE = None


def _on_signal(signum: int, frame: object) -> None:
    del signum, frame
    state = _ACTIVE
    if state is None or state.finished:
        return
    state.cancelled = True
    for part in list(state.parts):
        try:
            os.unlink(part)
        except OSError:
            pass


def _cancel_signals() -> tuple[int, ...]:
    numbers = [signal.SIGTERM]
    if hasattr(signal, "SIGINT"):
        numbers.append(signal.SIGINT)
    return tuple(numbers)
