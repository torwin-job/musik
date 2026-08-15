"""Playlist generators: Daily / Radio / Weekly / Mood + near/far diversity."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date
from typing import Any

import numpy as np

from musik.brain.explain import explain_daily, explain_mood, explain_radio, explain_weekly
from musik.brain.store import save_playlist
from musik.config import get_settings
from musik.index.brute import EmbeddingIndex, load_index
from musik.listen.profile import resolve_taste


@dataclass
class PlaylistBuild:
    kind: str
    name: str
    entries: list[dict[str, Any]] = field(default_factory=list)
    meta: dict[str, Any] = field(default_factory=dict)
    playlist_id: int | None = None

    def persist(self) -> int:
        self.playlist_id = save_playlist(
            kind=self.kind, name=self.name, entries=self.entries, meta=self.meta
        )
        return self.playlist_id


def _mmr_select(
    index: EmbeddingIndex,
    query_sims: np.ndarray,
    *,
    size: int,
    lambda_: float = 0.7,
    artist_penalty: float = 0.15,
    forbidden: set[int] | None = None,
) -> list[int]:
    """Maximal Marginal Relevance. Returns row indices."""
    n = index.size
    size = min(size, n)
    selected: list[int] = []
    remaining = set(range(n))
    if forbidden:
        remaining -= forbidden
    artist_counts: dict[str, int] = {}

    for _ in range(size):
        best_i = None
        best_score = -1e9
        for i in remaining:
            rel = float(query_sims[i])
            if selected:
                div = float(np.max(index.matrix[i] @ index.matrix[selected].T))
            else:
                div = 0.0
            artist = (index.meta[i].get("artist") or "").strip().lower()
            pen = artist_penalty * artist_counts.get(artist, 0)
            score = lambda_ * rel - (1.0 - lambda_) * div - pen
            if score > best_score:
                best_score = score
                best_i = i
        if best_i is None:
            break
        selected.append(best_i)
        remaining.remove(best_i)
        artist = (index.meta[best_i].get("artist") or "").strip().lower()
        artist_counts[artist] = artist_counts.get(artist, 0) + 1
    return selected


def _pick_far(
    index: EmbeddingIndex,
    query_sims: np.ndarray,
    *,
    k: int,
    forbidden: set[int],
    far_quantile: float = 0.25,
) -> list[int]:
    """
    Pick k tracks from the most distant quartile vs query (low cosine),
    with MMR among that pool so far tracks aren't clones of each other.
    """
    if k <= 0 or index.size == 0:
        return []
    thresh = float(np.quantile(query_sims, far_quantile))
    pool = [i for i in range(index.size) if i not in forbidden and query_sims[i] <= thresh]
    if len(pool) < k:
        # expand: take global farthest
        order = np.argsort(query_sims)  # ascending = farthest
        pool = [int(i) for i in order if int(i) not in forbidden][: max(k * 4, k)]
    if not pool:
        return []
    # relevance for MMR among far = distance = -cosine
    far_sims = np.full(index.size, -2.0, dtype=np.float32)
    for i in pool:
        far_sims[i] = -float(query_sims[i])
    return _mmr_select(
        index,
        far_sims,
        size=min(k, len(pool)),
        lambda_=0.55,
        artist_penalty=0.2,
        forbidden=forbidden,
    )


def _interleave(near: list[int], far: list[int]) -> list[tuple[int, bool]]:
    """Merge near/far lists → (row, is_explore). Spread far tracks through the mix."""
    if not far:
        return [(r, False) for r in near]
    out: list[tuple[int, bool]] = []
    fi = 0
    # insert a far track roughly every (len(near)//len(far)+1) near items
    gap = max(2, len(near) // max(1, len(far)))
    since = 0
    for r in near:
        out.append((r, False))
        since += 1
        if fi < len(far) and since >= gap:
            out.append((far[fi], True))
            fi += 1
            since = 0
    while fi < len(far):
        out.append((far[fi], True))
        fi += 1
    return out


def _taste_vector(index: EmbeddingIndex) -> tuple[np.ndarray, str]:
    return resolve_taste(index)


def _forbidden_rows(
    index: EmbeddingIndex,
    track_ids: set[int] | None,
) -> set[int]:
    if not track_ids:
        return set()
    return {
        row
        for track_id in track_ids
        if (row := index.row_of(track_id)) is not None
    }


def generate_daily(
    *,
    size: int = 30,
    seed: int | None = None,
    explore_ratio: float | None = None,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Daily: вкус (или центроид) + MMR near + дальние exploration-треки."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов — сначала musik embed")

    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio

    if seed is None:
        seed = int(date.today().strftime("%Y%m%d"))
    rng = np.random.default_rng(seed)

    center, center_src = _taste_vector(index)
    noise = rng.normal(0, 0.02, size=center.shape).astype(np.float32)
    q = center + noise
    q = q / (np.linalg.norm(q) + 1e-12)
    sims = index.sims_to_vector(q)
    forbidden = _forbidden_rows(index, forbidden_track_ids)

    target_size = min(size, max(0, index.size - len(forbidden)))
    n_far = int(round(target_size * explore_ratio))
    if size >= 5 and explore_ratio > 0:
        n_far = max(1, n_far)
    n_far = min(n_far, max(0, target_size // 2))
    n_near = target_size - n_far

    near = _mmr_select(
        index,
        sims,
        size=n_near,
        lambda_=0.72,
        forbidden=forbidden,
    )
    far = _pick_far(index, sims, k=n_far, forbidden=forbidden | set(near))
    # if far short, fill near
    if len(near) + len(far) < min(size, index.size):
        selected = set(near) | set(far)
        more = _mmr_select(
            index,
            sims,
            size=min(size, index.size) - len(selected),
            forbidden=selected,
        )
        near.extend(more)

    ordered = _interleave(near, far)
    entries = []
    for r, is_ex in ordered[:size]:
        m = index.meta[r]
        entries.append(
            {
                "track_id": int(m["id"]),
                "explanation": explain_daily(
                    cosine=float(sims[r]),
                    artist=m.get("artist") or "",
                    cluster=m.get("cluster_id"),
                    diversify=True,
                    explore=is_ex,
                ),
            }
        )
    return PlaylistBuild(
        kind="daily",
        name=f"Daily Mix · {date.today().isoformat()}",
        entries=entries,
        meta={
            "size": len(entries),
            "seed": seed,
            "center": center_src,
            "pack_excluded": len(forbidden),
            "explore_ratio": explore_ratio,
            "n_near": n_near,
            "n_far": len(far),
            "method": "taste+MMR+far-quartile",
        },
    )


def generate_radio(
    *,
    seed_track_id: int,
    size: int = 30,
    explore_ratio: float | None = None,
) -> PlaylistBuild:
    """Radio: цепочка похожих + редкие прыжки к далёким трекам."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов — сначала musik embed")
    start = index.row_of(seed_track_id)
    if start is None:
        raise KeyError(f"seed track_id={seed_track_id} not in index")

    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    rng = np.random.default_rng(seed_track_id + size)

    used = {start}
    used_md5 = {index.md5s[start]} if index.md5s[start] else set()
    order: list[tuple[int, float, bool]] = [(start, 1.0, False)]
    cur = start
    seed_title = index.meta[start].get("title") or str(seed_track_id)

    for hop in range(1, size):
        do_explore = rng.random() < explore_ratio
        sims = index.matrix @ index.matrix[cur]
        best_j = None
        best_s = -2.0

        if do_explore:
            # farthest unused (but not identical md5)
            for j in range(index.size):
                if j in used:
                    continue
                if index.md5s[j] and index.md5s[j] in used_md5:
                    continue
                s = float(sims[j])
                # want low similarity
                score = -s
                if score > best_s:
                    best_s = score
                    best_j = j
            if best_j is not None:
                cos = float(sims[best_j])
                used.add(best_j)
                if index.md5s[best_j]:
                    used_md5.add(index.md5s[best_j])
                order.append((best_j, cos, True))
                cur = best_j
                continue

        best_s = -2.0
        best_j = None
        for j in range(index.size):
            if j in used:
                continue
            if index.md5s[j] and index.md5s[j] in used_md5:
                continue
            s = float(sims[j])
            if (index.meta[j].get("artist") or "") == (index.meta[cur].get("artist") or ""):
                s -= 0.03
            if s > best_s:
                best_s = s
                best_j = j
        if best_j is None:
            break
        used.add(best_j)
        if index.md5s[best_j]:
            used_md5.add(index.md5s[best_j])
        order.append((best_j, best_s, False))
        cur = best_j

    entries = []
    prev_title = seed_title
    for hop, (row, cos, is_ex) in enumerate(order):
        m = index.meta[row]
        title = m.get("title") or ""
        entries.append(
            {
                "track_id": int(m["id"]),
                "explanation": explain_radio(
                    cosine=cos if hop else 1.0,
                    seed_title=prev_title if hop else title,
                    hop=hop,
                    explore=is_ex,
                ),
            }
        )
        prev_title = title

    return PlaylistBuild(
        kind="radio",
        name=f"Radio · {index.meta[start].get('artist') or ''} — {seed_title}",
        entries=entries,
        meta={
            "seed_track_id": seed_track_id,
            "size": len(entries),
            "explore_ratio": explore_ratio,
        },
    )


