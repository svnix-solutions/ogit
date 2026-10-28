# Pull requests

A pull request proposes merging one branch into another: within a repository, or from a [fork](./forks). Developers and up open them; maintainers and owners merge them. Anyone who can read the repository can read, comment on and review them.

## Opening one

Push a branch, then open **Pull requests → New pull request** (or **Compare** two branches): pick the source and target, check the diff, give it a title and a description. The target is the default branch unless you choose another. Open it as a **draft** when it isn't ready: drafts can't be merged until marked ready.

```bash
R=https://git.example.com/api/v1/repository/me%2Fmy-project
curl -X POST $R/pulls -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"title": "Add login", "source": "feature/login", "draft": true}'
```

Pull requests and issues share their numbers: `#12` is one or the other. Pushing to the source branch updates the pull request.

## Conversation and reviews

The pull request's page has three tabs: the **conversation**, its **commits**, and the **files changed**, where you can comment on any line.

- Comments are Markdown: `@alice` mentions someone (who is notified, if they can read the repository), `#12` links an issue or pull request.
- **Reviews** approve, request changes, or just comment, with a summary and inline comments. Each reviewer's latest review counts. Approvals and change requests only count from developers and up, and never the author's own.
- **Requested reviewers:** the author and developers and up ask anyone who can read the repository for a review. The request goes away when that person reviews.
- **Labels, assignees and a milestone**, like [issues](./issues).
- Checks from CI (commit statuses and [Actions](./actions) runs) appear next to the merge button.

## Merging

The merge box says whether you can merge, and if not, why: missing approvals, a change request, failing checks, a protected branch, conflicts.

- **Methods:** a merge commit, squash, or rebase.
- **Approvals:** the repository's **Settings → General** sets how many are needed (0 by default), and [branch protection](./branch-protection) can require more on some branches. Any outstanding request for changes blocks the merge.
- **Delete the branch** after merging, if you like (a protected branch is always kept).
- **Fixes #12** (or closes, resolves, and their other forms) in the title or description closes that issue when the pull request is merged into the default branch.

```bash
curl -X POST $R/pulls/1/merge -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"method": "squash", "delete_branch": true}'   # or merge, rebase
```

### Conflicts

The server merges file by file, without a git binary. When both branches changed the same file since they diverged, the pull request lists the conflicting files. Merge the target into your branch locally, resolve them, and push:

```bash
git fetch origin && git merge origin/main   # resolve, commit
git push
```

The target branch only moves if nobody pushed to it during the merge. Merged and closed pull requests keep their diff, even after their branches move or are deleted.

## Notifications

The author, requested reviewers, reviewers, commenters and anyone @mentioned are notified of others' comments, reviews, merges and closes. See [Notifications](./notifications).

## API

| | |
|---|---|
| `GET /api/v1/repository/<owner>%2F<repo>/pulls?state=open` | also `merged`, `closed`, `all`; `?label=` |
| `POST /api/v1/repository/<owner>%2F<repo>/pulls/<n>/comments` | `{"body", "inline": {"path", "line", "side": "new"}}` for a line comment |
| `POST /api/v1/repository/<owner>%2F<repo>/pulls/<n>/reviews` | `{"state": "approved"\|"changes_requested"\|"commented", "body"}` |
| `PUT /api/v1/repository/<owner>%2F<repo>/pulls/<n>/reviewers` | `{"reviewers": ["alice"]}` |
| `PUT /api/v1/repository/<owner>%2F<repo>/pulls/<n>/labels`, `.../assignees`, `.../milestone` | as for issues |
| `PATCH /api/v1/repository/<owner>%2F<repo>/pulls/<n>` | title, body, `{"draft": false}` to mark it ready |
