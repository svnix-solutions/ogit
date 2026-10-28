# Git LFS

Git LFS keeps large files (images, videos, datasets, binaries) out of git's history: git stores small pointers, and the files go to the server separately. It works with your usual remote, over HTTPS or SSH.

```bash
git lfs install                 # once per machine
git lfs track "*.psd"           # writes .gitattributes
git add .gitattributes design.psd && git commit -m "Add the design"
git push
```

- **Credentials:** the same as git's: access tokens over HTTPS (public repositories need none to read), keys over SSH. Over SSH, the server hands git-lfs a token valid for an hour.
- **Size:** files up to your server's limit (2 GB by default). An upload whose content doesn't match its checksum is refused.
- **Forks** read the files they lack from the repository they were forked from, if you can read it; files pushed to a fork stay in the fork.
- **In the web UI**, an LFS file shows "Stored with Git LFS" with its size. Raw links and archives serve its content.

## Locks

For files that can't be merged, lock them while you work:

```bash
git lfs lock design.psd
git lfs locks
git lfs unlock design.psd
```

- Writers lock files. Only the lock's owner may push changes to a locked file: the server refuses everyone else's pushes, whatever their client does.
- Owners of a lock release it; maintainers and owners can force it (`git lfs unlock --force`).

To have git-lfs make lockable files read-only until you lock them, mark them `lockable` in `.gitattributes`.
