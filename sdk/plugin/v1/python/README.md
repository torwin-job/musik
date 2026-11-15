# musik-plugin-sdk

Python helper for [musik plugin protocol v1](../README.md).

The package imports as `musik_plugin` and depends only on the standard library.
Install it from a plugin project with:

```bash
pip install -e /path/to/musik/sdk/plugin/v1/python
```

```python
from musik_plugin import Plugin, main


class ExamplePlugin(Plugin):
    id = "example"
    capabilities = ("library.import_url", "library.search")

    def import_url(self, request, session):
        session.progress(pct=10, phase="download", message="fetching")
        return [session.publish_bytes("track.mp3", fetch(request.url))]

    def search(self, request, session):
        return [{"title": "Example Track", "url": "https://example.invalid/track", "duration": 90}]


if __name__ == "__main__":
    main(ExamplePlugin())
```

The executable is invoked as `example-plugin describe` or `example-plugin run`.
`run` chooses `import_url` or `search` from the request `action`. A plugin that
does not list `library.search` keeps working as an import-only executable.
