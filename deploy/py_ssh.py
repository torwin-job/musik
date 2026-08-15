#!/usr/bin/env python3
"""Password/key SSH helper for deploy scripts (paramiko in deploy/.vendor)."""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

VENDOR = Path(__file__).resolve().parent / ".vendor"
sys.path.insert(0, str(VENDOR))

import paramiko  # noqa: E402


def connect(
    host: str,
    user: str,
    password: str | None,
    key_path: Path | None,
) -> paramiko.SSHClient:
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    kwargs: dict = {
        "hostname": host,
        "username": user,
        "timeout": 20,
        "allow_agent": False,
        "look_for_keys": False,
    }
    if key_path is not None and key_path.is_file():
        kwargs["key_filename"] = str(key_path)
    elif password:
        kwargs["password"] = password
    else:
        raise SystemExit("Need key or password")
    client.connect(**kwargs)
    return client


def ensure_key(key_dir: Path) -> tuple[Path, Path]:
    key_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    priv = key_dir / "id_ed25519"
    pub = key_dir / "id_ed25519.pub"
    if not priv.exists():
        subprocess.run(
            [
                "ssh-keygen",
                "-t",
                "ed25519",
                "-N",
                "",
                "-f",
                str(priv),
                "-C",
                "musik-deploy",
            ],
            check=True,
        )
        os.chmod(priv, 0o600)
    return priv, pub


def main() -> int:
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)

    c = sub.add_parser("run", help="Run remote command")
    c.add_argument("command")

    i = sub.add_parser("install-key", help="Generate key if needed and install pubkey")
    i.add_argument(
        "--key-dir",
        default=str(Path(__file__).resolve().parent / ".ssh"),
    )

    g = sub.add_parser("put", help="Upload local file")
    g.add_argument("local")
    g.add_argument("remote")

    args = p.parse_args()
    host = os.environ["MUSIK_SSH_HOST"]
    user = os.environ["MUSIK_SSH_USER"]
    password = os.environ.get("MUSIK_SSH_PASS") or None
    key = Path(__file__).resolve().parent / ".ssh" / "id_ed25519"

    if args.cmd == "install-key":
        _priv, pub = ensure_key(Path(args.key_dir))
        pubkey = pub.read_text().strip()
        client = connect(host, user, password, None)
        _stdin, stdout, _stderr = client.exec_command("printf %s \"$HOME\"")
        home = stdout.read().decode().strip() or ("/root" if user == "root" else f"/home/{user}")
        auth = f"{home}/.ssh/authorized_keys"
        client.exec_command(f"mkdir -p {home}/.ssh && chmod 700 {home}/.ssh")
        # append if missing
        cmd = (
            f"touch {auth} && chmod 600 {auth} && "
            f"grep -qxF '{pubkey}' {auth} || echo '{pubkey}' >> {auth}"
        )
        _stdin, stdout, stderr = client.exec_command(cmd)
        code = stdout.channel.recv_exit_status()
        if code != 0:
            sys.stderr.buffer.write(stderr.read())
            client.close()
            return code
        client.close()
        print(f"OK key installed: {pub}")
        return 0

    use_key = key if key.is_file() else None
    client = connect(host, user, None if use_key else password, use_key)

    if args.cmd == "run":
        _stdin, stdout, stderr = client.exec_command(args.command)
        out = stdout.read()
        err = stderr.read()
        code = stdout.channel.recv_exit_status()
        sys.stdout.buffer.write(out)
        sys.stderr.buffer.write(err)
        client.close()
        return code

    if args.cmd == "put":
        sftp = client.open_sftp()
        sftp.put(args.local, args.remote)
        sftp.close()
        client.close()
        print(f"OK {args.local} -> {args.remote}")
        return 0

    return 1


if __name__ == "__main__":
    raise SystemExit(main())
