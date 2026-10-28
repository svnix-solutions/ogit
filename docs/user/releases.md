# Releases

A release is a tag with notes and files: what people download. The **Releases** tab lists them with their notes, files, download counts and source code archives, and the code page shows the latest one.

## Who

Writers publish releases: developers and up, and CI with a token that has `write:repository`. Anyone who can read the repository sees the published ones.

## Publishing one

**Releases → Draft a new release**:

1. **Tag:** pick an existing tag, or type a new one; a new tag is created from the branch you choose when the release is published.
2. **Title and notes** in Markdown. **Generate notes** lists the pull requests merged since the previous release.
3. **Pre-release:** for betas and release candidates. Pre-releases are never shown as the latest release.
4. **Save draft** or **Publish release**.

Drafts are visible to writers only, create no tag and notify nobody until published. A published release keeps its tag and stays published. Deleting a release keeps its tag.

```bash
curl -X POST https://git.example.com/api/v1/repository/me%2Fmy-project/releases -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"tag_name": "v1.2.0", "target_commitish": "main", "name": "v1.2.0", "body": "..."}'
```

## Files

Attach files on the release's page, on drafts too, up to your server's limit (100 MB by default). Writers upload, rename and delete them.

- Downloads are counted, and always come as downloads, whatever the file's type.
- Download links look like `https://git.example.com/me/my-project/releases/download/v1.2.0/app_linux_amd64.tar.gz`. Private repositories need a token, as for git.
- Drafts' files are only downloadable by writers.

From CI, upload the file as the request body:

```bash
curl -X POST "https://git.example.com/api/v1/repository/me%2Fmy-project/releases/$RELEASE/assets?name=app_linux_amd64.tar.gz" \
  -H "Authorization: token $CI_TOKEN" -H 'Content-Type: application/gzip' --data-binary @dist/app_linux_amd64.tar.gz
```

Release tools built for GitHub, such as goreleaser, can publish to oGit through its GitHub-compatible releases API.

## Protected tags

To control who may create release tags, protect them: see [Protected tags](./branch-protection#protected-tags). Publishing a release that would create a protected tag needs the same rights as pushing it.
