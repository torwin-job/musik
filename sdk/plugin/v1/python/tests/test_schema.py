"""JSON Schema is the language-neutral contract and matches the helper."""

from __future__ import annotations

import json
from pathlib import Path

import jsonschema
import pytest

from musik_plugin.errors import PluginError
from musik_plugin.messages import (
    bind_import_request,
    bind_search_request,
    parse_describe,
    parse_error,
    parse_progress,
    parse_request,
    parse_result,
    parse_search_result,
)
from musik_plugin.manifest import parse_manifest
from musik_plugin.protocol import (
    CAPABILITY_PATTERN,
    ERROR_CODE_PATTERN,
    LIBRARY_PATTERN,
    MAX_CAPABILITIES,
    MAX_DESCRIPTION_CHARS,
    MAX_DURATION,
    MAX_ERROR_MESSAGE,
    MAX_EXECUTABLE_CHARS,
    MAX_ITEM_TEXT,
    MAX_LIBRARY_CHARS,
    MAX_NAME_CHARS,
    MAX_PATH_CHARS,
    MAX_PROGRESS_MESSAGE,
    MAX_QUERY_CHARS,
    MAX_RESULT_PATHS,
    MAX_SEARCH_ITEMS,
    MAX_SEARCH_LIMIT,
    MAX_URL_CHARS,
    MAX_VERSION_CHARS,
    PHASE_PATTERN,
    PLUGIN_ID_PATTERN,
    PROTOCOL_VERSION,
    RELATIVE_DOT_PATTERN,
    RELATIVE_PART_PATTERN,
    RELATIVE_SEGMENT_PATTERN,
    REQUEST_ID_PATTERN,
    TEXT_PATTERN,
)

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = ROOT / "schema"
EXAMPLES = ROOT / "examples"


def _schema(name: str) -> dict:
    return json.loads((SCHEMA / name).read_text(encoding="utf-8"))


def _example(name: str) -> dict:
    return json.loads((EXAMPLES / name).read_text(encoding="utf-8"))


def _validate(schema_name: str, instance: dict) -> None:
    jsonschema.Draft202012Validator(_schema(schema_name)).validate(instance)


@pytest.mark.parametrize(
    "name",
    ["manifest.schema.json", "describe.schema.json", "messages.schema.json"],
)
def test_schemas_are_draft_2020_12(name: str) -> None:
    jsonschema.Draft202012Validator.check_schema(_schema(name))


def test_protocol_const_is_the_exact_version_string() -> None:
    for name in ("manifest.schema.json", "describe.schema.json"):
        const = _schema(name)["properties"]["protocol"]["const"]
        assert const == PROTOCOL_VERSION
        assert isinstance(const, str)


def test_unknown_properties_are_allowed() -> None:
    manifest = _schema("manifest.schema.json")
    describe = _schema("describe.schema.json")
    messages = _schema("messages.schema.json")
    assert manifest["additionalProperties"] is True
    assert describe["additionalProperties"] is True
    for key in (
        "request",
        "progress",
        "result",
        "error",
        "importURLInput",
        "searchInput",
        "importResult",
        "searchResult",
        "searchItem",
    ):
        assert messages["$defs"][key]["additionalProperties"] is True
    progress = messages["$defs"]["progress"]["properties"]["progress"]
    error = messages["$defs"]["error"]["properties"]["error"]
    assert progress["additionalProperties"] is True
    assert error["additionalProperties"] is True
    assert "oneOf" in messages["$defs"]["result"]["properties"]["result"]


