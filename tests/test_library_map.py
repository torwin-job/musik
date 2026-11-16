from __future__ import annotations

import json

import numpy as np

from musik.db import ensure_db, save_embedding, upsert_track
from musik.db.schema import connect
from musik.db.store import set_genres
from musik.index import library_map


def _unit(v: np.ndarray) -> np.ndarray:
    return (v / np.linalg.norm(v)).astype(np.float32)


def _setup(tmp_path, monkeypatch, test_database) -> None:
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path / "data"))
    test_database(tmp_path / "data" / "db" / "t.db")
    monkeypatch.setenv("MUSIK_EMBEDDINGS_CACHE", str(tmp_path / "data" / "cache" / "e"))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(tmp_path / "data" / "cache" / "a"))
    from musik.config import get_settings

    get_settings.cache_clear()
    ensure_db()


def _track(tmp_path, i: int, vec: np.ndarray, genre: str | None) -> int:
    tid = upsert_track(
        {
            "path": str(tmp_path / f"t{i}.wav"),
            "file_md5": f"md5-{i}",
            "file_mtime": 0.0,
            "file_size": 1,
            "title": f"T{i}",
            "artist": "A",
            "album": None,
            "year": None,
            "track_number": i,
            "duration": 1.0,
            "bitrate": 128,
            "sample_rate": 44100,
            "channels": 2,
            "fingerprint": None,
            "lufs": None,
            "artwork_path": None,
        }
    )
    save_embedding(tid, vec)
    if genre:
        set_genres(tid, [genre])
    return tid


def _stored_map() -> dict:
    with connect() as conn:
        row = conn.execute(
            "SELECT value FROM scan_state WHERE key = :key", {"key": library_map.MAP_KEY}
        ).fetchone()
    return json.loads(row["value"])


def test_map_places_tracks_and_mood_labels(tmp_path, monkeypatch, test_database):
    _setup(tmp_path, monkeypatch, test_database)
    rng = np.random.default_rng(1)
    calm = _unit(rng.normal(size=32))
    loud = _unit(rng.normal(size=32))
    calm_ids, loud_ids = [], []
    for i in range(20):
        calm_ids.append(_track(tmp_path, i, _unit(calm + 0.05 * rng.normal(size=32)), "Ambient"))
    for i in range(20, 40):
        loud_ids.append(_track(tmp_path, i, _unit(loud + 0.05 * rng.normal(size=32)), "Metal"))

    # Fake the CLAP text tower: "sleep" points at the calm group, the rest
    # at the loud one.
    def fake_texts(prompts):
        return [calm if "sleep" in p else loud for p in prompts]

    monkeypatch.setattr("musik.embed.clap.embed_texts", fake_texts)
    # PCA keeps the test fast; UMAP is the same contract with nicer islands.
    monkeypatch.setattr(library_map, "_project", lambda X, seed: (library_map._pca(X), "pca"))

    result = library_map.build_library_map()
    assert result.n == 40
    doc = _stored_map()
    assert doc["method"] == "pca"
    assert sorted(doc["genres"]) == ["Ambient", "Metal"]
    points = {p[0]: p for p in doc["points"]}
    assert set(points) == set(calm_ids + loud_ids)
    assert all(0.0 <= p[1] <= 1.0 and 0.0 <= p[2] <= 1.0 for p in points.values())
    ambient = doc["genres"].index("Ambient")
    assert all(points[t][3] == ambient for t in calm_ids)

    # Each track keeps only its best mood: the fake text tower ties every
    # loud prompt, so the loud group goes to the first one and the rest drop.
    labels = {lab["id"]: lab for lab in doc["labels"]}
    assert set(labels) == {"workout", "sleep"}
    sleep = labels["sleep"]
    assert set(sleep["track_ids"]) == set(calm_ids)
    assert set(labels["workout"]["track_ids"]) == set(loud_ids)

    # Sound neighbours ride along in each point and stay inside the group.
    for t in calm_ids:
        assert len(points[t]) == 4 + library_map.NEIGHBORS
        assert set(points[t][4:]) <= set(calm_ids) - {t}
    calm_xy = np.array([points[t][1:3] for t in calm_ids]).mean(axis=0)
    loud_xy = np.array([points[t][1:3] for t in loud_ids]).mean(axis=0)
    at = np.array([sleep["x"], sleep["y"]])
    assert np.linalg.norm(at - calm_xy) < np.linalg.norm(at - loud_xy)


def test_map_without_text_model_keeps_points(tmp_path, monkeypatch, test_database):
    _setup(tmp_path, monkeypatch, test_database)
    rng = np.random.default_rng(2)
    for i in range(10):
        _track(tmp_path, i, _unit(rng.normal(size=32)), None)

    def broken(_prompts):
        raise OSError("model not cached")

    monkeypatch.setattr("musik.embed.clap.embed_texts", broken)
    result = library_map.build_library_map()
    doc = _stored_map()
    assert result.n == 10 and len(doc["points"]) == 10
    assert doc["labels"] == [] and doc["genres"] == []
    assert all(p[3] == -1 for p in doc["points"])
    assert all(len(p) == 4 + library_map.NEIGHBORS and p[0] not in p[4:] for p in doc["points"])


def test_empty_library_writes_empty_map(tmp_path, monkeypatch, test_database):
    _setup(tmp_path, monkeypatch, test_database)
    result = library_map.build_library_map()
    assert result.n == 0
    assert _stored_map()["points"] == []


def test_clusters_queue_one_map_job_for_a_capable_worker(tmp_path, monkeypatch, test_database):
    from musik.jobs.queue import claim_next
    from musik.jobs.runner import _run_clusters

    _setup(tmp_path, monkeypatch, test_database)
    rng = np.random.default_rng(3)
    for i in range(10):
        _track(tmp_path, i, _unit(rng.normal(size=32)), None)

    first = _run_clusters({"k": 2})
    second = _run_clusters({"k": 2})
    assert isinstance(first["library_map_job"], int)
    assert second["library_map_job"] == "already pending"
    # A CPU-only worker that does not take library_map leaves it for another.
    assert claim_next(kinds=("clusters",)) is None
    claimed = claim_next(kinds=("library_map",))
    assert claimed is not None and claimed["id"] == first["library_map_job"]
    assert claim_next(kinds=("library_map",)) is None


def test_neighbors_handle_tiny_libraries():
    X = np.eye(3, dtype=np.float32)
    assert library_map._neighbors(X[:1]).tolist() == [[-1, -1, -1]]
    two = library_map._neighbors(X[:2])
    assert two[:, 0].tolist() == [1, 0] and (two[:, 1:] == -1).all()
