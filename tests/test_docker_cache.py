from pathlib import Path


ROOT = Path(__file__).parents[1]


def test_worker_dependencies_are_cached_before_source_copy() -> None:
    dockerfile = (ROOT / "Dockerfile.worker").read_text()
    dependency_install = dockerfile.index('pip install --index-url "${TORCH_INDEX_URL}"')
    source_copy = dockerfile.index("COPY src ./src")
    editable_install = dockerfile.index("pip install --no-deps -e .")

    assert dependency_install < source_copy < editable_install
    assert "--mount=type=cache,target=/root/.cache/pip" in dockerfile
    assert "--no-cache-dir" not in dockerfile
    assert "download.pytorch.org/whl/cpu" in dockerfile


def test_docker_context_excludes_runtime_and_build_data() -> None:
    ignored = set((ROOT / ".dockerignore").read_text().splitlines())
    assert {"data", ".venv", ".venv-rocm", "mobile/flutter/build"} <= ignored
