"""VK-style mix pack: for_you, today, new, weekdays, weekly."""

from __future__ import annotations

from datetime import date, datetime, timedelta, timezone
from typing import Any

import numpy as np

from musik.brain.generators import (
    PlaylistBuild,
    _forbidden_rows,
    _interleave,
    _mmr_select,
    _pick_far,
    _taste_vector,
    generate_daily,
    generate_weekly,
)
from musik.brain.store import latest_playlist
from musik.config import get_settings
from musik.db.schema import connect, utcnow
from musik.index.brute import EmbeddingIndex, load_index

WEEKDAY_KEYS = (
    ("weekday_mon", "Понедельник", 0),
    ("weekday_tue", "Вторник", 1),
    ("weekday_wed", "Среда", 2),
    ("weekday_thu", "Четверг", 3),
    ("weekday_fri", "Пятница", 4),
    ("weekday_sat", "Суббота", 5),
    ("weekday_sun", "Воскресенье", 6),
)


def _weekday_taste(index: EmbeddingIndex, weekday: int) -> tuple[np.ndarray, str, int]:
    """Blend global taste with tracks historically played on this weekday."""
    base, src = _taste_vector(index)
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT track_id, COUNT(*) AS c
            FROM listening_history
            WHERE weekday = ?
              AND action IN ('like', 'finish', 'track_end')
              AND (reason IS NULL OR reason != 'skipped')
            GROUP BY track_id
            ORDER BY c DESC
            LIMIT 40
            """,
            (weekday,),
        ).fetchall()
    if not rows:
        return base, src, 0
    acc = np.zeros(index.dim, dtype=np.float64)
    wsum = 0.0
    for r in rows:
        row = index.row_of(int(r["track_id"]))
        if row is None:
            continue
        w = float(r["c"])
        acc += w * index.matrix[row]
        wsum += w
    if wsum < 1e-9:
        return base, src, 0
    day_vec = (acc / wsum).astype(np.float32)
    n = float(np.linalg.norm(day_vec))
    if n > 1e-12:
        day_vec = day_vec / n
    blended = 0.55 * base + 0.45 * day_vec
    blended = blended / (np.linalg.norm(blended) + 1e-12)
    return blended.astype(np.float32), f"{src}+weekday_{weekday}", len(rows)


def _for_you_forbidden_rows(
    index: EmbeddingIndex,
    *,
    recent_days: int,
) -> set[int]:
    """Rows shown in the previous mix or heard recently; used as a soft exclusion."""
    track_ids: set[int] = set()
    previous = latest_playlist("for_you")
    if previous:
        track_ids.update(int(t["track_id"]) for t in previous["tracks"])
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT DISTINCT track_id
            FROM listening_history
            WHERE datetime(ts) >= datetime('now', ?)
              AND action IN ('start', 'finish', 'track_end', 'like', 'dislike')
            """,
            (f"-{max(1, recent_days)} days",),
        ).fetchall()
    track_ids.update(int(r["track_id"]) for r in rows)
    return {
        row
        for track_id in track_ids
        if (row := index.row_of(track_id)) is not None
    }