def test_patterns_and_limits_match_the_helper() -> None:
    manifest = _schema("manifest.schema.json")
    assert manifest["properties"]["id"]["pattern"] == PLUGIN_ID_PATTERN
    executable = manifest["properties"]["executable"]
    assert executable["pattern"] == TEXT_PATTERN
    assert executable["maxLength"] == MAX_EXECUTABLE_CHARS
    capabilities = manifest["properties"]["capabilities"]
    assert capabilities["maxItems"] == MAX_CAPABILITIES
    assert capabilities["items"]["pattern"] == CAPABILITY_PATTERN
    assert manifest["properties"]["name"]["maxLength"] == MAX_NAME_CHARS
    assert manifest["properties"]["version"]["maxLength"] == MAX_VERSION_CHARS
    assert manifest["properties"]["description"]["maxLength"] == MAX_DESCRIPTION_CHARS

    messages = _schema("messages.schema.json")
    relative = messages["$defs"]["relativePath"]
    assert relative["maxLength"] == MAX_PATH_CHARS
    assert relative["allOf"][0]["pattern"] == RELATIVE_SEGMENT_PATTERN
    assert relative["allOf"][1]["not"]["pattern"] == RELATIVE_DOT_PATTERN
    assert relative["allOf"][2]["not"]["pattern"] == RELATIVE_PART_PATTERN
    assert messages["$defs"]["requestId"]["pattern"] == REQUEST_ID_PATTERN
    assert messages["$defs"]["capability"]["pattern"] == CAPABILITY_PATTERN
    url = messages["$defs"]["importURLInput"]["properties"]["url"]
    library = messages["$defs"]["importURLInput"]["properties"]["library"]
    assert url["pattern"] == TEXT_PATTERN
    assert url["maxLength"] == MAX_URL_CHARS
    assert library["pattern"] == LIBRARY_PATTERN
    assert library["maxLength"] == MAX_LIBRARY_CHARS
    progress = messages["$defs"]["progress"]["properties"]["progress"]["properties"]
    assert progress["phase"]["pattern"] == PHASE_PATTERN
    assert progress["message"]["maxLength"] == MAX_PROGRESS_MESSAGE
    error = messages["$defs"]["error"]["properties"]["error"]["properties"]
    assert error["code"]["pattern"] == ERROR_CODE_PATTERN
    assert error["message"]["maxLength"] == MAX_ERROR_MESSAGE
    paths = messages["$defs"]["importResult"]["properties"]["paths"]
    assert paths["maxItems"] == MAX_RESULT_PATHS
    items = messages["$defs"]["searchResult"]["properties"]["items"]
    assert items["maxItems"] == MAX_SEARCH_ITEMS
    query = messages["$defs"]["searchInput"]["properties"]["query"]
    assert query["maxLength"] == MAX_QUERY_CHARS
    assert messages["$defs"]["searchInput"]["properties"]["limit"]["maximum"] == MAX_SEARCH_LIMIT
    assert messages["$defs"]["searchItem"]["properties"]["title"]["maxLength"] == MAX_ITEM_TEXT
    assert messages["$defs"]["searchItem"]["properties"]["duration"]["maximum"] == MAX_DURATION


@pytest.mark.parametrize(
    ("example", "schema", "parser"),
    [
        ("manifest.json", "manifest.schema.json", parse_manifest),
        ("describe.json", "describe.schema.json", parse_describe),
        ("request.json", "messages.schema.json", parse_request),
        ("request-with-extension.json", "messages.schema.json", parse_request),
        ("progress.json", "messages.schema.json", parse_progress),
        ("result.json", "messages.schema.json", parse_result),
        ("search-request.json", "messages.schema.json", parse_request),
        ("search-result.json", "messages.schema.json", parse_search_result),
        ("error.json", "messages.schema.json", parse_error),
    ],
)
def test_examples_match_schema_and_helper(example: str, schema: str, parser) -> None:
    document = _example(example)
    _validate(schema, document)
    parser(document)


def test_search_example_binds() -> None:
    request = parse_request(_example("search-request.json"))
    bound = bind_search_request(request)
    assert bound.query == "example track"
    assert bound.limit == 8
    parsed_id, items = parse_search_result(_example("search-result.json"))
    assert parsed_id == "job-1"
    assert items[0]["title"] == "Example Track"
    assert items[0]["url"] == "https://example.invalid/track"
    assert items[0]["duration"] == 248


