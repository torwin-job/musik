# Plugin protocol v1

This directory is the language-neutral contract for a local musik plugin.
A plugin is a separate executable. The player discovers a manifest, checks
compatibility with `describe`, then runs one task per process over JSON Lines.
Protocol version `1` is the exact string `"1"`.

Site-specific downloaders are separate packages. They are not dependencies of
this tree. A plugin keeps its own credentials in its own configuration.

## Layout

The host scans manifest files in these directories:

1. When `MUSIK_PLUGIN_DIRS` contains at least one path, that list replaces the
   default. Entries are separated by the operating system path separator.
2. Otherwise the directory is `$XDG_DATA_HOME/musik/plugins`. An empty
   `XDG_DATA_HOME` means `~/.local/share/musik/plugins`.

Each `*.json` file placed directly in one of those directories is a manifest.
Nested directories are a place for the executable and its files; their JSON is
not discovered. A missing directory is skipped. An id that appears on more
than one valid manifest is left inactive.

The player does not download plugins and does not ship them in the standard
images. The operator puts the manifest and the executable in place, and mounts
that directory read-write into the player container. The library path in the
request is the player's library mount, also read-write, so the plugin can
publish files there. The worker mounts the same library read-only. Compose
bind-mounts the host directory from `MUSIK_PLUGIN_DIRS` at `/plugins` and sets
the process value to `/plugins`. See `docs/DEPLOY.md`.

## Manifest

`schema/manifest.schema.json` is normative. Unknown properties are kept.

| Field | Rule |
| --- | --- |
| `id` | Stable id matching `^[a-z][a-z0-9-]{0,62}$`. |
| `executable` | Path of the executable. A relative path is resolved from the manifest file's directory. |
| `protocol` | The exact string `"1"`. |
| `capabilities` | One or more unique capability names. Protocol v1 defines `library.import_url` and the optional `library.search`. Other well-formed names are preserved. |
| `name`, `version`, `description` | Optional display fields. |

The host executes the resolved path directly. It does not use a shell.
Arguments are exactly one of:

```text
<executable> describe
<executable> run
```

A shebang is honored by the operating system when the executable is a script.
The host still does not invoke `sh -c`.

## Discovery

`describe` reads no request. It writes one JSON object to stdout and exits 0.
`schema/describe.schema.json` is normative. Example: `examples/describe.json`.

The host accepts the plugin when all of the following hold:

- `describe.protocol` and `manifest.protocol` are both `"1"`.
- `describe.id` equals `manifest.id`.
- The intersection of the two capability lists contains `library.import_url`.

The plugin stays active when that intersection contains `library.import_url`.
`library.search` is optional: the host calls it only when the name is in the
intersection. Capabilities present on only one side, and any other well-formed
name, stay in the documents and are not invoked.

## JSON Lines session

`run` handles one task and exits. The host writes one request line to stdin.
The plugin writes zero or more `progress` lines and then one `result` or
`error` line to stdout. Every line is one UTF-8 JSON object terminated by
`\n`, at most 1 MiB including the newline. stdout carries protocol messages.
Diagnostics go to stderr.

`schema/messages.schema.json` is normative. Unknown properties on every object
are kept so a later field can be added without breaking a v1 peer. A peer
ignores properties it does not implement.

Exit status:

| Code | Meaning |
| --- | --- |
| 0 | `describe` succeeded, or `run` wrote a `result`. |
| 1 | `run` wrote an `error`. |
| 2 | The argument list or the plugin configuration is unusable. Stdout is empty and stderr explains it. |

A process that exits without a `result` or `error` line failed. The host
records that as an internal failure.

### Request

The host sends:

```json
{
  "type": "request",
  "protocol": "1",
  "id": "job-1",
  "action": "library.import_url",
  "input": {
    "url": "https://example.invalid/track",
    "library": "/var/lib/musik/library",
    "dest_dir": "imports/example"
  }
}
```

`id` matches `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`. The plugin copies it onto
every progress, result, and error line. When the request id cannot be read,
the error line uses `"0"`.

`input.url` is the address to import. `input.library` is an absolute library
root (`/...`). `input.dest_dir` is an optional directory relative to that root.
When `dest_dir` is omitted, the plugin writes under `imports/<plugin-id>`.

The v1 host sends `url`, `library`, and optionally `dest_dir`. It does not put
credentials, cookies, or account tokens in the request. A plugin reads those
from its own configuration.

### Progress, result, error

Progress may include `pct` (a number from 0 to 100), `phase` (a short token
such as `download` or `write`), and `message`. At least one of them is present.

A successful `library.import_url` result is:

```json
{
  "type": "result",
  "protocol": "1",
  "id": "job-1",
  "result": { "paths": ["imports/example/track.flac"] }
}
```

`paths` contains 1 to 100 library-relative paths of files that exist under
`dest_dir`.

