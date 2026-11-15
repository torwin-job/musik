"""describe and run speak protocol v1 and keep the request URL private."""

from __future__ import annotations

import io
import json
import os
import signal
import subprocess
import sys
import textwrap
import time
from pathlib import Path

import jsonschema

from musik_plugin import (
    CODE_CANCELLED,
    CODE_INVALID_REQUEST,
    CODE_PROTOCOL_MISMATCH,
    CODE_UNSUPPORTED_ACTION,
    CODE_UNSUPPORTED_URL,
    CODE_WRITE_FAILED,
    Plugin,
    PluginError,
    execute,
)
from musik_plugin.protocol import loads_object

ROOT = Path(__file__).resolve().parents[2]
MESSAGES = json.loads((ROOT / "schema" / "messages.schema.json").read_text(encoding="utf-8"))
DESCRIBE = json.loads((ROOT / "schema" / "describe.schema.json").read_text(encoding="utf-8"))
URL = "https://example.invalid/secret-track"


class Writer(Plugin):
    id = "demo"
    capabilities = ("library.import_url",)
    name = "Demo"
    version = "0.1.0"

    def __init__(self) -> None:
        self.called = False

    def import_url(self, request, session):
        self.called = True
        session.progress(pct=50, phase="download", message="fetching")
        return [session.publish_bytes("song.mp3", b"audio-bytes")]


def _request(library: Path, **input_overrides) -> bytes:
    payload = {
        "url": URL,
        "library": str(library),
    }
    payload.update(input_overrides)
    document = {
        "type": "request",
        "protocol": "1",
        "id": "job-1",
        "action": "library.import_url",
        "input": payload,
    }
    return (json.dumps(document) + "\n").encode("utf-8")


def test_describe_writes_one_schema_valid_line() -> None:
    stdout = io.BytesIO()
    assert execute(Writer(), ["describe"], stdout=stdout) == 0
    line = stdout.getvalue()
    assert line.endswith(b"\n")
    assert line.count(b"\n") == 1
    document = loads_object(line)
    jsonschema.Draft202012Validator(DESCRIBE).validate(document)
    assert document["id"] == "demo"
    assert document["capabilities"] == ["library.import_url"]
    assert URL not in line.decode()


def test_usage_errors_leave_stdout_empty() -> None:
    stdout = io.BytesIO()
    stderr = io.StringIO()
    real = sys.stderr
    sys.stderr = stderr
    try:
        assert execute(Writer(), ["describe", "extra"], stdout=stdout) == 2
    finally:
        sys.stderr = real
    assert stdout.getvalue() == b""
    assert "usage:" in stderr.getvalue()


