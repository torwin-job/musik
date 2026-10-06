from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

import pytest

_MODULE_PATH = Path(__file__).resolve().parents[1] / "tools" / "cue_split.py"
_spec = importlib.util.spec_from_file_location("cue_split", _MODULE_PATH)
assert _spec and _spec.loader
cue_split = importlib.util.module_from_spec(_spec)
sys.modules["cue_split"] = cue_split  # dataclasses resolve annotations via sys.modules
_spec.loader.exec_module(cue_split)


def test_msf_to_seconds():
    assert cue_split.msf_to_seconds("00:00:00") == 0.0
    assert cue_split.msf_to_seconds("00:05:37") == pytest.approx(5 + 37 / 75)
    assert cue_split.msf_to_seconds("03:41:15") == pytest.approx(3 * 60 + 41 + 15 / 75)
    assert cue_split.msf_to_seconds("0:5") is None
    assert cue_split.msf_to_seconds("1:2:3:4") is None
    assert cue_split.msf_to_seconds("bogus") is None


def test_decode_cue_encodings():
    text = "PERFORMER \"Щукин\"\n"
    assert "Щукин" in cue_split.decode_cue(text.encode("utf-8-sig"))
    assert "Щукин" in cue_split.decode_cue(text.encode("cp1251"))
    # 0x98 is the only byte cp1251 cannot decode, forcing the latin-1 fallback
    raw = b'REMOTE \x98\nPERFORMER "Jose"\n'
    assert "PERFORMER" in cue_split.decode_cue(raw)


SINGLE_FILE_CUE = """\
REM GENRE Rock
REM DATE 2005
PERFORMER "Би-2"
TITLE "Би-2"
FILE "Image_CD.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Вчера"
    PERFORMER "Би-2"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Коморбий"
    PERFORMER "Би-2"
    INDEX 00 03:17:45
    INDEX 01 03:21:00
  TRACK 03 AUDIO
    TITLE "Они пришли"
    INDEX 01 07:58:00
"""


def _write(tmp_path: Path, name: str, data: str, encoding: str = "utf-8") -> Path:
    path = tmp_path / name
    path.write_bytes(data.encode(encoding))
    return path


def test_parse_single_file_cue(tmp_path: Path):
    cue = _write(tmp_path, "Image_CD.cue", SINGLE_FILE_CUE, "cp1251")
    sheet = cue_split.parse_cue(cue)
    assert sheet.filenames == ["Image_CD.flac"]
    assert sheet.performer == "Би-2"
    assert sheet.title == "Би-2"
    assert sheet.date == "2005"
    assert sheet.genre == "Rock"
    assert [t.number for t in sheet.tracks] == [1, 2, 3]
    assert sheet.tracks[1].title == "Коморбий"
    assert sheet.tracks[1].performer == "Би-2"
    # INDEX 00 (pregap) wins over INDEX 01 for the cut point.
    assert sheet.tracks[1].start == pytest.approx(3 * 60 + 17 + 45 / 75)
    assert sheet.tracks[2].start == pytest.approx(7 * 60 + 58)


def test_parse_multi_file_cue_keeps_every_file(tmp_path: Path):
    cue = _write(
        tmp_path,
        "album.cue",
        'FILE "01 - One.flac" MP3\n'
        "  TRACK 01 AUDIO\n"
        "    TITLE \"One\"\n"
        "    INDEX 01 00:00:00\n"
        'FILE "02 - Two.flac" MP3\n'
        "  TRACK 02 AUDIO\n"
        "    TITLE \"Two\"\n"
        "    INDEX 01 00:00:00\n",
    )
    sheet = cue_split.parse_cue(cue)
    assert sheet.filenames == ["01 - One.flac", "02 - Two.flac"]


def test_parse_cue_requires_file_and_tracks(tmp_path: Path):
    empty = _write(tmp_path, "empty.cue", 'TITLE "no file"\n')
    with pytest.raises(ValueError, match="no FILE"):
        cue_split.parse_cue(empty)
    no_tracks = _write(tmp_path, "notracks.cue", 'FILE "a.flac" WAVE\n')
    with pytest.raises(ValueError, match="no AUDIO"):
        cue_split.parse_cue(no_tracks)


def test_resolve_image_by_stem(tmp_path: Path):
    (tmp_path / "Album Image.flac").write_bytes(b"x")
    cue = _write(
        tmp_path,
        "album.cue",
        'FILE "Album Image.wav" WAVE\n'
        "  TRACK 01 AUDIO\n"
        "    INDEX 01 00:00:00\n",
    )
    sheet = cue_split.parse_cue(cue)
    assert cue_split.resolve_image(tmp_path, sheet.filename) == tmp_path / "Album Image.flac"
    assert cue_split.resolve_image(tmp_path, "missing.wav") is None


