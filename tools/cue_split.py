"""Split single-file album images (.cue + .flac/.wav/.ape/...) into per-track FLACs.

The tool is a dry run by default: it only prints what it would do. Pass
``--apply`` to actually encode, and even then a cue and its image are deleted
only after every produced track has been verified (exists, non-empty, FLAC,
duration matches the cue timeline within tolerance).

Safety rules:
- only single-FILE cues with two or more AUDIO tracks are considered; per-track
  cues and cues for folders that already hold per-track audio are reported and
  skipped;
- cues are grouped by the image they reference, so two cues for one image
  (``Cue.cue`` / ``Cue_RepG.cue``) split it exactly once;
- an existing file at a target path aborts the cue unless ``--overwrite`` is
  given;
- if any track fails, everything this run created for that cue is removed and
  the image and cue stay untouched.

Usage:
    python tools/cue_split.py [PATH ...] [--apply] [--jobs N] [--limit N]
"""

from __future__ import annotations

import argparse
import concurrent.futures
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

AUDIO_EXTS = {
    ".flac", ".wav", ".ape", ".wv", ".mp3", ".m4a", ".aac",
    ".ogg", ".opus", ".aiff", ".aif", ".wma", ".mp4",
}
FFMPEG_TIMEOUT = 1800
PROBE_TIMEOUT = 300
# A cut track must match its cue window within half a second or one percent.
DURATION_TOLERANCE = 0.5


@dataclass
class CueTrack:
    number: int
    title: str | None = None
    performer: str | None = None
    indexes: dict[int, float] = field(default_factory=dict)

    @property
    def start(self) -> float | None:
        """Start of the track's audio, preferring INDEX 00 (pregap start)."""
        if 0 in self.indexes:
            return self.indexes[0]
        return self.indexes.get(1)


@dataclass
class CueSheet:
    performer: str | None = None
    title: str | None = None
    date: str | None = None
    genre: str | None = None
    filenames: list[str] = field(default_factory=list)
    tracks: list[CueTrack] = field(default_factory=list)

    @property
    def filename(self) -> str | None:
        return self.filenames[0] if self.filenames else None


_MSF_RE = re.compile(r"^(\d+):(\d{1,2}):(\d{1,2})$")
_UNSAFE_RE = re.compile(r'[\\/:*?"<>|]')


def decode_cue(data: bytes) -> str:
    """UTF-8 (with BOM), then CP1251 (EAC rips with Cyrillic), then latin-1."""
    for encoding in ("utf-8-sig", "cp1251", "latin-1"):
        try:
            return data.decode(encoding)
        except UnicodeDecodeError:
            continue
    return data.decode("latin-1", errors="replace")


def msf_to_seconds(value: str) -> float | None:
    match = _MSF_RE.match(value.strip())
    if not match:
        return None
    minutes, seconds, frames = (int(g) for g in match.groups())
    return minutes * 60 + seconds + frames / 75.0