def test_run_publishes_under_the_default_directory(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    plugin = Writer()
    code, stdout, stderr = _run_capture(plugin, library)
    assert code == 0, (stdout, stderr)
    lines = stdout.splitlines()
    assert len(lines) == 2
    for line in lines:
        jsonschema.Draft202012Validator(MESSAGES).validate(loads_object(line))
    result = loads_object(lines[1])
    assert result["result"]["paths"] == ["imports/demo/song.mp3"]
    final = library / "imports" / "demo" / "song.mp3"
    assert final.read_bytes() == b"audio-bytes"
    assert not final.with_name("song.mp3.part").exists()
    assert plugin.called
    assert URL not in stdout.decode()
    assert URL not in stderr


def test_explicit_destination_is_honored(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    code, stdout, _stderr = _run_capture(Writer(), library, raw=_request(library, dest_dir="incoming/box"))
    assert code == 0
    result = loads_object(stdout.splitlines()[-1])
    assert result["result"]["paths"] == ["incoming/box/song.mp3"]
    assert (library / "incoming" / "box" / "song.mp3").read_bytes() == b"audio-bytes"


def test_protocol_mismatch_does_not_call_the_plugin(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    document = json.loads(_request(library))
    document["protocol"] = "2"
    plugin = Writer()
    code, stdout, stderr = _run_capture(plugin, library, raw=(json.dumps(document) + "\n").encode())
    assert code == 1
    error = loads_object(stdout)
    assert error["error"]["code"] == CODE_PROTOCOL_MISMATCH
    assert not plugin.called
    assert URL not in stdout.decode()
    assert URL not in stderr
    assert list(library.iterdir()) == []


def test_search_dispatches_on_action_and_redacts_the_query(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    query = "secret-search-phrase"

    class Finder(Plugin):
        id = "example"
        capabilities = ("library.import_url", "library.search")

        def __init__(self) -> None:
            self.seen = None

        def import_url(self, request, session):
            raise AssertionError("import was called")

        def search(self, request, session):
            self.seen = request
            session.progress(phase="search", message=request.query)
            return [
                {
                    "title": "Example Track",
                    "artist": "Example Artist",
                    "url": "https://example.invalid/track",
                    "duration": 248,
                }
            ]

    plugin = Finder()
    document = {
        "type": "request",
        "protocol": "1",
        "id": "job-1",
        "action": "library.search",
        "input": {"query": query},
    }
    code, stdout, stderr = _run_capture(plugin, library, raw=(json.dumps(document) + "\n").encode())
    assert code == 0, (stdout, stderr)
    lines = stdout.splitlines()
    assert len(lines) == 2
    for line in lines:
        jsonschema.Draft202012Validator(MESSAGES).validate(loads_object(line))
    progress = loads_object(lines[0])
    result = loads_object(lines[1])
    assert progress["progress"]["message"] == "[redacted]"
    assert result["result"]["items"][0]["url"] == "https://example.invalid/track"
    assert result["result"]["items"][0]["duration"] == 248
    assert plugin.seen.limit == 8
    assert query not in stdout.decode()
    assert query not in stderr


def test_import_only_plugin_rejects_search(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    document = {
        "type": "request",
        "protocol": "1",
        "id": "job-1",
        "action": "library.search",
        "input": {"query": "example track", "limit": 8},
    }
    code, stdout, _stderr = _run_capture(Writer(), library, raw=(json.dumps(document) + "\n").encode())
    assert code == 1
    assert loads_object(stdout)["error"]["code"] == CODE_UNSUPPORTED_ACTION


def test_search_capability_requires_a_method() -> None:
    class Broken(Plugin):
        id = "example"
        capabilities = ("library.import_url", "library.search")

        def import_url(self, request, session):
            return [session.publish_bytes("song.mp3", b"audio")]

    stdout = io.BytesIO()
    stderr = io.StringIO()
    real = sys.stderr
    sys.stderr = stderr
    try:
        code = execute(Broken(), ["describe"], stdout=stdout)
    finally:
        sys.stderr = real
    assert code == 2
    assert stdout.getvalue() == b""
    assert "library.search" in stderr.getvalue()


def test_unsupported_action(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    document = json.loads(_request(library))
    document["action"] = "library.future_tool"
    document["input"] = {}
    code, stdout, _stderr = _run_capture(Writer(), library, raw=(json.dumps(document) + "\n").encode())
    assert code == 1
    assert loads_object(stdout)["error"]["code"] == CODE_UNSUPPORTED_ACTION


def test_destination_escape_is_rejected(tmp_path: Path) -> None:
    library = tmp_path / "library"
    outside = tmp_path / "outside"
    library.mkdir()
    outside.mkdir()
    plugin = Writer()
    code, stdout, stderr = _run_capture(plugin, library, raw=_request(library, dest_dir="../outside"))
    assert code == 1
    assert loads_object(stdout)["error"]["code"] == CODE_INVALID_REQUEST
    assert not plugin.called
    assert list(outside.iterdir()) == []
    assert URL not in stdout.decode()
    assert URL not in stderr


def test_symlink_escape_writes_nothing_outside(tmp_path: Path) -> None:
    library = tmp_path / "library"
    outside = tmp_path / "outside"
    (library / "imports").mkdir(parents=True)
    outside.mkdir()
    (library / "imports" / "demo").symlink_to(outside)

    class Escape(Plugin):
        id = "demo"
        capabilities = ("library.import_url",)

        def import_url(self, request, session):
            return [session.publish_bytes("song.mp3", b"nope")]

    code, stdout, stderr = _run_capture(Escape(), library)
    assert code == 1
    assert loads_object(stdout)["error"]["code"] in {CODE_INVALID_REQUEST, CODE_WRITE_FAILED}
    assert list(outside.iterdir()) == []
    assert URL not in stdout.decode()
    assert URL not in stderr


def test_plugin_error_redacts_the_url(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()

    class Reject(Plugin):
        id = "demo"
        capabilities = ("library.import_url",)

        def import_url(self, request, session):
            raise PluginError(CODE_UNSUPPORTED_URL, f"cannot import {request.url}")

    code, stdout, stderr = _run_capture(Reject(), library)
    assert code == 1
    error = loads_object(stdout)
    assert error["error"]["code"] == CODE_UNSUPPORTED_URL
    assert error["error"]["message"] == "cannot import [redacted]"
    assert URL not in stdout.decode()
    assert URL not in stderr


def test_progress_message_redacts_the_url(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()

    class Leaky(Plugin):
        id = "demo"
        capabilities = ("library.import_url",)

        def import_url(self, request, session):
            session.progress(phase="download", message=request.url)
            return [session.publish_bytes("song.mp3", b"audio")]

    code, stdout, stderr = _run_capture(Leaky(), library)
    assert code == 0
    progress = loads_object(stdout.splitlines()[0])
    assert progress["progress"]["message"] == "[redacted]"
    assert URL not in stdout.decode()
    assert URL not in stderr


def test_unexpected_exception_hides_the_url_and_the_part(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()

    class Boom(Plugin):
        id = "demo"
        capabilities = ("library.import_url",)

        def import_url(self, request, session):
            with session.open_part("song.mp3") as handle:
                handle.write(b"partial")
                handle.flush()
                raise RuntimeError(request.url)

    code, stdout, stderr = _run_capture(Boom(), library)
    assert code == 1
    error = loads_object(stdout)
    assert error["error"] == {"code": "internal", "message": "internal error"}
    assert stderr == "plugin failed\n"
    assert URL not in stdout.decode()
    assert URL not in stderr
    assert "Traceback" not in stderr
    assert not (library / "imports" / "demo" / "song.mp3.part").exists()
    assert not (library / "imports" / "demo" / "song.mp3").exists()


def test_sigterm_cancels_and_removes_the_part(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    ready = tmp_path / "ready"
    script = tmp_path / "plugin.py"
    script.write_text(
        textwrap.dedent(
            f"""\
            from musik_plugin import Plugin, main

            class Slow(Plugin):
                id = "slow"
                capabilities = ("library.import_url",)

                def import_url(self, request, session):
                    with session.open_part("song.mp3") as handle:
                        handle.write(b"partial")
                        handle.flush()
                        open({str(ready)!r}, "w", encoding="utf-8").write("ready")
                        session.sleep(30)
                    return [session.destination("song.mp3")]

            if __name__ == "__main__":
                main(Slow())
            """
        ),
        encoding="utf-8",
    )
    env = os.environ.copy()
    src = str(Path(__file__).resolve().parents[1] / "src")
    env["PYTHONPATH"] = src + (os.pathsep + env["PYTHONPATH"] if env.get("PYTHONPATH") else "")
    proc = subprocess.Popen(
        [sys.executable, str(script), "run"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        cwd=tmp_path,
    )
    assert proc.stdin is not None
    proc.stdin.write(_request(library))
    proc.stdin.close()
    proc.stdin = None
    part = library / "imports" / "slow" / "song.mp3.part"
    deadline = time.monotonic() + 10
    while not ready.exists():
        if proc.poll() is not None:
            out, err = proc.communicate()
            raise AssertionError((out, err, proc.returncode))
        if time.monotonic() > deadline:
            proc.kill()
            raise AssertionError("plugin did not become ready")
        time.sleep(0.02)
    assert part.is_file()
    proc.send_signal(signal.SIGTERM)
    stdout, stderr = proc.communicate(timeout=5)
    assert proc.returncode == 1, (proc.returncode, stdout, stderr)
    error = loads_object(stdout)
    assert error["error"]["code"] == CODE_CANCELLED
    assert not part.exists()
    assert not part.with_name("song.mp3").exists()
    combined = stdout.decode() + stderr.decode()
    assert URL not in combined
    assert "secret-track" not in combined


def _run_capture(plugin: Plugin, library: Path, *, raw: bytes | None = None):
    stdout = io.BytesIO()
    stderr = io.StringIO()
    real = sys.stderr
    sys.stderr = stderr
    try:
        code = execute(
            plugin,
            ["run"],
            stdin=io.BytesIO(raw if raw is not None else _request(library)),
            stdout=stdout,
        )
    finally:
        sys.stderr = real
    return code, stdout.getvalue(), stderr.getvalue()
