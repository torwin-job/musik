from __future__ import annotations

import logging
from pathlib import Path
from typing import Optional

import typer
from rich.console import Console
from rich.table import Table

from musik.brain import (
    generate_daily,
    generate_mix_pack,
    generate_mood,
    generate_radio,
    generate_weekly,
    get_playlist,
    list_playlists,
    mix_catalog,
)
from musik.config import get_settings
from musik.db import counts, ensure_db, list_active_tracks
from musik.discover import rebuild_discover_tips
from musik.embed import embed_library
from musik.embed.segments import SEGMENT_STRATEGY
from musik.index import assign_clusters, find_tracks, get_track, list_cluster_summary, load_index
from musik.jobs import enqueue_job, list_recent
from musik.listen import (
    build_profile,
    history_counts,
    recent_history,
    record_listen,
    top_transitions,
)
from musik.scanner import scan_library
from musik.worker import serve as serve_worker

app = typer.Typer(add_completion=False, no_args_is_help=True, help="Умный локальный плеер (ТЗ 3.0)")
playlist_app = typer.Typer(help="Шаг 4: генерация плейлистов (Daily/Radio/Weekly/Mood)")
listen_app = typer.Typer(help="Шаг 5: история слушания / лайки / скипы")
jobs_app = typer.Typer(help="Фоновые задачи (очередь jobs)")
discover_app = typer.Typer(help="Discover: подсказки альбомов")
app.add_typer(playlist_app, name="playlist")
app.add_typer(listen_app, name="listen")
app.add_typer(jobs_app, name="jobs")
app.add_typer(discover_app, name="discover")
console = Console()


def _setup_logging(verbose: bool) -> None:
    logging.basicConfig(
        level=logging.DEBUG if verbose else logging.INFO,
        format="%(levelname)s %(name)s: %(message)s",
    )


