# Backups

If the machine running Lectern dies, everything it knew can come back from a
private GitHub repository.

## What is saved

- the database: sessions, tasks, projects, workflows, approvals, settings
- posted media (`LECTERN_MEDIA_DIR`)
- `~/.lectern`: plugins, hooks, Scribe chats, browser profiles (caches left out)
- the Claude and Codex chat transcripts of every workspace a session ran in,
  on this machine and on SSH machines
- the server's `LECTERN_*` settings, secrets included

## How

Each backup is a gzip'd tar, encrypted with age using a passphrase (scrypt),
uploaded as a release of a private repository. Git refuses files over 100 MB
and keeps every version forever; a release asset may be 2 GiB (larger backups
are split) and an old one is deleted outright.

Transcripts run to gigabytes, so a full backup is made weekly and the ones in
between hold only the files changed since it, plus the whole database. A
restore downloads the full backup and the newest changes. Pruning keeps the
newest `keep` backups and any full backup they build on.

Every backup is decrypted and read through before it is uploaded; one that
does not open is reported as failed, not uploaded.

## Set up

On the server, signed in to GitHub (`gh auth login`):

    lectern backup setup you/lectern-backups

or Settings → Backups. Either creates the repository (private; a public one is
refused), and shows a new passphrase once. Keep it in a password manager:
nothing opens the backups without it. The server then backs up every 24 hours
(`--every`), keeping 14 (`--keep`).

`lectern backup` backs up now; `lectern backup list` shows what is there;
`lectern backup export FILE` writes one to a file; Settings → Backups also
offers a download.

## Restore

On the new machine: install Lectern, `gh auth login`, then with the server
stopped:

    lectern backup restore --repo you/lectern-backups

It asks for the passphrase. An existing database is moved to
`lectern.db.before-restore`; files already present are kept unless `--force`.
The old server's settings land in `restored/lectern.env` next to the database
to copy into `/etc/lectern.env`; transcripts from SSH machines land in
`restored/remote/` to unpack on those machines.

Without Lectern: `age -d FILE | tar -xz` opens any backup.

## Settings

| | |
|---|---|
| `LECTERN_BACKUP_REPO` | repository, overriding the saved one |
| `LECTERN_BACKUP_PASSPHRASE` | passphrase, overriding the saved one |
| `LECTERN_BACKUP_EVERY_HOURS` | schedule; 0 = only when asked |
| `LECTERN_BACKUP_GITHUB_TOKEN` | GitHub token; else `GH_TOKEN`, `GITHUB_TOKEN`, then `gh auth token` |

Only a person may set up, run or download backups through the API; an agent
on the machine is refused.
