# Yandex Music plugin

This package implements protocol v1 with `library.search` and
`library.import_url`. Search returns tracks; importing a track, album, or
artist link writes tagged audio under the request's `dest_dir`, then the host
queues its normal `full_rescan` job.

The plugin uses one server-side Yandex OAuth token. The v1 host API is
administrator-only and does not yet provide a per-user sign-in capability.
Store the token in the untracked `token` file next to the executable; never
put it in the manifest or a request URL.

## Install

From the `player/` directory, build the standalone executable:

```sh
go build -o plugins/yandex/yandex-plugin ./cmd/yandex-plugin
```

Set `MUSIK_PLUGIN_DIRS` to this directory (or copy `manifest.json` and the
executable into the directory already mounted as `/plugins`). Create the
`token` file beside the executable and restrict its permissions to the player
service account (for example, mode `0600`). In Docker, the token is read from
the mounted `/plugins` directory.

Optional settings are `MUSIK_YANDEX_QUALITY=lossless|hq|nq`,
`MUSIK_YANDEX_API_URL` for a compatible API proxy,
`MUSIK_YANDEX_TOKEN_FILE` to use a different token path,
`MUSIK_YANDEX_DESKTOP_SIGN_KEY` for the Yandex download signing key, and
`MUSIK_FFMPEG` to select the ffmpeg executable. Keep the signing key in the
service environment; without it, search works but downloads do not. The player
image includes ffmpeg.