def test_safe_name():
    assert cue_split.safe_name("Live: takes <live>", "01") == "Live_ takes _live_"
    assert cue_split.safe_name("  Вчера  ", "01") == "Вчера"
    assert cue_split.safe_name(None, "Track 04") == "Track 04"
    assert cue_split.safe_name("...", "Track 04") == "Track 04"
    assert len(cue_split.safe_name("x" * 300, "01")) <= 100


def test_build_job_track_boundaries(tmp_path: Path):
    (tmp_path / "Image_CD.flac").write_bytes(b"x")
    cue = _write(tmp_path, "Image_CD.cue", SINGLE_FILE_CUE, "cp1251")
    sheet = cue_split.parse_cue(cue)
    job = cue_split._build_job(cue, [], tmp_path / "Image_CD.flac", sheet, 600.0)
    assert [t[1].name for t in job.tracks] == [
        "01 - Вчера.flac",
        "02 - Коморбий.flac",
        "03 - Они пришли.flac",
    ]
    starts = [t[2] for t in job.tracks]
    lengths = [t[3] for t in job.tracks]
    assert starts == [0.0, pytest.approx(3 * 60 + 17 + 45 / 75), pytest.approx(7 * 60 + 58)]
    # every window ends where the next one starts; the last one ends at the image end
    for i in range(len(job.tracks) - 1):
        assert starts[i] + lengths[i] == pytest.approx(starts[i + 1])
    assert starts[-1] + lengths[-1] == pytest.approx(600.0)


def test_build_job_track_names_are_unique(tmp_path: Path):
    (tmp_path / "x.flac").write_bytes(b"x")
    cue = _write(
        tmp_path,
        "x.cue",
        'FILE "x.flac" WAVE\n'
        "  TRACK 01 AUDIO\n"
        "    TITLE \"Intro\"\n"
        "    INDEX 01 00:00:00\n"
        "  TRACK 02 AUDIO\n"
        "    TITLE \"Intro\"\n"
        "    INDEX 01 00:30:00\n"
        "  TRACK 01 AUDIO\n"
        "    TITLE \"Intro\"\n"
        "    INDEX 01 01:00:00\n",
    )
    sheet = cue_split.parse_cue(cue)
    job = cue_split._build_job(cue, [], tmp_path / "x.flac", sheet, 120.0)
    names = [t[1].name for t in job.tracks]
    # equal numbers+titles must not overwrite each other
    assert names[0] == "01 - Intro.flac"
    assert names[1] == "02 - Intro.flac"
    assert names[2] == "01 - Intro [03].flac"
    assert len(set(names)) == 3


def test_collect_cues_dedupes_and_warns(tmp_path: Path, capsys):
    (tmp_path / "a.cue").write_text("FILE \"a.flac\" WAVE\n", encoding="utf-8")
    nested = tmp_path / "sub"
    nested.mkdir()
    (nested / "b.CUE").write_text("FILE \"b.flac\" WAVE\n", encoding="utf-8")
    missing = tmp_path / "nope"
    found = cue_split.collect_cues([tmp_path, tmp_path / "a.cue", missing])
    assert [p.name for p in found] == ["a.cue", "b.CUE"]
    assert "does not exist" in capsys.readouterr().err


def test_dry_run_skips_multi_file_cue(tmp_path: Path, monkeypatch, capsys):
    """A per-track cue must never become a split candidate (regression)."""
    monkeypatch.setattr(cue_split.shutil, "which", lambda _name: "/usr/bin/ffmpeg")
    _write(
        tmp_path,
        "album.cue",
        'FILE "01 - One.flac" MP3\n'
        "  TRACK 01 AUDIO\n"
        "    INDEX 01 00:00:00\n"
        'FILE "02 - Two.flac" MP3\n'
        "  TRACK 02 AUDIO\n"
        "    INDEX 01 00:00:00\n",
    )
    image = tmp_path / "01 - One.flac"
    image.write_bytes(b"x")
    before = {p.name: p.stat().st_mtime_ns for p in tmp_path.iterdir()}

    assert cue_split.main([str(tmp_path)]) == 0

    out = capsys.readouterr().out
    assert "splittable cues: 0" in out
    assert "multi-file cue (per-track layout)" in out
    assert "dry run" in out
    after = {p.name: p.stat().st_mtime_ns for p in tmp_path.iterdir()}
    assert before == after


