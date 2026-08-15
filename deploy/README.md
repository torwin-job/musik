# Deploy musik → LXC (temporary / VPS)

Local secrets: `deploy/secrets.env` (**gitignored**). Template: `secrets.env.example`.

Target host, user, paths and domain are configured only in the gitignored
`deploy/secrets.env`.

## One-time

1. Edit `deploy/secrets.env`: domain, passwords/tokens (`MUSIK_DOMAIN` when DNS ready).
2. `./deploy/ssh-key-setup.sh` — key auth (done if `deploy/.ssh/` exists).
3. `./deploy/remote-bootstrap.sh` — dirs `/opt/musik`, `/music`.

## Each deploy

```bash
./deploy/sync.sh                         # code + generated .env
./deploy/push-music.sh /path/to/music    # merge into /music
./deploy/remote-up.sh                    # compose up + rescan jobs
./deploy/install-proxy.sh                # after DNS → box
```

LAN without domain: `http://<server-lan-ip>:8787`

## Helpers

| Script | What |
|--------|------|
| `ssh.sh '…'` | remote command |
| `ssh-key-setup.sh` | ed25519 → LXC |
| `remote-bootstrap.sh` | docker/dirs |
| `sync.sh` | rsync repo + `.env` |
| `push-music.sh` | rsync library → `/music` |
| `remote-up.sh` | compose + rescan/mixes |
| `install-proxy.sh` | Caddy → `:8787` |

## Notes

- `/music` on the LXC already has some albums; push only merges.
- LXC has ~4 GiB RAM — CLAP embed on CPU will be slow/tight; bump RAM if OOM.
- Do not commit `secrets.env` / `deploy/.ssh/`.
- Rotate root/app passwords if they were pasted in chat.