@app.command()
def scan(
    library: Optional[Path] = typer.Option(
        None, "--library", "-l", help="Путь к музыкальной библиотеке (или MUSIK_LIBRARY)"
    ),
    workers: Optional[int] = typer.Option(None, "--workers", "-w", help="Параллельные воркеры"),
    limit: Optional[int] = typer.Option(None, "--limit", help="Ограничить число файлов (для теста)"),
    tags_only: bool = typer.Option(
        False, "--tags-only", help="Только теги/MD5, без fingerprint/LUFS/BPM/key"
    ),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Шаг 1: сканировать библиотеку — теги, MD5, дедуп, LUFS/BPM/key."""
    _setup_logging(verbose)
    settings = get_settings()
    lib = library or settings.library
    console.print(f"[bold]Library:[/bold] {lib}")
    console.print(
        "[yellow]Без GPU полный CLAP-эмбеддинг (шаг 2) займёт часы на большой коллекции. "
        "Сейчас считаются только теги и точечные параметры.[/yellow]"
    )
    def _progress(p: dict) -> None:
        msg = p.get("message") or ""
        if msg:
            console.print(f"[dim]{msg}[/dim]")

    result = scan_library(
        lib,
        extract_audio=not tags_only,
        workers=workers,
        limit=limit,
        on_progress=_progress,
    )
    table = Table(title="Scan result")
    table.add_column("metric")
    table.add_column("value", justify="right")
    for k, v in [
        ("files found", result.scanned),
        ("upserted", result.upserted),
        ("failed", result.failed),
        ("inactivated (missing)", result.inactivated),
        ("duplicates marked", result.duplicates_marked),
    ]:
        table.add_row(k, str(v))
    console.print(table)
    st = counts()
    console.print(
        f"DB: active={st['tracks_active']} total={st['tracks_total']} "
        f"features pending={st['features_pending']} ready={st['features_ready']}"
    )


@app.command()
def embed(
    limit: Optional[int] = typer.Option(None, "--limit", help="Сколько треков обработать (тест)"),
    force: bool = typer.Option(False, "--force", help="Пересчитать даже если status=ready"),
    workers: Optional[int] = typer.Option(
        None, "--workers", "-w", help="Параллелизм (по умолчанию 1 — модель не потокобезопасна)"
    ),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Шаг 2: CLAP-эмбеддинги — 30с начало + середина + конец, кеш по MD5."""
    _setup_logging(verbose)
    settings = get_settings()
    console.print(
        f"[bold]Embedding[/bold] strategy={SEGMENT_STRATEGY} "
        f"segment={settings.embed_segment_sec:.0f}s × 3 windows → mean"
    )
    def _progress(p: dict) -> None:
        msg = p.get("message") or ""
        if msg:
            console.print(f"[dim]{msg}[/dim]")

    result = embed_library(
        limit=limit,
        force=force,
        workers=workers if workers is not None else settings.embed_workers,
        on_progress=_progress,
    )
    table = Table(title="Embed result")
    table.add_column("metric")
    table.add_column("value", justify="right")
    for k, v in [
        ("queued", result.total),
        ("from cache", result.from_cache),
        ("computed", result.computed),
        ("failed", result.failed),
        ("missing file", result.skipped_missing),
    ]:
        table.add_row(k, str(v))
    console.print(table)
    st = counts()
    console.print(
        f"DB: features pending={st['features_pending']} "
        f"ready={st['features_ready']} failed={st['features_failed']}"
    )


@app.command("status")
def status_cmd() -> None:
    """Показать состояние индекса."""
    ensure_db()
    st = counts()
    table = Table(title="Musik status")
    table.add_column("key")
    table.add_column("value", justify="right")
    for k, v in st.items():
        table.add_row(k, str(v))
    console.print(table)
    try:
        idx = load_index()
        console.print(f"Index: n={idx.size} dim={idx.dim} (brute cosine)")
    except Exception as exc:
        console.print(f"[yellow]Index load skipped: {exc}[/yellow]")
    rows = list_active_tracks(10)
    if rows:
        t = Table(title="Sample tracks")
        t.add_column("id")
        t.add_column("artist")
        t.add_column("title")
        t.add_column("bitrate")
        for r in rows:
            t.add_row(
                str(r["id"]),
                r.get("artist") or "",
                r.get("title") or "",
                str(r.get("bitrate") or ""),
            )
        console.print(t)


@app.command()
def similar(
    query: str = typer.Argument(..., help="track id (число) или текст artist/title"),
    k: int = typer.Option(10, "--k", "-k", help="Сколько соседей"),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Шаг 3: найти похожие треки по CLAP cosine."""
    _setup_logging(verbose)
    ensure_db()
    track_id: int | None = None
    if query.strip().isdigit():
        track_id = int(query.strip())
    else:
        hits = find_tracks(query, limit=10)
        if not hits:
            console.print(f"[red]Ничего не найдено по запросу:[/red] {query}")
            raise typer.Exit(1)
        if len(hits) > 1:
            t = Table(title="Уточните id — несколько совпадений")
            t.add_column("id")
            t.add_column("artist")
            t.add_column("title")
            t.add_column("embed")
            for h in hits:
                t.add_row(str(h["id"]), h.get("artist") or "", h.get("title") or "", h.get("status") or "")
            console.print(t)
            console.print("Повторите: musik similar <id>")
            raise typer.Exit(2)
        track_id = int(hits[0]["id"])

    assert track_id is not None
    seed = get_track(track_id)
    if not seed:
        console.print(f"[red]track_id={track_id} не найден[/red]")
        raise typer.Exit(1)

    idx = load_index()
    console.print(
        f"[bold]Seed[/bold] #{track_id}  {seed.get('artist') or ''} — {seed.get('title') or ''}  "
        f"(index n={idx.size})"
    )
    try:
        neighbors = idx.neighbors(track_id, k=k)
    except KeyError as exc:
        console.print(f"[red]{exc}[/red]")
        raise typer.Exit(1) from exc

    table = Table(title=f"Top-{k} similar (cosine)")
    table.add_column("#", justify="right")
    table.add_column("cos", justify="right")
    table.add_column("id", justify="right")
    table.add_column("artist")
    table.add_column("title")
    for i, n in enumerate(neighbors, 1):
        table.add_row(str(i), f"{n.cosine:.4f}", str(n.track_id), n.artist, n.title)
    console.print(table)


@app.command()
def clusters(
    k: int = typer.Option(8, "--k", "-k", help="Число кластеров k-means"),
    show: Optional[int] = typer.Option(None, "--show", help="Показать треки кластера N"),
    rebuild: bool = typer.Option(True, "--rebuild/--no-rebuild", help="Пересчитать k-means"),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Шаг 3: кластеры по CLAP (k-means на cosine)."""
    _setup_logging(verbose)
    ensure_db()
    if rebuild and show is None:
        result = assign_clusters(k=k)
        console.print(
            f"Clusters: k={result.k} n={result.n} inertia(1-cos)={result.inertia:.4f}"
        )
    summary = list_cluster_summary()
    if not summary:
        console.print("[yellow]Кластеров нет — запустите musik clusters -k 8[/yellow]")
        raise typer.Exit(1)
    table = Table(title="Cluster summary")
    table.add_column("cluster", justify="right")
    table.add_column("n", justify="right")
    table.add_column("sample artist")
    for row in summary:
        table.add_row(str(row["cluster_id"]), str(row["n"]), row.get("sample_artist") or "")
    console.print(table)
    if show is not None:
        from musik.index.clusters import cluster_members

        members = cluster_members(show, limit=40)
        t = Table(title=f"Cluster {show}")
        t.add_column("id")
        t.add_column("artist")
        t.add_column("title")
        for m in members:
            t.add_row(str(m["id"]), m.get("artist") or "", m.get("title") or "")
        console.print(t)


@app.command()
def init() -> None:
    """Создать каталоги и пустую БД."""
    settings = get_settings()
    settings.ensure_dirs()
    ensure_db()
    console.print(f"DB ready: {settings.db_path}")


@app.command("lyrics")
def lyrics_cmd(
    limit: Optional[int] = typer.Option(None, "--limit", help="Сколько треков обработать"),
    force: bool = typer.Option(False, "--force", help="Перекачать даже если уже ready"),
    track: Optional[str] = typer.Option(
        None, "--track", "-t", help="Один трек: id или поисковая строка"
    ),
    delay: float = typer.Option(0.35, "--delay", help="Пауза между запросами к LRCLIB (сек)"),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Скачать тексты с LRCLIB и сохранить в таблицу lyrics."""
    _setup_logging(verbose)
    ensure_db()
    from musik.lyrics import fetch_library_lyrics, get_lyrics
    from musik.lyrics.lrclib import fetch_lyrics
    from musik.lyrics.store import upsert_lyrics

    if track:
        tid = _resolve_track_arg(track)
        meta = get_track(tid)
        if not meta:
            console.print(f"[red]Нет трека {tid}[/red]")
            raise typer.Exit(1)
        hit = fetch_lyrics(
            artist=meta.get("artist") or "",
            title=meta.get("title") or "",
            album=meta.get("album") or "",
            duration_sec=meta.get("duration"),
        )
        if hit is None:
            upsert_lyrics(tid, status="missing", source="lrclib", error="not found")
            console.print("[yellow]Текст не найден[/yellow]")
            raise typer.Exit(2)
        upsert_lyrics(
            tid,
            plain_lyrics=hit.get("plain_lyrics") or "",
            synced_lyrics=hit.get("synced_lyrics") or "",
            source=hit.get("source") or "lrclib",
            source_id=hit.get("source_id") or "",
            instrumental=bool(hit.get("instrumental")),
            status="ready",
        )
        stored = get_lyrics(tid)
        console.print(f"[green]OK[/green] track={tid} source={stored and stored.get('source')}")
        text = (stored or {}).get("plain_lyrics") or ""
        console.print(text[:2000] + ("…" if len(text) > 2000 else ""))
        return

    result = fetch_library_lyrics(limit=limit, force=force, delay_sec=delay)
    table = Table(title="Lyrics result")
    table.add_column("metric")
    table.add_column("value", justify="right")
    for k, v in [
        ("queued", result.total),
        ("found", result.found),
        ("missing", result.missing),
        ("failed", result.failed),
    ]:
        table.add_row(k, str(v))
    console.print(table)


def _print_playlist(pl: dict) -> None:
    console.print(f"[bold]#{pl['id']}[/bold] [{pl['kind']}] {pl['name']}  ({pl['created_at']})")
    table = Table()
    table.add_column("#", justify="right")
    table.add_column("id", justify="right")
    table.add_column("artist")
    table.add_column("title")
    table.add_column("why")
    for t in pl.get("tracks") or []:
        table.add_row(
            str(t["position"] + 1),
            str(t["track_id"]),
            t.get("artist") or "",
            t.get("title") or "",
            (t.get("explanation") or "")[:80],
        )
    console.print(table)


def _resolve_track_arg(query: str) -> int:
    if query.strip().isdigit():
        return int(query.strip())
    hits = find_tracks(query, limit=5)
    if not hits:
        console.print(f"[red]Трек не найден:[/red] {query}")
        raise typer.Exit(1)
    if len(hits) > 1:
        for h in hits:
            console.print(f"  {h['id']}: {h.get('artist')} — {h.get('title')}")
        console.print("Укажите точный id")
        raise typer.Exit(2)
    return int(hits[0]["id"])


@playlist_app.command("daily")
def playlist_daily(
    size: int = typer.Option(25, "--size", "-n"),
    explore: Optional[float] = typer.Option(
        None, "--explore", "-e", help="Доля дальних треков 0..1 (default MUSIK_EXPLORE_RATIO)"
    ),
    save: bool = typer.Option(True, "--save/--no-save"),
) -> None:
    """Daily Mix: вкус/центроид + MMR + дальние exploration-треки."""
    ensure_db()
    build = generate_daily(size=size, explore_ratio=explore)
    console.print(
        f"center={build.meta.get('center')} near={build.meta.get('n_near')} "
        f"far={build.meta.get('n_far')} explore={build.meta.get('explore_ratio')}"
    )
    if save:
        pid = build.persist()
        console.print(f"[green]Saved playlist #{pid}[/green] · {build.name} · {len(build.entries)} tracks")
        _print_playlist(get_playlist(pid))  # type: ignore[arg-type]
    else:
        console.print(f"{build.name} · {len(build.entries)} tracks (not saved)")


@playlist_app.command("radio")
def playlist_radio(
    seed: str = typer.Argument(..., help="track id или текст для поиска сида"),
    size: int = typer.Option(25, "--size", "-n"),
    explore: Optional[float] = typer.Option(None, "--explore", "-e", help="Вероятность прыжка вдаль"),
    save: bool = typer.Option(True, "--save/--no-save"),
) -> None:
    """Radio: цепочка похожих + редкие прыжки к далёким трекам."""
    ensure_db()
    seed_id = _resolve_track_arg(seed)
    build = generate_radio(seed_track_id=seed_id, size=size, explore_ratio=explore)
    if save:
        pid = build.persist()
        console.print(f"[green]Saved playlist #{pid}[/green] · {build.name}")
        _print_playlist(get_playlist(pid))  # type: ignore[arg-type]
    else:
        console.print(build.name)


@playlist_app.command("weekly")
def playlist_weekly(
    size: int = typer.Option(40, "--size", "-n"),
    explore: Optional[float] = typer.Option(None, "--explore", "-e"),
    save: bool = typer.Option(True, "--save/--no-save"),
) -> None:
    """Weekly Mix: корзины по кластерам + far-хвост."""
    ensure_db()
    build = generate_weekly(size=size, explore_ratio=explore)
    if save:
        pid = build.persist()
        console.print(f"[green]Saved playlist #{pid}[/green] · {build.name}")
        _print_playlist(get_playlist(pid))  # type: ignore[arg-type]


@playlist_app.command("mixes")
def playlist_mixes(
    save: bool = typer.Option(True, "--save/--no-save"),
) -> None:
    """Собрать полки VK-style: Для вас, Сегодня, Новинки, Пн–Вс, Неделя."""
    ensure_db()
    if save:
        result = generate_mix_pack()
        console.print_json(data=result)
    else:
        for card in mix_catalog():
            console.print(
                f"{card['title']}: ready={card['ready']} tracks={card['tracks']} kind={card['kind']}"
            )


@playlist_app.command("mood")
def playlist_mood(
    mood: str = typer.Argument("energy", help="energy | calm"),
    size: int = typer.Option(25, "--size", "-n"),
    explore: Optional[float] = typer.Option(None, "--explore", "-e"),
    save: bool = typer.Option(True, "--save/--no-save"),
) -> None:
    """Mood-плейлист (energy/calm) + exploration."""
    ensure_db()
    if mood not in ("energy", "calm"):
        console.print("[red]mood: energy или calm[/red]")
        raise typer.Exit(1)
    build = generate_mood(mood=mood, size=size, explore_ratio=explore)
    if save:
        pid = build.persist()
        console.print(f"[green]Saved playlist #{pid}[/green] · {build.name}")
        _print_playlist(get_playlist(pid))  # type: ignore[arg-type]


@playlist_app.command("list")
def playlist_list_cmd() -> None:
    """Список сохранённых плейлистов."""
    ensure_db()
    rows = list_playlists()
    table = Table(title="Playlists")
    table.add_column("id", justify="right")
    table.add_column("kind")
    table.add_column("name")
    table.add_column("n", justify="right")
    table.add_column("created")
    for r in rows:
        table.add_row(str(r["id"]), r["kind"], r["name"], str(r["n"]), r["created_at"])
    console.print(table)


@playlist_app.command("show")
def playlist_show_cmd(playlist_id: int = typer.Argument(...)) -> None:
    """Показать плейлист с explanations."""
    ensure_db()
    pl = get_playlist(playlist_id)
    if not pl:
        console.print(f"[red]playlist #{playlist_id} не найден[/red]")
        raise typer.Exit(1)
    _print_playlist(pl)


@listen_app.command("finish")
def listen_finish(
    track: str = typer.Argument(...),
    prev: Optional[str] = typer.Option(None, "--prev", help="Предыдущий трек (для transitions)"),
) -> None:
    """Отметить дослушивание."""
    ensure_db()
    tid = _resolve_track_arg(track)
    prev_id = _resolve_track_arg(prev) if prev else None
    hid = record_listen(tid, "finish", prev_track_id=prev_id)
    console.print(f"finish #{hid} track={tid}")


@listen_app.command("skip")
def listen_skip(track: str = typer.Argument(...)) -> None:
    """Отметить скип (тянет профиль вкуса в сторону)."""
    ensure_db()
    tid = _resolve_track_arg(track)
    hid = record_listen(tid, "skip")
    console.print(f"skip #{hid} track={tid}")


@listen_app.command("like")
def listen_like(track: str = typer.Argument(...)) -> None:
    ensure_db()
    tid = _resolve_track_arg(track)
    hid = record_listen(tid, "like")
    console.print(f"like #{hid} track={tid}")


@listen_app.command("dislike")
def listen_dislike(track: str = typer.Argument(...)) -> None:
    ensure_db()
    tid = _resolve_track_arg(track)
    hid = record_listen(tid, "dislike")
    console.print(f"dislike #{hid} track={tid}")


@listen_app.command("history")
def listen_history_cmd(limit: int = typer.Option(20, "--limit", "-n")) -> None:
    ensure_db()
    rows = recent_history(limit)
    table = Table(title="Listening history")
    table.add_column("id")
    table.add_column("action")
    table.add_column("artist")
    table.add_column("title")
    table.add_column("ts")
    for r in rows:
        table.add_row(
            str(r["id"]),
            r["action"],
            r.get("artist") or "",
            r.get("title") or "",
            (r.get("ts") or "")[:19],
        )
    console.print(table)
    console.print(history_counts())


@listen_app.command("profile")
def listen_profile_cmd() -> None:
    """Показать online (Go) и offline (Python) профили; offline пересобрать в offline_report."""
    ensure_db()
    from musik.listen.profile import CONTEXT_ONLINE, latest_profile, resolve_taste

    online = latest_profile(CONTEXT_ONLINE)
    if online is not None:
        console.print(
            f"[green]online Go EMA[/green] context=global dim={online.embedding.size}"
        )
    else:
        console.print("[yellow]online Go EMA[/yellow]: нет снапшота global")
    p = build_profile(persist=True)  # writes offline_report only
    console.print(
        f"offline_report ready={p.ready} dim={p.embedding.size} "
        f"pos={p.n_positive} neg={p.n_negative} context={p.context}"
    )
    _vec, src = resolve_taste()
    console.print(f"resolve_taste source=[cyan]{src}[/cyan]")
    tr = top_transitions(8)
    if tr:
        t = Table(title="Top transitions")
        t.add_column("w", justify="right")
        t.add_column("from")
        t.add_column("to")
        for r in tr:
            t.add_row(
                f"{r['weight']:.0f}",
                f"{r['from_artist']} — {r['from_title']}",
                f"{r['to_artist']} — {r['to_title']}",
            )
        console.print(t)


@app.command()
def worker(
    addr: Optional[str] = typer.Option(
        None, "--addr", help="Адрес HTTP worker (default MUSIK_WORKER_ADDR)"
    ),
    no_poll: bool = typer.Option(False, "--no-poll", help="Без фонового опроса очереди"),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Запустить HTTP worker (очередь jobs + фоновый poll)."""
    _setup_logging(verbose)
    ensure_db()
    serve_worker(addr=addr, poll=not no_poll)


@app.command("watch")
def watch_cmd(
    library: Optional[Path] = typer.Option(
        None, "--library", "-l", help="Папка библиотеки (или MUSIK_LIBRARY)"
    ),
    debounce: Optional[float] = typer.Option(
        None, "--debounce", help="Секунд тишины после изменений перед запуском (default 45)"
    ),
    tags_only: bool = typer.Option(
        False, "--tags-only", help="Scan без LUFS/BPM (быстрее)"
    ),
    no_clusters: bool = typer.Option(False, "--no-clusters", help="Не пересобирать кластеры"),
    no_mixes: bool = typer.Option(False, "--no-mixes", help="Не пересобирать миксы"),
    once: bool = typer.Option(
        False, "--once", help="Один прогон pipeline без слежения (для теста)"
    ),
    verbose: bool = typer.Option(False, "--verbose", "-v"),
) -> None:
    """Следить за папкой музыки и автоматически scan → embed → clusters/mixes → reload."""
    _setup_logging(verbose)
    ensure_db()
    settings = get_settings()
    from musik.watch import run_incremental_pipeline, start_library_watch

    do_clusters = not no_clusters and settings.watch_clusters
    do_mixes = not no_mixes and settings.watch_mixes
    lib = library or settings.library
    if once:
        console.print(f"[bold]One-shot pipeline[/bold] library={lib}")
        out = run_incremental_pipeline(
            tags_only=tags_only, do_clusters=do_clusters, do_mixes=do_mixes
        )
        console.print(out)
        return

    console.print(
        f"[bold]Watching[/bold] {lib}\n"
        f"После паузы ~{debounce or settings.watch_debounce_sec:.0f}s: "
        f"scan → embed(pending) → "
        f"{'clusters → ' if do_clusters else ''}"
        f"{'mixes → ' if do_mixes else ''}"
        f"player reload\n"
        f"[dim]Ctrl+C чтобы остановить[/dim]"
    )
    start_library_watch(
        library=lib,
        debounce_sec=debounce,
        tags_only=tags_only,
        do_clusters=do_clusters,
        do_mixes=do_mixes,
        blocking=True,
    )


@jobs_app.command("enqueue")
def jobs_enqueue(
    kind: str = typer.Argument(..., help="scan|embed|clusters|daily|album_tips|full_rescan"),
    payload_json: Optional[str] = typer.Option(
        None, "--payload", "-p", help="JSON payload для задачи"
    ),
) -> None:
    """Поставить задачу в очередь jobs."""
    ensure_db()
    payload: dict | None = None
    if payload_json:
        import json

        payload = json.loads(payload_json)
    job = enqueue_job(kind, payload)
    console.print(f"[green]Enqueued job #{job['id']}[/green] kind={kind} status={job['status']}")


@jobs_app.command("list")
def jobs_list(limit: int = typer.Option(20, "--limit", "-n")) -> None:
    """Список последних jobs."""
    ensure_db()
    rows = list_recent(limit)
    table = Table(title="Jobs")
    table.add_column("id", justify="right")
    table.add_column("kind")
    table.add_column("status")
    table.add_column("created")
    for r in rows:
        table.add_row(
            str(r["id"]),
            r["kind"],
            r["status"],
            (r.get("created_at") or "")[:19],
        )
    console.print(table)


@discover_app.command("tips")
def discover_tips_cmd(
    new_days: int = typer.Option(14, "--new-days", help="Окно «новый альбом», дней"),
    limit_new: int = typer.Option(10, "--limit-new"),
    limit_old: int = typer.Option(10, "--limit-old"),
) -> None:
    """Пересобрать discover_tips (new_album + resurfaced)."""
    ensure_db()
    result = rebuild_discover_tips(
        new_album_days=new_days,
        limit_new=limit_new,
        limit_old=limit_old,
    )
    console.print(result)


if __name__ == "__main__":
    app()
