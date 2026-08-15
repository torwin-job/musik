from __future__ import annotations

from datetime import datetime, timezone

import numpy as np

from musik.brain import mixes
from musik.brain.generators import _mmr_select
from musik.config import get_settings
from musik.db.schema import init_db
from musik.index.brute import EmbeddingIndex
from musik.jobs.queue import list_recent
from musik.worker.server import enqueue_nightly_mixes_if_due


class FakeIndex:
    def __init__(self) -> None:
        raw = np.array(
            [
                [1.0, 0.0, 0.0],
                [0.9, 0.1, 0.0],
                [0.4, 0.8, 0.1],
                [0.0, 0.4, 0.9],
            ],
            dtype=np.float32,
        )
        self.matrix = raw / np.linalg.norm(raw, axis=1, keepdims=True)
        self.meta = [
            {"id": i + 1, "artist": f"artist-{i}", "title": f"track-{i}"}
            for i in range(len(raw))
        ]
        self.size = len(raw)
        self.dim = raw.shape[1]

    def sims_to_vector(self, vector: np.ndarray) -> np.ndarray:
        return self.matrix @ vector

    def row_of(self, track_id: int) -> int | None:
        row = track_id - 1
        return row if 0 <= row < self.size else None


def test_for_you_is_seeded_and_avoids_recent_rows(monkeypatch) -> None:
    index = FakeIndex()
    monkeypatch.setattr(mixes, "load_index", lambda: index)
    monkeypatch.setattr(
        mixes,
        "_taste_vector",
        lambda _: (np.array([1.0, 0.0, 0.0], dtype=np.float32), "test"),
    )
    monkeypatch.setattr(mixes, "_for_you_forbidden_rows", lambda *_args, **_kwargs: {0, 1})

    first = mixes.generate_for_you(size=2, explore_ratio=0, seed=20260815)
    second = mixes.generate_for_you(size=2, explore_ratio=0, seed=20260815)

    first_ids = [entry["track_id"] for entry in first.entries]
    assert first_ids == [entry["track_id"] for entry in second.entries]
    assert set(first_ids).isdisjoint({1, 2})
    assert first.meta["seed"] == 20260815
    assert first.meta["excluded"] == 2


def test_for_you_rotates_with_seed(monkeypatch) -> None:
    rng = np.random.default_rng(42)
    raw = rng.normal(size=(60, 8)).astype(np.float32)
    raw /= np.linalg.norm(raw, axis=1, keepdims=True)
    index = EmbeddingIndex(
        track_ids=np.arange(1, 61, dtype=np.int64),
        matrix=raw,
        meta=[
            {"id": i + 1, "artist": f"artist-{i % 12}", "title": f"track-{i}"}
            for i in range(60)
        ],
        md5s=[str(i) for i in range(60)],
    )
    center = index.centroid()
    monkeypatch.setattr(mixes, "load_index", lambda: index)
    monkeypatch.setattr(mixes, "_taste_vector", lambda _: (center, "test"))
    monkeypatch.setattr(mixes, "_for_you_forbidden_rows", lambda *_args, **_kwargs: set())

    day_one = mixes.generate_for_you(size=20, explore_ratio=0.2, seed=20260815)
    same_day = mixes.generate_for_you(size=20, explore_ratio=0.2, seed=20260815)
    day_two = mixes.generate_for_you(size=20, explore_ratio=0.2, seed=20260816)

    ids_one = [entry["track_id"] for entry in day_one.entries]
    assert ids_one == [entry["track_id"] for entry in same_day.entries]
    assert ids_one != [entry["track_id"] for entry in day_two.entries]


def test_mmr_diversifies_artists() -> None:
    matrix = np.repeat(np.eye(3, dtype=np.float32), 4, axis=0)
    index = EmbeddingIndex(
        track_ids=np.arange(1, 13, dtype=np.int64),
        matrix=matrix,
        meta=[
            {"id": i + 1, "artist": f"artist-{i // 4}", "title": f"track-{i}"}
            for i in range(12)
        ],
        md5s=[str(i) for i in range(12)],
    )
    picked = _mmr_select(
        index,
        np.ones(index.size, dtype=np.float32),
        size=6,
        lambda_=0.7,
        artist_penalty=0.2,
    )
    artists = {index.meta[row]["artist"] for row in picked}
    assert len(picked) == 6
    assert len(artists) == 3


def test_for_you_small_catalog_falls_back_when_everything_is_recent(monkeypatch) -> None:
    index = FakeIndex()
    monkeypatch.setattr(mixes, "load_index", lambda: index)
    monkeypatch.setattr(
        mixes,
        "_taste_vector",
        lambda _: (np.array([1.0, 0.0, 0.0], dtype=np.float32), "test"),
    )
    monkeypatch.setattr(
        mixes,
        "_for_you_forbidden_rows",
        lambda *_args, **_kwargs: set(range(index.size)),
    )

    playlist = mixes.generate_for_you(size=20, explore_ratio=0.25, seed=20260815)
    ids = [entry["track_id"] for entry in playlist.entries]
    assert len(ids) == index.size
    assert len(set(ids)) == index.size


def test_nightly_mix_pack_is_queued_once(monkeypatch, tmp_path) -> None:
    monkeypatch.setenv("MUSIK_DB_PATH", str(tmp_path / "musik.db"))
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("MUSIK_NIGHTLY_MIXES_ENABLED", "1")
    monkeypatch.setenv("MUSIK_NIGHTLY_MIXES_HOUR", "3")
    monkeypatch.setenv("MUSIK_NIGHTLY_MIXES_TIMEZONE", "UTC")
    get_settings.cache_clear()
    init_db()

    now = datetime(2026, 8, 15, 3, 5, tzinfo=timezone.utc)
    first = enqueue_nightly_mixes_if_due(now)
    second = enqueue_nightly_mixes_if_due(now)

    assert first is not None
    assert second is None
    jobs = list_recent()
    assert len(jobs) == 1
    assert jobs[0]["kind"] == "mix_pack"
    assert jobs[0]["payload"] == {"night": "2026-08-15", "trigger": "nightly"}
    get_settings.cache_clear()