def _unquote(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def parse_cue(path: Path) -> CueSheet:
    """Parse a cue sheet. Raises ValueError when there is nothing to split."""
    sheet = CueSheet()
    track: CueTrack | None = None
    seen = False

    for raw_line in decode_cue(path.read_bytes()).splitlines():
        line = raw_line.strip()
        if not line:
            continue
        parts = line.split(None, 1)
        command = parts[0].upper()
        argument = parts[1] if len(parts) > 1 else ""
        seen = True

        if command == "REM":
            sub = argument.split(None, 1)
            if len(sub) == 2 and track is None:
                keyword = sub[0].upper()
                if keyword == "DATE" and not sheet.date:
                    sheet.date = _unquote(sub[1])
                elif keyword == "GENRE" and not sheet.genre:
                    sheet.genre = _unquote(sub[1])
        elif command == "PERFORMER":
            if track is not None:
                track.performer = _unquote(argument)
            elif sheet.performer is None:
                sheet.performer = _unquote(argument)
        elif command == "TITLE":
            if track is not None and track.title is None:
                track.title = _unquote(argument)
            elif sheet.title is None:
                sheet.title = _unquote(argument)
        elif command == "FILE":
            match = re.match(r'^"(.*)"\s+(\S+)\s*$', argument)
            if match:
                sheet.filenames.append(match.group(1))
            elif argument.split():
                sheet.filenames.append(argument.split()[0].strip('"'))
            # INDEX lines that follow a FILE but no TRACK belong to the
            # previous track (some rippers write that layout), so the open
            # track is deliberately kept here.
        elif command == "TRACK":
            tokens = argument.split()
            if len(tokens) < 2 or tokens[1].upper() != "AUDIO":
                track = None
                continue
            try:
                number = int(tokens[0])
            except ValueError:
                track = None
                continue
            track = CueTrack(number=number)
            sheet.tracks.append(track)
        elif command == "INDEX" and track is not None:
            tokens = argument.split()
            if len(tokens) < 2:
                continue
            try:
                index = int(tokens[0])
            except ValueError:
                continue
            seconds = msf_to_seconds(tokens[1])
            if seconds is not None:
                track.indexes[index] = seconds

    if not seen or not sheet.filenames:
        raise ValueError("no FILE entry")
    if not sheet.tracks:
        raise ValueError("no AUDIO tracks")
    return sheet


def resolve_image(cue_dir: Path, filename: str) -> Path | None:
    """Resolve the cue's FILE name against the cue's own directory."""
    base = Path(filename.replace("\\", "/").lstrip("./")).name
    if not base:
        return None
    direct = cue_dir / base
    if direct.is_file():
        return direct
    stem = Path(base).stem.lower()
    try:
        siblings = sorted(p for p in cue_dir.iterdir() if p.is_file())
    except OSError:
        return None
    for candidate in siblings:
        if candidate.suffix.lower() in AUDIO_EXTS and candidate.stem.lower() == stem:
            return candidate
    return None


def probe_duration(path: Path) -> float | None:
    try:
        out = subprocess.run(
            [
                "ffprobe", "-v", "error",
                "-show_entries", "format=duration",
                "-of", "csv=p=0", str(path),
            ],
            capture_output=True, text=True, timeout=PROBE_TIMEOUT, check=True,
        )
        return float(out.stdout.strip())
    except Exception:  # noqa: BLE001
        return None


def probe_format(path: Path) -> str:
    try:
        out = subprocess.run(
            [
                "ffprobe", "-v", "error",
                "-show_entries", "format=format_name",
                "-of", "csv=p=0", str(path),
            ],
            capture_output=True, text=True, timeout=PROBE_TIMEOUT, check=True,
        )
        return out.stdout.strip().lower()
    except Exception:  # noqa: BLE001
        return ""


def read_image_tags(path: Path) -> dict[str, str]:
    """Best-effort tag fallback from the image itself (mutagen is optional)."""
    try:
        from mutagen import File as MutagenFile  # type: ignore
    except Exception:  # noqa: BLE001
        return {}
    try:
        handle = MutagenFile(str(path), easy=True)
        tags = getattr(handle, "tags", None) or {}
    except Exception:  # noqa: BLE001
        return {}
    out: dict[str, str] = {}
    for key in ("artist", "album", "date", "genre"):
        value = tags.get(key)
        if isinstance(value, (list, tuple)) and value:
            out[key] = str(value[0]).strip()
        elif isinstance(value, str):
            out[key] = value.strip()
    return out


def safe_name(text: str | None, fallback: str) -> str:
    text = (text or "").strip()
    text = _UNSAFE_RE.sub("_", text)
    text = re.sub(r"\s+", " ", text).strip(" .")
    return text[:100] or fallback


def _first_year(value: str | None) -> str | None:
    if not value:
        return None
    match = re.search(r"(19|20)\d{2}", value)
    return match.group(0) if match else None


@dataclass
class Job:
    cue: Path
    extra_cues: list[Path]
    image: Path
    duration: float
    tracks: list[tuple[CueTrack, Path, float, float]]  # track, out, start, length
    sheet: CueSheet
    image_tags: dict[str, str]


@dataclass
class PlanResult:
    status: str  # ok | skip | error
    reason: str
    cue: Path
    detail: str = ""


def _build_job(
    cue: Path, extra_cues: list[Path], image: Path, sheet: CueSheet, duration: float
) -> Job:
    starts: list[float] = []
    for i, track in enumerate(sheet.tracks):
        start = 0.0 if i == 0 else (track.start or 0.0)
        starts.append(start)
    tracks: list[tuple[CueTrack, Path, float, float]] = []
    used: set[str] = set()
    for i, track in enumerate(sheet.tracks):
        end = starts[i + 1] if i + 1 < len(starts) else duration
        title = safe_name(track.title, f"Track {track.number:02d}")
        name = f"{track.number:02d} - {title}.flac"
        if name.lower() in used:
            name = f"{track.number:02d} - {title} [{i + 1:02d}].flac"
        used.add(name.lower())
        tracks.append((track, cue.parent / name, starts[i], end - starts[i]))
    return Job(
        cue=cue, extra_cues=extra_cues, image=image, duration=duration,
        tracks=tracks, sheet=sheet, image_tags=read_image_tags(image),
    )


def run_job(job: Job, *, overwrite: bool, keep_image: bool, keep_cue: bool) -> PlanResult:
    existing = [str(out.name) for _, out, _, _ in job.tracks if out.exists()]
    if existing and not overwrite:
        return PlanResult(
            "skip", f"output exists ({', '.join(existing[:3])}); use --overwrite", job.cue
        )

    if shutil.which("ffmpeg") is None or shutil.which("ffprobe") is None:
        return PlanResult("error", "ffmpeg/ffprobe not found on PATH", job.cue)

    fallback = read_image_tags(job.image)
    album = job.sheet.title or fallback.get("album") or ""
    year = _first_year(job.sheet.date) or _first_year(fallback.get("date"))
    genre = job.sheet.genre or fallback.get("genre") or ""

    created: list[Path] = []
    total = len(job.tracks)
    for track, out, start, length in job.tracks:
        artist = (
            track.performer or job.sheet.performer
            or fallback.get("artist") or "Unknown Artist"
        )

        def _cmd(at: float) -> list[str]:
            cmd = [
                "ffmpeg", "-v", "error", "-y", "-nostdin",
                "-accurate_seek", "-ss", f"{at:.6f}",
                "-i", str(job.image),
                "-t", f"{length:.3f}",
                "-map", "0:a:0", "-c:a", "flac", "-compression_level", "8",
                "-map_metadata", "-1",
                "-metadata", f"TITLE={track.title or f'Track {track.number:02d}'}",
                "-metadata", f"ARTIST={artist}",
                "-metadata", f"TRACKNUMBER={track.number}",
                "-metadata", f"TRACKTOTAL={total}",
            ]
            if album:
                cmd += ["-metadata", f"ALBUM={album}"]
            if year:
                cmd += ["-metadata", f"DATE={year}"]
            if genre:
                cmd += ["-metadata", f"GENRE={genre}"]
            cmd.append(str(out))
            return cmd

        # Some seek offsets land on a frame boundary this ffmpeg build rejects
        # with "invalid block size"; a 2 ms nudge re-frames it cleanly.
        failure: str | None = None
        for attempt, offset in enumerate((0.0, 0.002)):
            try:
                subprocess.run(
                    _cmd(start + offset), timeout=FFMPEG_TIMEOUT,
                    check=True, capture_output=True,
                )
                failure = None
                break
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
                failure = _ffmpeg_error(exc)
                if attempt == 0:
                    continue
        if failure is not None:
            created.append(out)
            _cleanup(created)
            return PlanResult("error", f"ffmpeg failed: {failure[:200]}", job.cue)

        problem = _verify(out, length)
        if problem:
            created.append(out)
            _cleanup(created)
            return PlanResult("error", f"{out.name}: {problem}", job.cue)
        created.append(out)

    for path in [job.cue, *job.extra_cues]:
        if not keep_cue:
            try:
                path.unlink()
            except OSError as exc:
                return PlanResult("error", f"cannot delete {path.name}: {exc}", job.cue)
    if not keep_image:
        try:
            job.image.unlink()
        except OSError as exc:
            return PlanResult("error", f"cannot delete {job.image.name}: {exc}", job.cue)

    return PlanResult("ok", f"{total} tracks", job.cue, detail=job.image.name)


def _verify(out: Path, expected: float) -> str | None:
    if not out.is_file():
        return "not written"
    if out.stat().st_size == 0:
        return "empty file"
    if "flac" not in probe_format(out):
        return "not a flac"
    actual = probe_duration(out)
    if actual is None:
        return "ffprobe failed"
    tolerance = max(DURATION_TOLERANCE, expected * 0.01)
    if abs(actual - expected) > tolerance:
        return f"duration {actual:.2f}s != expected {expected:.2f}s"
    return None


def _ffmpeg_error(exc: Exception) -> str:
    if isinstance(exc, subprocess.TimeoutExpired):
        return f"timeout after {exc.timeout}s"
    if isinstance(exc, subprocess.CalledProcessError) and exc.stderr:
        text = exc.stderr.decode("utf-8", errors="replace")
        lines = [line for line in text.splitlines() if line.strip()]
        if lines:
            return "; ".join(lines[:2])
    return str(exc).splitlines()[0] if str(exc) else "ffmpeg failed"


def _cleanup(paths: list[Path]) -> None:
    for path in paths:
        try:
            path.unlink()
        except OSError:
            pass


def collect_cues(paths: list[Path]) -> list[Path]:
    cues: list[Path] = []
    for raw in paths:
        path = Path(raw).expanduser()
        if path.is_file():
            if path.suffix.lower() == ".cue":
                cues.append(path)
        elif path.is_dir():
            for child in sorted(path.rglob("*")):
                if child.is_file() and child.suffix.lower() == ".cue":
                    cues.append(child)
        else:
            print(f"warning: {path} does not exist", file=sys.stderr)
    seen: set[Path] = set()
    unique: list[Path] = []
    for cue in cues:
        key = cue.resolve()
        if key not in seen:
            seen.add(key)
            unique.append(cue)
    return unique


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Split .cue album images into per-track FLACs (dry run by default)."
    )
    parser.add_argument("paths", nargs="*",
                        help="cue files or directories (default: MUSIK_LIBRARY)")
    parser.add_argument("--apply", action="store_true",
                        help="actually encode and remove cue+image on success")
    parser.add_argument("--overwrite", action="store_true",
                        help="replace existing target files instead of skipping")
    parser.add_argument("--keep-image", action="store_true", help="do not delete the image")
    parser.add_argument("--keep-cue", action="store_true", help="do not delete the cue")
    parser.add_argument("--jobs", type=int, default=4,
                        help="parallel ffmpeg processes for --apply (default: 4)")
    parser.add_argument("--limit", type=int, default=0,
                        help="process at most N splittable cues")
    args = parser.parse_args(argv)

    if not args.paths:
        library_root = os.environ.get("MUSIK_LIBRARY")
        if not library_root:
            print(
                "error: no paths given and MUSIK_LIBRARY is not set — "
                "pass a cue file/folder or set MUSIK_LIBRARY",
                file=sys.stderr,
            )
            return 2
        args.paths = [library_root]

    if shutil.which("ffprobe") is None:
        print("error: ffprobe not found on PATH", file=sys.stderr)
        return 2

    cues = collect_cues([Path(p) for p in args.paths])
    if not cues:
        print("no .cue files found", file=sys.stderr)
        return 2

    print(f"scanning {len(cues)} cue file(s)...")
    seen_images: dict[Path, Path] = {}
    jobs: list[Job] = []
    skipped: dict[str, list[Path]] = {}
    errors: list[tuple[Path, str]] = []

    for cue in cues:
        try:
            sheet = parse_cue(cue)
        except (OSError, ValueError) as exc:
            skipped.setdefault(f"parse error: {exc}", []).append(cue)
            continue
        if len(sheet.tracks) < 2:
            skipped.setdefault("single-track cue", []).append(cue)
            continue
        if len(sheet.filenames) != 1:
            skipped.setdefault("multi-file cue (per-track layout)", []).append(cue)
            continue
        image = resolve_image(cue.parent, sheet.filename or "")
        if image is None:
            skipped.setdefault("image not found", []).append(cue)
            continue
        if image in seen_images:
            seen_images[image].append(cue)
            skipped.setdefault(f"duplicate cue for {image.name}", []).append(cue)
            continue
        seen_images[image] = [cue]

        try:
            audio = [
                p for p in cue.parent.iterdir()
                if p.is_file() and p.suffix.lower() in AUDIO_EXTS
            ]
        except OSError as exc:
            errors.append((cue, str(exc)))
            continue
        others = [p for p in audio if p.resolve() != image.resolve()]
        if len(others) >= len(sheet.tracks):
            skipped.setdefault("folder already has per-track audio", []).append(cue)
            continue

        duration = probe_duration(image)
        if duration is None:
            errors.append((cue, "ffprobe failed on image"))
            continue

        starts: list[float] = []
        bad = None
        for i, track in enumerate(sheet.tracks):
            start = track.start
            if start is None:
                bad = f"track {track.number:02d} has no INDEX"
                break
            if i == 0:
                start = 0.0
            if starts and start < starts[-1] - 1e-6:
                bad = "cue times are not increasing"
                break
            if start >= duration:
                bad = "cue runs past the image end"
                break
            starts.append(start)
        if bad:
            skipped.setdefault(bad, []).append(cue)
            continue
        if duration - starts[-1] < 0.5:
            skipped.setdefault("last track shorter than 0.5s", []).append(cue)
            continue

        jobs.append(_build_job(cue, [], image, sheet, duration))
        if args.limit and len(jobs) >= args.limit:
            break

    # attach duplicate cues so success removes every leftover sheet
    for job in jobs:
        job.extra_cues = [c for c in seen_images.get(job.image, []) if c != job.cue]

    tracks_total = sum(len(j.tracks) for j in jobs)
    print(f"splittable cues: {len(jobs)}  (tracks: {tracks_total})")
    for job in jobs:
        size_mb = job.image.stat().st_size / 1e6
        print(f"  [split] {job.cue}")
        print(f"          image {job.image.name} ({size_mb:.0f} MB) -> {len(job.tracks)} tracks")
    if skipped:
        print("\nskipped:")
        for reason, items in sorted(skipped.items(), key=lambda kv: -len(kv[1])):
            print(f"  {len(items):4d}  {reason}")
            if args.limit == 0 or len(items) <= 5:
                for cue in items[:5]:
                    print(f"          {cue}")
    if errors:
        print("\nerrors:")
        for cue, message in errors:
            print(f"  {cue}: {message}")

    if not args.apply:
        print("\ndry run — nothing was changed. Re-run with --apply to split.")
        return 1 if errors else 0
    if not jobs:
        return 1 if errors else 0

    print(f"\napplying with {max(1, args.jobs)} job(s)...")
    results: list[PlanResult] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.jobs)) as pool:
        futures = [
            pool.submit(
                run_job, job,
                overwrite=args.overwrite,
                keep_image=args.keep_image,
                keep_cue=args.keep_cue,
            )
            for job in jobs
        ]
        for future in concurrent.futures.as_completed(futures):
            result = future.result()
            results.append(result)
            mark = "ok  " if result.status == "ok" else ("skip" if result.status == "skip" else "FAIL")
            line = f"  [{mark}] {result.cue} — {result.reason}"
            if result.detail and result.status == "ok":
                line += f" ({result.detail})"
            print(line)

    ok = sum(1 for r in results if r.status == "ok")
    failed = [r for r in results if r.status != "ok"]
    print(f"\ndone: {ok} album(s) split, {len(failed)} failed, {tracks_total} tracks planned")
    for result in failed:
        print(f"  FAIL {result.cue}: {result.reason}")
    return 1 if failed or errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