def test_extension_fields_survive_parsing() -> None:
    document = _example("request-with-extension.json")
    request = parse_request(document)
    assert request.extra["trace"] == "abc"
    bound = bind_import_request(request, plugin_id="example")
    assert bound.dest_dir == "imports/example"
    assert bound.input_extra["label"] == "keep"
    assert bound.url == "https://example.invalid/track"


@pytest.mark.parametrize(
    ("schema", "document"),
    [
        (
            "manifest.schema.json",
            {
                "id": "example",
                "executable": "bin/plugin",
                "protocol": 1,
                "capabilities": ["library.import_url"],
            },
        ),
        (
            "manifest.schema.json",
            {
                "id": "example",
                "executable": "bin/plugin",
                "protocol": "1.0",
                "capabilities": ["library.import_url"],
            },
        ),
        (
            "manifest.schema.json",
            {
                "id": "Example",
                "executable": "bin/plugin",
                "protocol": "1",
                "capabilities": ["library.import_url"],
            },
        ),
        (
            "manifest.schema.json",
            {
                "id": "example",
                "executable": "bin/plugin",
                "protocol": "1",
                "capabilities": ["library.import_url", "library.import_url"],
            },
        ),
        (
            "messages.schema.json",
            {
                "type": "result",
                "protocol": "1",
                "id": "job-1",
                "result": {"paths": ["../secret.mp3"]},
            },
        ),
        (
            "messages.schema.json",
            {
                "type": "result",
                "protocol": "1",
                "id": "job-1",
                "result": {"paths": ["/tmp/secret.mp3"]},
            },
        ),
        (
            "messages.schema.json",
            {
                "type": "result",
                "protocol": "1",
                "id": "job-1",
                "result": {"paths": ["imports/example/song.part"]},
            },
        ),
        (
            "messages.schema.json",
            {
                "type": "request",
                "protocol": "1",
                "id": "job-1",
                "action": "library.import_url",
                "input": {"url": "https://example.invalid/a", "library": "relative/lib"},
            },
        ),
        (
            "messages.schema.json",
            {
                "type": "progress",
                "protocol": "1",
                "id": "job-1",
                "progress": {"pct": 101},
            },
        ),
    ],
)
def test_schema_rejects_incompatible_documents(schema: str, document: dict) -> None:
    with pytest.raises(jsonschema.ValidationError):
        _validate(schema, document)


def test_future_capability_is_preserved() -> None:
    document = {
        "id": "example",
        "executable": "bin/plugin",
        "protocol": "1",
        "capabilities": ["library.import_url", "library.future_tool"],
        "homepage": "https://example.invalid/plugin",
    }
    _validate("manifest.schema.json", document)
    manifest = parse_manifest(document)
    assert manifest.capabilities == ("library.import_url", "library.future_tool")
    assert manifest.extra["homepage"] == "https://example.invalid/plugin"


@pytest.mark.parametrize(
    "path",
    ["a.mp3", "imports/demo/a.mp3", ".hidden", "a b.mp3", "...", "песня.mp3", "a.part/b.mp3"],
)
def test_relative_paths_are_accepted_by_schema_and_helper(path: str) -> None:
    from musik_plugin.paths import validate_relative_path

    document = {
        "type": "result",
        "protocol": "1",
        "id": "job-1",
        "result": {"paths": [path]},
    }
    _validate("messages.schema.json", document)
    assert validate_relative_path(path) == path


@pytest.mark.parametrize(
    "path",
    ["", ".", "..", "/a.mp3", "a/../b.mp3", "a/./b.mp3", "a//b.mp3", "a\\b.mp3", "a.part", "a/b.part", "a/"],
)
def test_relative_paths_are_rejected_by_schema_and_helper(path: str) -> None:
    from musik_plugin.paths import validate_relative_path

    document = {
        "type": "result",
        "protocol": "1",
        "id": "job-1",
        "result": {"paths": [path]},
    }
    with pytest.raises(jsonschema.ValidationError):
        _validate("messages.schema.json", document)
    with pytest.raises(PluginError):
        validate_relative_path(path)