def generate_for_you(
    *,
    size: int = 40,
    explore_ratio: float | None = None,
    seed: int | None = None,
    recent_days: int = 7,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Strong personalization — 'Для вас'."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов")
    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    if seed is None:
        seed = int(date.today().strftime("%Y%m%d"))
    rng = np.random.default_rng(seed)
    center, center_src = _taste_vector(index)
    noise = rng.normal(0, 0.02, size=center.shape).astype(np.float32)
    query = center + noise
    query = query / (np.linalg.norm(query) + 1e-12)
    sims = index.sims_to_vector(query)

    pack_forbidden = _forbidden_rows(index, forbidden_track_ids)
    soft_forbidden = (
        _for_you_forbidden_rows(index, recent_days=recent_days) | pack_forbidden
    )
    preferred_size = min(size, max(0, index.size - len(soft_forbidden)))
    n_far = max(0, int(round(preferred_size * explore_ratio)))
    n_far = min(n_far, preferred_size // 2)
    n_near = preferred_size - n_far
    near = _mmr_select(
        index,
        sims,
        size=n_near,
        lambda_=0.76,
        forbidden=soft_forbidden,
    )
    far_forbidden = soft_forbidden | set(near)
    far = _pick_far(index, sims, k=n_far, forbidden=far_forbidden) if n_far else []
    # Small libraries may not have enough unseen tracks. Fill without the soft
    # history exclusion instead of returning a short playlist.
    if len(near) + len(far) < min(size, index.size):
        selected = set(near) | set(far)
        near.extend(
            _mmr_select(
                index,
                sims,
                size=min(size, index.size) - len(selected),
                lambda_=0.72,
                forbidden=selected,
            )
        )
    ordered = _interleave(near, far)
    entries = [
        {
            "track_id": int(index.meta[r]["id"]),
            "explanation": f"для вас · cosine {float(sims[r]):.3f}"
            + (" · explore" if is_ex else ""),
        }
        for r, is_ex in ordered[:size]
    ]
    return PlaylistBuild(
        kind="for_you",
        name="Для вас",
        entries=entries,
        meta={
            "size": len(entries),
            "center": center_src,
            "seed": seed,
            "recent_days": recent_days,
            "excluded": len(soft_forbidden),
            "pack_excluded": len(pack_forbidden),
            "explore_ratio": explore_ratio,
            "method": "taste+daily-noise+MMR+recent-exclusion",
        },
    )


def generate_weekday(
    weekday: int,
    *,
    size: int = 30,
    explore_ratio: float | None = None,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Плейлист дня недели (0=Пн … 6=Вс)."""
    if weekday < 0 or weekday > 6:
        raise ValueError("weekday 0..6")
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов")
    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    key, title, _ = WEEKDAY_KEYS[weekday]
    center, center_src, signal_count = _weekday_taste(index, weekday)
    # Rotate nightly while preserving a deterministic result for the same day.
    seed = int(f"{date.today().strftime('%Y%m%d')}{weekday}")
    rng = np.random.default_rng(seed)
    # With no weekday history all days share the global profile. A stronger
    # seeded perturbation prevents those fallback shelves from collapsing into
    # the same ranking while remaining deterministic for the current night.
    noise_scale = 0.035 if signal_count == 0 else 0.015
    noise = rng.normal(0, noise_scale, size=center.shape).astype(np.float32)
    q = center + noise
    q = q / (np.linalg.norm(q) + 1e-12)
    sims = index.sims_to_vector(q)
    forbidden = _forbidden_rows(index, forbidden_track_ids)
    target_size = min(size, max(0, index.size - len(forbidden)))
    n_far = (
        max(1, int(round(target_size * explore_ratio)))
        if explore_ratio > 0 and target_size > 0
        else 0
    )
    n_far = min(n_far, target_size // 2)
    n_near = target_size - n_far
    near = _mmr_select(
        index,
        sims,
        size=n_near,
        lambda_=0.7,
        forbidden=forbidden,
    )
    far = _pick_far(index, sims, k=n_far, forbidden=forbidden | set(near))
    if len(near) + len(far) < min(size, index.size):
        selected = set(near) | set(far)
        near.extend(
            _mmr_select(
                index,
                sims,
                size=min(size, index.size) - len(selected),
                forbidden=selected,
            )
        )
    ordered = _interleave(near, far)
    entries = [
        {
            "track_id": int(index.meta[r]["id"]),
            "explanation": f"{title} · cosine {float(sims[r]):.3f}"
            + (" · explore" if is_ex else ""),
        }
        for r, is_ex in ordered[:size]
    ]
    return PlaylistBuild(
        kind=key,
        name=f"Микс · {title}",
        entries=entries,
        meta={
            "weekday": weekday,
            "title": title,
            "size": len(entries),
            "center": center_src,
            "seed": seed,
            "weekday_signals": signal_count,
            "noise_scale": noise_scale,
            "pack_excluded": len(forbidden),
            "explore_ratio": explore_ratio,
        },
    )


def generate_new_releases(
    *,
    size: int = 25,
    days: int = 14,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Новинки из недавно добавленных треков / discover tips."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов")
    cutoff = datetime.now(timezone.utc) - timedelta(days=days)
    center, _ = _taste_vector(index)
    sims = index.sims_to_vector(center)
    forbidden_track_ids = forbidden_track_ids or set()

    with connect() as conn:
        tip_rows = conn.execute(
            """
            SELECT track_ids_json, score FROM discover_tips
            WHERE kind = 'new_album'
            ORDER BY score DESC LIMIT 30
            """
        ).fetchall()
        track_rows = conn.execute(
            """
            SELECT id, created_at FROM tracks
            WHERE is_active = 1 AND is_duplicate_of IS NULL
            """
        ).fetchall()

    import json

    cand_ids: list[int] = []
    for tr in tip_rows:
        try:
            ids = json.loads(tr["track_ids_json"] or "[]")
        except json.JSONDecodeError:
            ids = []
        for tid in ids:
            cand_ids.append(int(tid))

    created_ok: set[int] = set()
    for r in track_rows:
        try:
            ts = datetime.fromisoformat(str(r["created_at"]).replace("Z", "+00:00"))
        except ValueError:
            continue
        if ts >= cutoff:
            created_ok.add(int(r["id"]))

    # prefer tip tracks, then any recently scanned
    ordered_ids: list[int] = []
    seen: set[int] = set()
    for tid in cand_ids + sorted(created_ok, key=lambda t: -float(sims[index.row_of(t)]) if index.row_of(t) is not None else 0):
        if tid in seen or tid in forbidden_track_ids:
            continue
        if index.row_of(tid) is None:
            continue
        seen.add(tid)
        ordered_ids.append(tid)
        if len(ordered_ids) >= size:
            break

    # score by taste among candidates
    scored = []
    for tid in ordered_ids:
        row = index.row_of(tid)
        if row is None:
            continue
        scored.append((tid, float(sims[row])))
    scored.sort(key=lambda x: -x[1])

    entries = [
        {
            "track_id": tid,
            "explanation": f"новинка · cosine {sc:.3f} к вкусу",
        }
        for tid, sc in scored[:size]
    ]
    return PlaylistBuild(
        kind="new_releases",
        name="Новинки",
        entries=entries,
        meta={
            "size": len(entries),
            "days": days,
            "empty": len(entries) == 0,
            "pack_excluded": len(forbidden_track_ids),
        },
    )


def generate_mix_pack(
    *,
    daily_size: int = 30,
    for_you_size: int = 40,
    weekday_size: int = 30,
    weekly_size: int = 50,
    new_size: int = 25,
    reserve_prefix: int = 10,
) -> dict[str, Any]:
    """Build and persist the full VK-style shelf set (25–50 tracks)."""
    results: dict[str, Any] = {}
    reserved: set[int] = set()

    def reserve(build: PlaylistBuild) -> None:
        reserved.update(
            int(entry["track_id"])
            for entry in build.entries[: max(1, reserve_prefix)]
        )

    fy = generate_for_you(size=for_you_size)
    results["for_you"] = {"id": fy.persist(), "name": fy.name, "n": len(fy.entries)}
    reserve(fy)

    daily = generate_daily(size=daily_size, forbidden_track_ids=reserved)
    # rename for UX
    daily.name = f"На сегодня · {date.today().strftime('%d.%m')}"
    results["daily"] = {"id": daily.persist(), "name": daily.name, "n": len(daily.entries)}
    reserve(daily)

    neu = generate_new_releases(size=new_size, forbidden_track_ids=reserved)
    neu_id = neu.persist() if neu.entries else None
    results["new_releases"] = {
        "id": neu_id,
        "name": neu.name,
        "n": len(neu.entries),
        "empty": len(neu.entries) == 0,
    }
    reserve(neu)

    weekly = generate_weekly(size=weekly_size, forbidden_track_ids=reserved)
    results["weekly"] = {"id": weekly.persist(), "name": weekly.name, "n": len(weekly.entries)}
    reserve(weekly)

    weekdays = []
    for key, title, wd in WEEKDAY_KEYS:
        build = generate_weekday(
            wd,
            size=weekday_size,
            forbidden_track_ids=reserved,
        )
        pid = build.persist()
        weekdays.append({"kind": key, "id": pid, "name": build.name, "n": len(build.entries)})
        reserve(build)
    results["weekdays"] = weekdays
    results["reserved_prefix"] = max(1, reserve_prefix)
    results["reserved_tracks"] = len(reserved)
    results["generated_at"] = utcnow()
    return results


def mix_catalog() -> list[dict[str, Any]]:
    """Shelf cards for API/UI (latest of each kind)."""
    today_wd = date.today().weekday()  # Mon=0
    shelves: list[dict[str, Any]] = []

    def card(kind: str, title: str, subtitle: str, *, highlight: bool = False) -> dict[str, Any]:
        pl = latest_playlist(kind)
        n = len(pl["tracks"]) if pl else 0
        return {
            "kind": kind,
            "title": title,
            "subtitle": subtitle,
            "playlist_id": pl["id"] if pl else None,
            "tracks": n,
            "ready": pl is not None and n > 0,
            "highlight": highlight,
            "cover_track_id": pl["tracks"][0]["track_id"] if pl and pl["tracks"] else None,
        }

    shelves.append(card("for_you", "Для вас", "Персональный микс под вкус", highlight=True))
    shelves.append(card("daily", "На сегодня", "Свежий микс на день", highlight=True))
    shelves.append(
        {
            "kind": "later",
            "title": "Потом",
            "subtitle": "Отложенные треки",
            "playlist_id": None,
            "tracks": _later_count(),
            "ready": _later_count() > 0,
            "highlight": False,
            "cover_track_id": None,
            "special": "later",
        }
    )
    neu = card("new_releases", "Новинки", "Недавно в библиотеке")
    shelves.append(neu)
    shelves.append(card("weekly", "Недельный микс", f"Неделя {date.today().isocalendar().week}"))

    for key, title, wd in WEEKDAY_KEYS:
        shelves.append(
            card(
                key,
                title,
                "Микс дня недели",
                highlight=(wd == today_wd),
            )
        )
    return shelves


def _later_count() -> int:
    with connect() as conn:
        row = conn.execute("SELECT COUNT(*) AS c FROM listen_later").fetchone()
    return int(row["c"]) if row else 0


def ensure_later_table() -> None:
    with connect() as conn:
        conn.execute(
            """
            CREATE TABLE IF NOT EXISTS listen_later (
                track_id INTEGER PRIMARY KEY,
                added_at TEXT NOT NULL,
                position INTEGER NOT NULL DEFAULT 0
            )
            """
        )


def later_list() -> list[dict[str, Any]]:
    ensure_later_table()
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT l.track_id, l.added_at, l.position,
                   t.artist, t.title, t.duration
            FROM listen_later l
            JOIN tracks t ON t.id = l.track_id
            ORDER BY l.position ASC, l.added_at DESC
            """
        ).fetchall()
    return [dict(r) for r in rows]


def later_add(track_id: int) -> None:
    ensure_later_table()
    now = utcnow()
    with connect() as conn:
        row = conn.execute("SELECT COALESCE(MAX(position),0) AS m FROM listen_later").fetchone()
        pos = int(row["m"]) + 1
        conn.execute(
            """
            INSERT INTO listen_later(track_id, added_at, position) VALUES (?,?,?)
            ON CONFLICT(track_id) DO UPDATE SET added_at=excluded.added_at
            """,
            (track_id, now, pos),
        )


def later_remove(track_id: int) -> None:
    ensure_later_table()
    with connect() as conn:
        conn.execute("DELETE FROM listen_later WHERE track_id = ?", (track_id,))