def test_dry_run_skips_folder_with_per_track_audio(tmp_path: Path, monkeypatch, capsys):
    monkeypatch.setattr(cue_split.shutil, "which", lambda _name: "/usr/bin/ffmpeg")
    (tmp_path / "album.flac").write_bytes(b"x")
    for number in (1, 2, 3):
        (tmp_path / f"{number:02d} - track.flac").write_bytes(b"x")
    _write(
        tmp_path,
        "album.cue",
        'FILE "album.flac" WAVE\n'
        + "".join(
            f"  TRACK {number:02d} AUDIO\n    INDEX 01 00:{number:02d}:00\n"
            for number in (1, 2, 3)
        ),
    )

    # duration probing would follow; stub it to keep the test offline
    monkeypatch.setattr(cue_split, "probe_duration", lambda _path: 600.0)
    assert cue_split.main([str(tmp_path)]) == 0

    out = capsys.readouterr().out
    assert "folder already has per-track audio" in out


def test_run_job_retries_nudged_seek_and_succeeds(tmp_path: Path, monkeypatch):
    """ffmpeg rejects some exact seek offsets once; a 2 ms nudge must be tried."""
    import subprocess as sp

    calls: list[list[str]] = []
    attempts: dict[str, int] = {}

    def fake_run(cmd, **kwargs):
        calls.append(cmd)
        out = cmd[-1]
        attempts[out] = attempts.get(out, 0) + 1
        if attempts[out] == 1:
            raise sp.CalledProcessError(1, cmd, stderr=b"invalid block size: 15")
        Path(out).write_bytes(b"fLaC")
        return sp.CompletedProcess(cmd, 0)

    monkeypatch.setattr(cue_split.subprocess, "run", fake_run)
    monkeypatch.setattr(cue_split.shutil, "which", lambda _n: "/usr/bin/ffmpeg")
    monkeypatch.setattr(cue_split, "_verify", lambda _out, _length: None)

    cue = _write(tmp_path, "Image_CD.cue", SINGLE_FILE_CUE, "cp1251")
    extra = _write(tmp_path, "Cue_RepG.cue", SINGLE_FILE_CUE, "cp1251")
    image = tmp_path / "Image_CD.flac"
    image.write_bytes(b"image-bytes")
    sheet = cue_split.parse_cue(cue)
    job = cue_split._build_job(cue, [extra], image, sheet, 600.0)

    result = cue_split.run_job(job, overwrite=False, keep_image=False, keep_cue=False)
    assert result.status == "ok", result.reason
    assert len(calls) == 2 * len(job.tracks)
    # every retry starts 2 ms later than the failed attempt
    for first, second in zip(calls[0::2], calls[1::2]):
        ss1 = float(first[first.index("-ss") + 1])
        ss2 = float(second[second.index("-ss") + 1])
        assert ss2 - ss1 == pytest.approx(0.002, abs=1e-9)
    # full precision, no 3-digit truncation of the cue position
    assert calls[0][calls[0].index("-ss") + 1] == "0.000000"
    assert calls[1][calls[1].index("-ss") + 1] == "0.002000"
    assert not cue.exists() and not extra.exists() and not image.exists()


def test_run_job_cleans_partial_output_when_all_attempts_fail(tmp_path: Path, monkeypatch):
    import subprocess as sp

    def fake_run(cmd, **kwargs):
        Path(cmd[-1]).write_bytes(b"")
        raise sp.CalledProcessError(1, cmd, stderr=b"everything is broken")

    monkeypatch.setattr(cue_split.subprocess, "run", fake_run)
    monkeypatch.setattr(cue_split.shutil, "which", lambda _n: "/usr/bin/ffmpeg")
    monkeypatch.setattr(cue_split, "_verify", lambda _out, _length: None)

    cue = _write(tmp_path, "Image_CD.cue", SINGLE_FILE_CUE, "cp1251")
    image = tmp_path / "Image_CD.flac"
    sheet = cue_split.parse_cue(cue)
    job = cue_split._build_job(cue, [], image, sheet, 600.0)

    result = cue_split.run_job(job, overwrite=False, keep_image=True, keep_cue=True)
    assert result.status == "error"
    assert "everything is broken" in result.reason
    leftovers = [p.name for p in tmp_path.glob("*.flac")]
    assert leftovers == []  # the half-written track was removed
    assert cue.exists()  # sources untouched on failure
