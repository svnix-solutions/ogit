# GitHub-compatible API

Tools built for GitHub (or GitHub Enterprise) often only need a few endpoints. oGit serves them under GitHub Enterprise's paths, so these tools work by pointing them at your server:

- API URL: `https://git.example.com/api/v3/`
- upload URL: `https://git.example.com/api/uploads/`

Authenticate with a personal access token or a repository token (`Authorization: token <token>`). Repository paths are `<owner>/<repo>`, sub-groups included (`org/team/app`).

## What's there

| Endpoints | |
|---|---|
| `POST /repos/{owner}/{repo}/statuses/{sha}`, `GET .../statuses/{sha}`, `GET .../commits/{ref}/status` | [commit statuses](./statuses) |
| `GET\|POST /repos/{owner}/{repo}/releases`, `GET\|PATCH\|DELETE .../releases/{id}`, `GET .../releases/latest`, `GET .../releases/tags/{tag}`, `POST .../releases/generate-notes` | releases (create also takes `generate_release_notes`) |
| `GET .../releases/{id}/assets`, `GET\|PATCH\|DELETE .../releases/assets/{id}` | release assets; download with `Accept: application/octet-stream` |
| `POST /api/uploads/repos/{owner}/{repo}/releases/{id}/assets?name=` | upload an asset (the release's `upload_url`) |
| `GET /repos/{owner}/{repo}/tarball/{ref}`, `.../zipball/{ref}` | source archives (`actions/checkout` without git uses them) |
| `GET /repos/{owner}/{repo}/actions/runs/{id}/artifacts`, `.../actions/artifacts/{id}`, `.../actions/artifacts/{id}/zip` | Actions artifacts |

Ids are numbers here, because GitHub's clients parse them as integers. Anything not listed isn't served.

## goreleaser

```yaml
# .goreleaser.yaml
github_urls:
  api: https://git.example.com/api/v3/
  upload: https://git.example.com/api/uploads/
  download: https://git.example.com/
```

Then `GITHUB_TOKEN=<token> goreleaser release`, with a token that can write the repository (`write:repository`), for instance a repository token, or the job token in [Actions](/user/actions) with `permissions: contents: write`.
