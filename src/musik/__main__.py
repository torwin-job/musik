"""Allow `python -m musik …` (used by player worker autostart)."""

from musik.cli import app

if __name__ == "__main__":
    app()
