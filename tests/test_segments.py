from musik.embed.segments import plan_windows


def test_short_track_one_window():
    wins = plan_windows(20.0, segment_sec=30.0)
    assert len(wins) == 1
    assert wins[0].name == "full"


def test_long_track_three_windows():
    wins = plan_windows(240.0, segment_sec=30.0)
    assert [w.name for w in wins] == ["start", "middle", "end"]
    assert wins[0].offset_sec == 0.0
    assert abs(wins[1].offset_sec - (120.0 - 15.0)) < 1e-6
    assert abs(wins[2].offset_sec - 210.0) < 1e-6


def test_medium_track_dedupes_offsets():
    # 45s → start@0, middle@7.5, end@15 — all distinct
    wins = plan_windows(45.0, segment_sec=30.0)
    assert len(wins) == 3
    # 35s → start@0, middle@2.5, end@5 — all kept (offsets differ >0.5)
    wins35 = plan_windows(35.0, segment_sec=30.0)
    assert len(wins35) >= 2
