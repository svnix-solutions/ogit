# Wikis

Every repository has a wiki: Markdown pages kept in a git repository of their own, `<owner>/<repo>.wiki.git`. It is created by the first page you write, or the first push to it.

## Who

Anyone who can read the repository reads its wiki. Developers and up edit it by default. In **Settings → General**, maintainers can let every signed-in reader edit it, or turn it off (hidden everywhere, its content kept). The wikis of archived repositories and mirrors are read-only.

## Pages

The **Wiki** tab lists the pages, shows each one with its sidebar and footer, and searches them.

- A page is a Markdown file, in folders if you like. Its name is its path without `.md` (`Docs/Getting-Started`), and its title is the last part with dashes shown as spaces ("Getting Started"). **Home** is the front page.
- **`_Sidebar`** and **`_Footer`** are shown with every page. The nearest one wins, from the page's folder up, so a folder can have its own.
- **Links:** `[[Page Name]]`, or `[[text|Page Name]]` to show other text. Links to pages that don't exist yet are red, and open the new page form. Ordinary Markdown links to other pages work too.

## Editing

**New page** or **Edit** opens the editor, with a preview and **Attach a file**. Changing the title renames the page. Each save is a commit by you, with your message or a default one ("Updated Home").

If someone changed the same page while you were editing, your save is refused, so nobody's work is overwritten silently: copy your text, open the latest version, and apply your changes again. Edits of other pages in the meantime don't conflict.

**Attachments** (images and other files, 25 MiB each) are stored with Git LFS under `uploads/`, and the editor inserts a link where your cursor is.

## History

**History** on a page lists its commits. Each one shows its changes, the page as it was then, and, for writers, **Revert to this version** (a new commit that brings the old text back).

## With git

```bash
git clone https://git.example.com/me/my-project.wiki.git
```

Clone, edit and push with your usual tools: same tokens, keys and roles as the repository. Pages are `.md` files (`.markdown` is read too). Branch protection and LFS locks don't apply to the wiki.

## Copies

Forks copy the wiki (unless it's off). Pull mirrors and imports fetch the remote's wiki when it has one, and push mirrors push to the remote's wiki. Renames and transfers keep it; deleting the repository deletes it.

People watching the repository at "all activity" are notified of wiki changes.

## API

| | |
|---|---|
| `GET /api/v1/repository/<owner>%2F<repo>/wiki` | who edits it, whether it exists, whether you can edit, and its pages |
| `GET /api/v1/repository/<owner>%2F<repo>/wiki/page?name=&version=` | a page (Home by default) with its sidebar, footer, and `head` |
| `POST /api/v1/repository/<owner>%2F<repo>/wiki/pages`, `PUT\|DELETE /api/v1/repository/<owner>%2F<repo>/wiki/page` | create; edit, rename (`new_name`) or delete. `base` is the `head` you read: a changed page is a `WIKI_CONFLICT` |
| `POST /api/v1/repository/<owner>%2F<repo>/wiki/revert` | `{"name", "version", "base"}` |
| `GET /api/v1/repository/<owner>%2F<repo>/wiki/search?q=` | pages with every word |
| `POST /api/v1/repository/<owner>%2F<repo>/wiki/uploads?name=` | an attachment: the file is the body |

History and diffs are the browse API on `<owner>%2F<repo>.wiki` (`/api/v1/repository/me%2Fapp.wiki/commits?spec=main/Home.md`), and files are at `/<owner>/<repo>.wiki/raw/<ref>/<path>`.
