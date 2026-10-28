# Forks

A fork is your own copy of a repository: to work on it without write access, and propose changes back with a pull request.

## Forking

**Fork** on a repository's page copies it into your namespace, or a group you maintain. Anyone who can read a repository can fork it.

- A fork is a full copy of the original's branches, tags and wiki. Deleting the original, or making it private, never affects the fork.
- Forks of private repositories are private, and the original's members don't get access to them.
- **Forks** lists the forks you can read, and a fork shows which repository it came from, if you can read it.

## Pull requests from a fork

Push a branch to your fork, then open a pull request into the original: in the original's **Pull requests → New pull request**, choose your fork as the source. You need write access to the fork and read access to the original.

- The fork's commits are copied into the original when you open the pull request and every time you push the branch, so merged and closed pull requests stay readable even after the fork is deleted.
- The original's maintainers merge; "delete branch" never touches your fork.
- **Maintainer edits:** unless you untick "Allow edits by maintainers", the original's developers may push to your pull request's branch, and only that branch, while it's open, to fix it up before merging.
- Runs of the original's [Actions](./actions) on pull requests from forks get no secrets and a read-only token, and a first-time contributor's wait for a maintainer's approval.

```bash
curl -X POST https://git.example.com/api/v1/repository/alice%2Fapp/forks -H "Authorization: Bearer $SESSION"
curl -X POST https://git.example.com/api/v1/repository/alice%2Fapp/pulls -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"title": "Fix", "source": "fix", "source_repository": "bob/app"}'
```

## Keeping it up to date

**Sync fork** fast-forwards a branch of your fork to the original's, when the branch has no commits of its own. If it does, merge or rebase locally:

```bash
git remote add upstream https://git.example.com/alice/app.git
git fetch upstream && git rebase upstream/main && git push --force-with-lease
```

## API

| | |
|---|---|
| `POST /api/v1/repository/<owner>%2F<repo>/forks` | `{"namespace", "name"}`, both optional |
| `GET /api/v1/repository/<owner>%2F<repo>/forks` | the forks you can read |
| `GET /api/v1/repository/<owner>%2F<repo>/pulls/preview?source_repository=bob/app&source=fix` | compare before opening a pull request |
| `POST /api/v1/repository/<owner>%2F<repo>/sync` | `{"branch": "main"}`: fast-forward a fork's branch (`DIVERGED` otherwise) |
