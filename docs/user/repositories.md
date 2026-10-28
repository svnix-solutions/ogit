# Repositories

## Creating one

Click **New repository** in the header (or open `/new`). Choose the owner, your own namespace or a group you maintain, a name, and the visibility. Repositories are private unless you make them public. A new repository starts with a README on `main`.

The same page can **Import from a URL**: see [Mirroring and imports](./mirroring).

```bash
curl -X POST https://git.example.com/api/v1/repository -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "My Project", "is_private": false}'
```

In the API, a repository is its id or its path with the slash URL-encoded: `/api/v1/repository/me%2Fmy-project`.

## Members

The repository's **Settings → Members** gives people and groups a role (see [Roles](./#roles)):

- Owners can grant any role; maintainers only up to developer.
- A group can be given at most maintainer, and only by one of its members. In a group's own namespace, its members already have their group role.
- A repository always keeps at least one owner. Anyone can leave, except the last owner.
- Anyone who can read the repository can see its members.

```bash
curl -X POST https://git.example.com/api/v1/repository/me%2Fmy-project/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"username": "alice", "role": "developer"}'   # or {"group": "backend", ...}
```

## Settings

The **Settings** tab (maintainers and up) has:

| Section | Who | |
|---|---|---|
| **General** | owners; maintainers the description and the wiki | name, description, visibility, archiving, issues on or off, the wiki's editors, approvals required to merge |
| **Members** | maintainers | people and groups |
| **Labels** | maintainers | labels for issues and pull requests |
| **Branch protection**, **Protected tags** | owners | see [Branch protection](./branch-protection) |
| **Webhooks** | maintainers | HTTP callbacks for events |
| **Deploy keys** | maintainers | SSH keys for one repository ([more](./git#deploy-keys)) |
| **Push mirrors**, **Mirror** | maintainers | see [Mirroring](./mirroring) |
| **Runners**, **Secrets**, **Variables**, **Caches** | maintainers | see [Actions](./actions) |
| **Transfer** | owners | move it to another namespace |
| **Delete this repository** | owners | permanently, after typing its name |

## Renaming, moving and archiving

- **Rename** (owners): the path changes, and the old one keeps working, for the web, the API, HTTP and SSH, until another repository takes it. Git data never moves.
- **Transfer** (owners): move it into a group you maintain or own, or back to your namespace. The old path redirects too.
- **Visibility:** public or private, at any time.
- **Archive** (owners): an archived repository can still be read and cloned, but refuses every push, from everyone, and nothing in it can change until it's unarchived.
- **Delete** (owners): repeat the name to confirm. The git data, wiki, LFS objects, release files and Actions artifacts go with it; linked packages are unlinked and made private.

## Browsing

The **Code** tab shows the files and README of the default branch, or any branch, tag or commit. Files are syntax-highlighted, Markdown is rendered, and any file has a raw link and a blame view. **Commits** lists history (per file too), **Branches** lists branches with download links, and **Compare** shows the difference between two refs, ready to open a pull request.

README images are shown only from the repository itself: the site's security policy blocks images from other sites, such as badges served elsewhere.

`/explore` lists public repositories, with search.
