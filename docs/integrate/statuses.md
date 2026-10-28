# Commit statuses

CI systems report results on commits, as they do to GitHub. Pull requests show them next to the merge button, and branch protection can require them.

## Posting a status

Use a token with `write:status` (a personal one that can post statuses without git access, or a repository token), or `write:repository`. Developers and up post; anyone who can read the repository reads.

```bash
R=https://git.example.com/api/v1/repository/me%2Fmy-project
curl -X POST $R/statuses/<sha> -H "Authorization: token $CI_TOKEN" -H 'Content-Type: application/json' \
  -d '{"state": "success", "context": "ci/build", "description": "All tests passed", "target_url": "https://ci.example.com/runs/42"}'
curl "$R/status?ref=main" -H "Authorization: token $CI_TOKEN"   # the combined state
```

Or with GitHub's paths, for CI plugins that report to GitHub Enterprise (API URL `https://git.example.com/api/v3`):

```bash
curl -X POST https://git.example.com/api/v3/repos/me/my-project/statuses/<sha> -H "Authorization: token $CI_TOKEN" \
  -d '{"state": "pending", "context": "ci/build"}'
curl https://git.example.com/api/v3/repos/me/my-project/commits/main/status -H "Authorization: token $CI_TOKEN"
```

## States

- `state`: `pending`, `success`, `failure` or `error`.
- `context` (default `default`): each context counts with its latest report.
- **Combined:** `failure` if any context failed or errored; otherwise `pending` if any is pending, or none was reported; otherwise `success`.
- `target_url` must be `http` or `https`; descriptions are cut to 140 characters.

[Actions](/user/actions) jobs post their own statuses, named `<workflow> / <job> (<event>)`.

## Required checks

A branch protection rule's `required_contexts` must each be `success` on a pull request's head commit before it can be merged. A new push needs new reports. A pull request's `required_checks` lists their states. See [Branch protection](/user/branch-protection).

Each status is also a `status` [webhook](./webhooks) event.