def generate_weekly(
    *,
    size: int = 50,
    seed: int | None = None,
    explore_ratio: float | None = None,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Weekly: корзины по кластерам + явный far-хвост."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов — сначала musik embed")

    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    if seed is None:
        seed = int(date.today().strftime("%G%V"))
    rng = np.random.default_rng(seed)

    center, center_src = _taste_vector(index)
    sims = index.sims_to_vector(center)
    forbidden = _forbidden_rows(index, forbidden_track_ids)

    target_size = min(size, max(0, index.size - len(forbidden)))
    n_far = max(1, int(round(target_size * explore_ratio))) if explore_ratio > 0 else 0
    n_far = min(n_far, target_size // 2)
    n_main = target_size - n_far

    clusters: dict[int | None, list[int]] = {}
    for i, m in enumerate(index.meta):
        if i in forbidden:
            continue
        clusters.setdefault(m.get("cluster_id"), []).append(i)

    keys = list(clusters.keys())
    rng.shuffle(keys)
    per = max(1, n_main // max(1, len(keys)))
    picked: list[int] = []
    bucket_of: dict[int, str] = {}

    for key in keys:
        rows = clusters[key][:]
        rng.shuffle(rows)
        sub = rows[: min(len(rows), per * 3)]
        if not sub:
            continue
        cvec = index.centroid(sub)
        local = index.matrix[sub] @ cvec
        order = np.argsort(-local)
        for j in order[:per]:
            r = sub[int(j)]
            if r not in picked:
                picked.append(r)
                bucket_of[r] = f"cluster={key}"
        if len(picked) >= n_main:
            break

    if len(picked) < n_main:
        rest = [i for i in range(index.size) if i not in forbidden and i not in picked]
        rng.shuffle(rest)
        for r in rest:
            picked.append(r)
            bucket_of[r] = "fill"
            if len(picked) >= n_main:
                break

    far = _pick_far(index, sims, k=n_far, forbidden=forbidden | set(picked))
    for r in far:
        bucket_of[r] = "far-tail"
    if len(picked) + len(far) < min(size, index.size):
        selected = set(picked) | set(far)
        fill = _mmr_select(
            index,
            sims,
            size=min(size, index.size) - len(selected),
            forbidden=selected,
        )
        for r in fill:
            bucket_of[r] = "fallback-fill"
        picked.extend(fill)

    ordered = _interleave(picked[:n_main], far)
    if len(ordered) < min(size, index.size):
        already = {row for row, _ in ordered}
        ordered.extend((row, False) for row in picked if row not in already)
    entries = []
    for r, is_ex in ordered[:size]:
        m = index.meta[r]
        bucket = bucket_of.get(r, "?")
        if is_ex:
            bucket = "exploration-far"
        entries.append(
            {
                "track_id": int(m["id"]),
                "explanation": explain_weekly(
                    cosine=float(sims[r]),
                    cluster=m.get("cluster_id"),
                    bucket=bucket,
                ),
            }
        )
    return PlaylistBuild(
        kind="weekly",
        name=f"Weekly Mix · {date.today().isocalendar().week} неделя",
        entries=entries,
        meta={
            "size": len(entries),
            "seed": seed,
            "center": center_src,
            "pack_excluded": len(forbidden),
            "explore_ratio": explore_ratio,
            "n_far": len(far),
            "method": "cluster-buckets+far",
        },
    )


def generate_mood(
    *,
    mood: str = "energy",
    size: int = 25,
    explore_ratio: float | None = None,
) -> PlaylistBuild:
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов — сначала musik embed")

    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    mood = mood.lower().strip()
    center = index.centroid()

    scores = np.zeros(index.size, dtype=np.float32)
    has_scalar = False
    for i, m in enumerate(index.meta):
        s = 0.0
        if m.get("bpm") is not None:
            s += float(m["bpm"]) / 140.0
            has_scalar = True
        if m.get("lufs") is not None:
            s += (float(m["lufs"]) + 30.0) / 20.0
            has_scalar = True
        scores[i] = s

    if has_scalar and float(scores.std()) > 1e-6:
        order = np.argsort(-scores) if mood == "energy" else np.argsort(scores)
        pool = order[: max(size * 3, size)].tolist()
    else:
        sims_c = index.sims_to_vector(center)
        order = np.argsort(sims_c) if mood == "energy" else np.argsort(-sims_c)
        pool = order[: max(size * 3, size)].tolist()

    local = index.centroid(pool[: min(len(pool), size * 2)])
    sims = index.sims_to_vector(local)
    mask_sims = np.full(index.size, -2.0, dtype=np.float32)
    for i in pool:
        mask_sims[i] = sims[i]

    n_far = max(1, int(round(size * explore_ratio))) if size >= 5 and explore_ratio > 0 else 0
    n_near = size - n_far
    near = _mmr_select(index, mask_sims, size=n_near, lambda_=0.65)
    # far relative to mood local center, from outside pool preferred
    far = _pick_far(index, sims, k=n_far, forbidden=set(near))
    ordered = _interleave(near, far)

    entries = []
    for r, is_ex in ordered[:size]:
        m = index.meta[r]
        exp = explain_mood(
            cosine=float(sims[r]),
            mood=mood,
            lufs=float(m["lufs"]) if m.get("lufs") is not None else None,
            bpm=float(m["bpm"]) if m.get("bpm") is not None else None,
        )
        if is_ex:
            exp = "exploration · " + exp
        entries.append({"track_id": int(m["id"]), "explanation": exp})
    return PlaylistBuild(
        kind="mood",
        name=f"Mood · {mood}",
        entries=entries,
        meta={
            "mood": mood,
            "size": len(entries),
            "scalars": has_scalar,
            "explore_ratio": explore_ratio,
            "n_far": len(far),
        },
    )
