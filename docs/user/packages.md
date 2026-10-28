# Packages

oGit hosts container images, npm packages, Go modules and generic files. Packages belong to a user or a group, like repositories, and can be linked to one of its repositories. A namespace's **Packages** tab lists them, and a repository with linked packages or a Go module gets one too.

## Credentials

Publish with an [access token](./account#access-tokens) that has `write:packages` (`read:packages` to pull private ones), or from [Actions](./actions) with `permissions: packages: write`.

**Who publishes:** the namespace's user, a group's maintainers and owners, the linked repository's developers and up, and that repository's Actions jobs (which create packages linked to their repository).

**Visibility:** packages are private by default. A linked package can follow its repository (public with it), or be made public on its own. Readers of the linked repository can read the package.

## Container images

```bash
docker login git.example.com                 # your user name, and the token as the password
docker tag app git.example.com/me/app:1.0
docker push git.example.com/me/app:1.0
docker pull git.example.com/me/app:1.0
```

The registry is on your server's host name. Docker only talks plain HTTP to `localhost`, `127.0.0.0/8` or registries listed in `insecure-registries`, so use HTTPS. Layers are shared between the images of a namespace. In a group's sub-group, images are `org/team/app`.

From Actions: `echo "$GITHUB_TOKEN" | docker login git.example.com -u x --password-stdin`. The [container guide](/guides/publish-a-container) has a complete workflow.

## npm

Packages are scoped by their owner: `@me/name`. In `.npmrc`:

```ini
@me:registry=https://git.example.com/api/packages/npm/
//git.example.com/api/packages/npm/:_authToken=${OGIT_TOKEN}
```

`npm publish`, `install`, `view`, `dist-tag`, `deprecate`, `unpublish`, `search` and `whoami` work; `npm login` doesn't (use a token). In Actions, use `setup-node` with `registry-url` and `NODE_AUTH_TOKEN`.

- Versions can't change once published, and their integrity is checked.
- Writers deprecate versions and manage dist-tags; package admins unpublish (`latest` then moves to the highest version left).
- The README shown is the one in the tarball.

## Go modules

Every repository is a Go module, `git.example.com/<owner>/<repo>` (`/v2` and up for major versions), served by a module proxy from its tags. Nothing needs publishing: tag a version and push it.

```bash
go env -w GOPRIVATE=git.example.com
go get git.example.com/me/mylib@v1.2.0
```

- Versions are semver tags whose go.mod names the module (v0 and v1 tags without a go.mod work too). Branches and commits resolve to pseudo-versions.
- For private repositories, add `machine git.example.com login <user> password <token>` to `~/.netrc` (a token that reads the repository; go only sends it over HTTPS).
- The module path is your server's host name, without a port, and it needs a dot (`git.example.com`, not `localhost`).
- The proxy is also at `https://git.example.com/api/packages/go`, for `GOPROXY`. Modules in sub-directories aren't served.

## Generic files

Any file, by name and version:

```bash
curl -u me:$TOKEN --upload-file tool.tar.gz https://git.example.com/api/packages/me/generic/tool/1.0.0/tool.tar.gz
curl -u me:$TOKEN -O https://git.example.com/api/packages/me/generic/tool/1.0.0/tool.tar.gz
```

A version's file is published once; downloads always come as downloads.

## Deleting

Package admins (the namespace's user, a group's maintainers and owners, and the linked repository's maintainers and owners) delete versions and packages. For images, deleting a tag only untags; delete a digest to delete an image. Layers no image uses any more are deleted after a day.

Deleting a repository unlinks its packages and makes them private. Users and groups with packages can't be deleted.

Files and layers can be up to your server's limit (2 GB by default).

## API

| | |
|---|---|
| `GET /api/v1/packages/<owner>[?type=]` | a namespace's packages |
| `GET\|PATCH\|DELETE /api/v1/packages/<owner>/<type>/<name>` | a package (image names with slashes URL-encoded; `DELETE` takes `?confirm=<name>`) |
| `DELETE .../versions/<version>` | a version (an image digest, or tag to untag) |
| `GET /api/v1/repository/<owner>%2F<repo>/packages`, `/api/v1/repository/<owner>%2F<repo>/go-module[?summary=true]` | a repository's packages, and its Go module |
