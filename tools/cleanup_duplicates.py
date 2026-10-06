"""Report duplicate music files and delete the losing copies with --apply.

The database decides who wins: ``musik.db.store.mark_duplicates()`` groups
tracks by MD5, then fingerprint, then artist+title+album+year, and keeps the
finest copy (FLAC > WAV > ... > MP3, then bitrate, then size). This tool only
prints the losing files — and actually removes them — when --apply is given.

Safety rules:
- the keeper (winner) must still exist on disk, otherwise nothing is removed;
- a path that is both keeper and duplicate is never deleted;
- only files under the library root are considered;
- deletion is per file: on any failure the run reports it and keeps going.

Usage:
    python tools/cleanup_duplicates.py [--apply] [--limit N] [--verbose]
"""

from __future__ import annotations

import argparse
import os
import sys
from dataclasses import dataclass
from pathlib import Path

from musik.config import get_settings
from musik.db.schema import connect, utcnow
from musik.db.store import mark_duplicates


@dataclass
class Item:
    track_id: int
    dup_path: Path
    size: int
    label: str
    keeper_path: Path
    exists: bool


def _norm(path: Path) -> str:
    return os.path.normcase(str(path.resolve())) if path.exists() else os.path.normcase(str(path))


def collect(conn, library: Path) -> tuple[list[Item], list[str]]:
    rows = conn.execute(
        """
        SELECT d.id, d.path AS dup_path, COALESCE(d.file_size, 0) AS file_size,
               d.artist, d.title, d.album, w.path AS keeper_path
        FROM tracks AS d
        JOIN tracks AS w ON w.id = d.is_duplicate_of
        WHERE d.is_duplicate_of IS NOT NULL AND d.is_active = 1
        ORDER BY keeper_path, dup_path
        """
    ).fetchall()
    items: list[Item] = []
    notes: list[str] = []
    for row in rows:
        dup = Path(row["dup_path"])
        keeper = Path(row["keeper_path"])
        if _norm(dup) == _norm(keeper):
            notes.append(f"skip (same file as keeper): {dup}")
            continue
        if not _is_under(dup, library) or not _is_under(keeper, library):
            notes.append(f"skip (outside library): {dup}")
            continue
        if not keeper.is_file():
            notes.append(f"skip (keeper missing): {dup} -> {keeper}")
            continue
        label = " - ".join(
            part for part in (row["artist"], row["title"], row["album"]) if part
        )
        items.append(
            Item(
                track_id=int(row["id"]),
                dup_path=dup,
                size=int(row["file_size"]),
                label=label,
                keeper_path=keeper,
                exists=dup.is_file(),
            )
        )
    return items, notes


def _is_under(path: Path, root: Path) -> bool:
    try:
        path.resolve().relative_to(root.resolve())
    except ValueError:
        return False
    return True


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Delete duplicate music files kept out of the catalog (dry run by default)."
    )
    parser.add_argument("--apply", action="store_true",
                        help="actually delete the losing files")
    parser.add_argument("--db", type=Path, default=None,
                        help="override the SQLite path (also reads MUSIK_DB_PATH)")
    parser.add_argument("--limit", type=int, default=0,
                        help="touch at most N duplicates")
    parser.add_argument("--verbose", "-v", action="store_true",
                        help="print every group even in dry-run summary mode")
    args = parser.parse_args(argv)

    if args.db is not None:
        os.environ["MUSIK_DB_PATH"] = str(args.db)
        get_settings.cache_clear()
    settings = get_settings()
    if not settings.db_path.exists():
        print(f"error: no database at {settings.db_path} — run `musik scan` first",
              file=sys.stderr)
        return 2

    print("recomputing duplicate flags...")
    marked = mark_duplicates()

    with connect() as conn:
        library = Path(settings.library)
        items, notes = collect(conn, library)

    if args.limit:
        items = items[: args.limit]

    total_bytes = sum(i.size for i in items if i.exists)
    missing = sum(1 for i in items if not i.exists)
    print(f"duplicate files: {len(items)} ({total_bytes / 1e6:.1f} MB to free"
          f"{f', {missing} already missing' if missing else ''})")
    if marked:
        print(f"flags recomputed: {marked} track(s) newly marked")
    for note in notes:
        print(f"  {note}")

    shown = items if args.verbose else items[:40]
    keeper: str | None = None
    for item in shown:
        if str(item.keeper_path) != keeper:
            keeper = str(item.keeper_path)
            print(f"  keep: {keeper}")
        state = "" if item.exists else "  [missing]"
        print(f"    del: {item.dup_path} ({item.size / 1e6:.1f} MB){state}"
              f"{'  ' + item.label if item.label else ''}")
    if len(items) > len(shown):
        print(f"  ... and {len(items) - len(shown)} more (use --verbose to list)")

    if not args.apply:
        print("\ndry run — nothing was deleted. Re-run with --apply to remove.")
        return 0
    if not items:
        return 0

    print("\nremoving duplicates...")
    deleted = 0
    freed = 0
    failed = 0
    now = utcnow()
    with connect() as conn:
        for item in items:
            if item.exists:
                try:
                    item.dup_path.unlink()
                except OSError as exc:
                    print(f"  FAIL {item.dup_path}: {exc}")
                    failed += 1
                    continue
                deleted += 1
                freed += item.size
            conn.execute(
                """
                UPDATE tracks SET is_active = 0, is_duplicate_of = NULL, updated_at = ?
                WHERE path = ? AND is_active = 1
                """,
                (now, str(item.dup_path)),
            )
    print(f"\ndone: {deleted} file(s) removed, {freed / 1e6:.1f} MB freed, "
          f"{failed} failed, {len(items) - deleted - failed} flag(s) only")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