### Search

`library.search` is optional. The host sends it on stdin, the same way as an
import, and only when both the manifest and `describe` list the capability.
`limit` is an integer from 1 to 20. When the host omits it, the value is 8.
The plugin returns at most 20 items.

```json
{
  "type": "request",
  "protocol": "1",
  "id": "job-1",
  "action": "library.search",
  "input": { "query": "example track", "limit": 8 }
}
```

```json
{
  "type": "result",
  "protocol": "1",
  "id": "job-1",
  "result": {
    "items": [
      {
        "title": "Example Track",
        "artist": "Example Artist",
        "url": "https://example.invalid/track",
        "duration": 248
      }
    ]
  }
}
```

`url` is an opaque string the same plugin accepts in a later
`library.import_url` request. Peers check that it is a single line of text.
They do not inspect the host. `duration` is a whole number of seconds from 0
to 864000 and may be omitted. `artist` may be omitted. The query and these
addresses stay out of logs and out of the process argument list.

An error is:

```json
{
  "type": "error",
  "protocol": "1",
  "id": "job-1",
  "error": { "code": "unsupported_url", "message": "URL is not supported" }
}
```

Known codes are `invalid_request`, `protocol_mismatch`, `unsupported_action`,
`unsupported_url`, `download_failed`, `write_failed`, `cancelled`, and
`internal`. Any other code that matches `^[a-z][a-z0-9_]{0,63}$` is still a
failure. Messages are a single line of at most 500 characters (200 on progress).

## Paths and atomic writes

A relative path uses `/` separators. Every segment is non-empty. The segments
`.` and `..` are invalid. The finished path is at most 1024 characters and
ends with a suffix other than `.part`. Absolute paths and backslashes are
invalid.

The plugin writes each finished file under `library/dest_dir`:

1. Create or replace `filename.part` in the same directory as the final file.
2. Write the bytes, flush, and `fsync` the file.
3. `rename` the partial file onto the final name.
4. On failure, or when the process is cancelled, remove the `.part` file.

The rename stays on one filesystem because the partial file is in the
destination directory. The host checks every returned path again after the
process exits: resolve it against the library root and accept it when the
resolved file is inside that root and inside `dest_dir`.

After those checks succeed, the host enqueues the existing `full_rescan` job.
That job scans the library, embeds new audio, and reloads the player catalog.
The plugin does not scan or embed.

## Cancellation

The host cancels a task by sending `SIGTERM` to the plugin process. A timeout
uses the same signal, and the host may follow it with `SIGKILL` when the
process stays alive. Cancellation is a signal, not a JSON message.

The plugin stops the work, removes every `.part` file it created, writes an
`error` line with code `cancelled` when it still can, and exits 1. Children
started by the plugin are the plugin's responsibility.

## Secrets

`input.url` and `input.query` are visible to the plugin process because the
plugin needs them. The host keeps the import URL out of public API responses
and logs. The search query and the item addresses stay out of logs and out of
the process argument list. Progress, result, and error messages omit the
import URL, the search query, and any credential. Stderr does too. The Python
helper removes those request values from messages it sends and replaces an
unexpected exception with `internal error`.

## Python helper

The helper for plugin authors lives in `python/` and imports as `musik_plugin`.
It depends only on the Python standard library. From a plugin project:

```bash
pip install -e /path/to/musik/sdk/plugin/v1/python
```

```python
from musik_plugin import CODE_UNSUPPORTED_URL, Plugin, PluginError, main


class ExamplePlugin(Plugin):
    id = "example"
    capabilities = ("library.import_url", "library.search")

    def import_url(self, request, session):
        session.progress(pct=10, phase="download", message="fetching")
        data = fetch(request.url)  # the plugin's own client; do not log the URL
        if data is None:
            raise PluginError(CODE_UNSUPPORTED_URL, "URL is not supported")
        session.progress(pct=90, phase="write", message="writing")
        return [session.publish_bytes("track.mp3", data)]

    def search(self, request, session):
        return [
            {
                "title": "Example Track",
                "artist": "Example Artist",
                "url": "https://example.invalid/track",
                "duration": 248,
            }
        ]


if __name__ == "__main__":
    main(ExamplePlugin())
```

`session.publish_bytes(name, data)` and `session.open_part(name)` write under
`request.dest_dir` using the atomic rules above and return a library-relative
path. `session.sleep` and `session.check_cancelled` observe `SIGTERM`.
`request.input_extra` and `request.extra` hold properties this version does
not interpret.

Call the executable as `example-plugin describe` and `example-plugin run`.

## Versioning

A peer rejects a document whose `protocol` is anything other than the string
`"1"`, including `1`, `"v1"`, and `"1.0"`. Adding a property is compatible.
Changing the meaning of an existing property, or requiring a new property,
needs a new `sdk/plugin/vN` contract.
