"""Message parsers keep unknown fields and require protocol string 1."""

from __future__ import annotations

import pytest

from musik_plugin.errors import CODE_PROTOCOL_MISMATCH, CODE_UNSUPPORTED_ACTION, PluginError
from musik_plugin.messages import (
    bind_import_request,
    bind_search_request,
    build_error,
    build_progress,
    build_result,
    build_search_result,
    parse_describe,
    parse_error,
    parse_progress,
    parse_request,
    parse_result,
    parse_search_result,
)
from musik_plugin.protocol import LIBRARY_IMPORT_URL, LIBRARY_SEARCH, dumps_message, loads_object


def _request(**overrides):
    document = {
        "type": "request",
        "protocol": "1",
        "id": "job-1",
        "action": LIBRARY_IMPORT_URL,
        "input": {
            "url": "https://example.invalid/track",
            "library": "/var/lib/musik/library",
        },
    }
    document.update(overrides)
    return document


@pytest.mark.parametrize("protocol", [1, "2", "1.0", "v1", True, None])
def test_protocol_must_be_the_exact_string(protocol) -> None:
    document = _request(protocol=protocol)
    with pytest.raises(PluginError) as caught:
        parse_request(document)
    assert caught.value.code == CODE_PROTOCOL_MISMATCH


def test_missing_protocol_is_a_mismatch() -> None:
    document = _request()
    del document["protocol"]
    with pytest.raises(PluginError) as caught:
        parse_request(document)
    assert caught.value.code == CODE_PROTOCOL_MISMATCH


def test_wrong_message_type_is_an_invalid_request() -> None:
    document = _request(type="progress")
    with pytest.raises(PluginError) as caught:
        parse_request(document)
    assert caught.value.code == "invalid_request"


def test_other_action_is_preserved_until_binding() -> None:
    request = parse_request(_request(action="library.future_tool", input={}))
    assert request.action == "library.future_tool"
    with pytest.raises(PluginError) as caught:
        bind_import_request(request, plugin_id="example")
    assert caught.value.code == CODE_UNSUPPORTED_ACTION


def test_builders_round_trip() -> None:
    progress = build_progress(request_id="job-1", pct=40, phase="download", message="fetching")
    result = build_result(request_id="job-1", paths=["imports/example/song.mp3"])
    error = build_error(request_id="job-1", code="download_failed", message="download failed")
    assert parse_progress(loads_object(dumps_message(progress))).pct == 40
    assert parse_result(loads_object(dumps_message(result))).paths == ("imports/example/song.mp3",)
    parsed = parse_error(loads_object(dumps_message(error)))
    assert parsed.code == "download_failed"
    assert parsed.message == "download failed"


def test_search_limit_defaults_and_rejects_a_bool() -> None:
    request = parse_request(
        {
            "type": "request",
            "protocol": "1",
            "id": "job-1",
            "action": LIBRARY_SEARCH,
            "input": {"query": "example track"},
        }
    )
    assert bind_search_request(request).limit == 8
    request = parse_request(
        {
            "type": "request",
            "protocol": "1",
            "id": "job-1",
            "action": LIBRARY_SEARCH,
            "input": {"query": "example track", "limit": True},
        }
    )
    with pytest.raises(PluginError):
        bind_search_request(request)


def test_progress_items_keep_a_track_list() -> None:
    payload = build_progress(
        request_id="job-1",
        pct=40,
        phase="download",
        message="2 из 3",
        items=[
            {"title": "Первая", "status": "done", "pct": 100},
            {"title": "Вторая", "status": "running", "pct": 20},
            {"title": "мимо", "status": "later"},
        ],
    )
    rows = payload["progress"]["items"]
    assert rows == [
        {"title": "Первая", "status": "done", "pct": 100},
        {"title": "Вторая", "status": "running", "pct": 20},
    ]
    parsed = parse_progress(loads_object(dumps_message(payload)))
    assert parsed.progress_extra["items"][1]["title"] == "Вторая"


def test_search_result_round_trip() -> None:
    payload = build_search_result(
        request_id="job-1",
        items=[{"title": "Example Track", "artist": "Example Artist", "url": "https://example.invalid/track", "duration": 248}],
    )
    parsed_id, items = parse_search_result(loads_object(dumps_message(payload)))
    assert parsed_id == "job-1"
    assert items[0]["duration"] == 248
    with pytest.raises(PluginError):
        build_search_result(request_id="job-1", items=[{"title": "Example", "url": "bad\nurl"}])


def test_describe_keeps_an_unknown_field() -> None:
    parsed = parse_describe(
        {
            "type": "describe",
            "protocol": "1",
            "id": "example",
            "capabilities": ["library.import_url"],
            "vendor": {"channel": "stable"},
        }
    )
    assert parsed.extra["vendor"] == {"channel": "stable"}


def test_crlf_line_decodes() -> None:
    payload = loads_object(b'{"type":"error","protocol":"1","id":"job-1"}\r\n')
    assert payload["id"] == "job-1"
