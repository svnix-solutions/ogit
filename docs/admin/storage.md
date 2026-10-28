# Storage

Everything that isn't in the database goes to the repository storage: git data, wikis, Git LFS objects, release assets, packages, and Actions artifacts and caches.

## Local filesystem

```yaml
storage:
  type: local
  local:
    path: /data/repositories
```

Each repository is a bare git repository, `<path>/<storage key>.git`; its wiki is `<storage key>.wiki.git`. The storage key is the repository's id (or, for repositories older than renames, its original name). It never changes, so renames and transfers move nothing. LFS objects, release assets and Actions files live inside the repository's directory and go with it. Package files are kept per owner under `packages/`.

A local directory serves one server. Several servers need S3 (or a shared filesystem with POSIX rename semantics, which isn't tested).

## S3-compatible object storage

```yaml
storage:
  type: s3
  s3:
    bucket: ogit
    endpoint: https://s3.eu-west-2.amazonaws.com   # or http://minio:9000, Outscale, ...
    accesskey: …
    secretkey: …
    region: eu-west-2
```

Requests use path-style addressing, so MinIO and other S3-compatible stores work. Repositories are stored as git packs (`repositories/<key>.git/objects/pack/pack-<sha>.pack` and `.idx`), one pack per push, read with ranged requests.

Several servers can share one bucket. Ref updates are conditional writes (`If-Match` / `If-None-Match`), so concurrent pushes to the same branch can't overwrite each other, and each request lists the packs again, so it sees the latest push.

::: warning MinIO and deletions
MinIO ignores `If-Match` on DELETE, so two clients deleting the same branch at the same moment can race there. Pushes that move branches are not affected.
:::

### Cache

Packs never change once written, so their indexes and 4 MiB blocks are cached:

```yaml
storage:
  s3:
    cache:
      type: memory     # memory (default), redis or none
      max_mb: 256      # memory: LRU size per server
      redis_url: ""    # redis: redis://cache:6379/0, shared by every server
```

With `redis`, if Redis is down requests go to S3, and Redis is tried again after 30 seconds, so an outage costs nothing but speed. Refs and HEAD are never cached.

With the cache on a 300-commit repository, a push takes 12 S3 requests instead of 1810, a clone 12 instead of 1513, and a one-commit fetch 14 instead of 1256.

## Maintenance

### gc: merge packs

Each push adds a pack, and every request lists and loads every pack, so a repository slows down with each push (a clone after 40 pushes takes 92 requests instead of 12). Merge packs regularly, e.g. nightly:

```bash
./ogit storage gc -c config.yaml --all             # or --repository <owner>/<name>
```

- It merges the small packs and leaves the big old ones alone, so each run costs about the size of the recent pushes.
- Objects are moved, never pruned, so it can't lose data. It is safe while the server runs and pushes arrive.
- Replaced packs are hidden at once, but kept for `--grace` (default 1h) for requests already reading them, and deleted by the next run after that.
- It also packs loose objects (see repack) and includes wikis.

### prune: drop unreachable objects

`gc` never deletes objects. Commits dropped by force-pushes or deleted branches stay until you prune them, e.g. weekly:

```bash
./ogit storage prune -c config.yaml --all --dry-run   # report only
./ogit storage prune -c config.yaml --all             # --expire 336h (2 weeks) by default
```

- An object is pruned only when no ref reaches it (branches, tags, notes, any `refs/` name, HEAD) and it is older than `--expire`. So force-pushed work can be recovered by its hash for two weeks.
- Objects that recent packs need, such as a push still in progress, are kept. Objects `gc` just merged sit in a new pack, so their two weeks start again.
- Prune rewrites a repository's old packs, so it costs a full read and write of them. Replaced packs follow the same `--grace` rule as `gc`.
- If an object is missing from the repository, prune stops without deleting anything.

### repack: convert old repositories

Repositories created before packs (one S3 object per git object) still work, but stay slow until converted:

```bash
./ogit storage repack -c config.yaml --repository <owner>/<name>   # or --all
```

It writes one pack, checks that every object is in it, then deletes the loose objects. The server can keep running, but a clone or fetch of that repository in progress when the loose objects go can fail and must be retried, so run it when the repository is quiet.

### Example cron

```txt
15 3 * * *  ogit storage gc    -c /etc/ogit/config.yaml --all
45 3 * * 0  ogit storage prune -c /etc/ogit/config.yaml --all
```

On local storage these commands do nothing; use `git gc` on the repositories if you need to.
