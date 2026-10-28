import { useMutation, useQueryClient } from "@tanstack/react-query";
import { BookOpen, FilePlus, History, Paperclip, Pencil, RotateCcw, Search, Trash2 } from "lucide-react";
import { useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { CopyField } from "@/components/copy-field";
import { DiffView } from "@/components/diff-view";
import { pageParam, Pager } from "@/components/pager";
import { useRepo } from "@/components/repo-layout";
import { ErrorText } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Button, ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api, ApiError, type Repository } from "@/lib/api";
import { formatSize, relativeTime, shortHash, summary, useCommit, useCommits } from "@/lib/browse";
import {
  maxAttachment,
  pageName,
  renderWiki,
  uploadAttachment,
  useWiki,
  useWikiPage,
  useWikiSearch,
  wikiBase,
  wikiKey,
  wikiPagePath,
  wikiRepo,
  type WikiInfo,
  type WikiPage as Page,
} from "@/lib/wiki";

const historyPerPage = 30;

function dirOf(name: string) {
  const i = name.lastIndexOf("/");
  return i < 0 ? "" : name.slice(0, i);
}

function Markdown({ text, repo, name, gitRef, pages }: { text: string; repo: Repository; name: string; gitRef: string; pages?: Set<string> }) {
  const html = useMemo(() => renderWiki(text, { fullPath: repo.full_path, ref: gitRef, dir: dirOf(name), pages }), [text, repo.full_path, gitRef, name, pages]);
  return <div className="markdown text-sm" dangerouslySetInnerHTML={{ __html: html }} />;
}

// WikiPage is every wiki URL: /wiki (Home), /wiki/<page>, and with
// ?new=<name>, ?edit, ?history, ?version=<commit>, ?diff=<commit> or
// ?q=<search>. Page names may contain slashes, so the views are query
// parameters, not path segments.
export function WikiPage() {
  const repo = useRepo();
  const name = useParams()["*"] || "Home";
  const [params] = useSearchParams();
  const info = useWiki(repo.full_path);
  if (info.isError) return <Alert variant="destructive">{info.error.message}</Alert>;
  if (!info.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const wiki = info.data;

  let main;
  if (params.has("new")) main = <Editor wiki={wiki} initialName={params.get("new") ?? ""} />;
  else if (params.has("q")) main = <SearchResults query={params.get("q") ?? ""} />;
  else if (!wiki.exists) main = <Empty wiki={wiki} />;
  else if (params.has("edit")) main = <EditPage wiki={wiki} name={name} />;
  else if (params.has("history")) main = <PageHistory wiki={wiki} name={name} />;
  else if (params.get("diff")) main = <Revision name={name} sha={params.get("diff")!} />;
  else main = <ViewPage wiki={wiki} name={name} version={params.get("version") ?? undefined} />;

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_16rem]">
      <div className="min-w-0">{main}</div>
      <Aside wiki={wiki} />
    </div>
  );
}

function Aside({ wiki }: { wiki: WikiInfo }) {
  const repo = useRepo();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [q, setQ] = useState(params.get("q") ?? "");
  const pages = wiki.pages.filter((p) => !p.name.split("/").pop()!.startsWith("_"));
  function search(e: FormEvent) {
    e.preventDefault();
    if (q.trim()) navigate(`/${repo.full_path}/wiki?q=${encodeURIComponent(q.trim())}`);
  }
  return (
    <aside className="space-y-4">
      {wiki.can_edit && (
        <ButtonLink to={`/${repo.full_path}/wiki?new=`} variant="outline" className="w-full">
          <FilePlus className="h-4 w-4" /> New page
        </ButtonLink>
      )}
      <form onSubmit={search} className="flex gap-1">
        <Input aria-label="Search the wiki" placeholder="Search the wiki" value={q} onChange={(e) => setQ(e.target.value)} />
        <Button type="submit" variant="ghost" size="icon" aria-label="Search">
          <Search className="h-4 w-4" />
        </Button>
      </form>
      {wiki.exists && (
        <Card className="p-3">
          <h2 className="mb-2 flex items-center gap-1.5 text-sm font-semibold">
            <BookOpen className="h-4 w-4" /> Pages <span className="text-muted-foreground">{pages.length}</span>
          </h2>
          <ul className="space-y-1 text-sm" aria-label="Pages">
            {pages.map((p) => (
              <li key={p.name} className="truncate">
                <Link to={wikiPagePath(repo.full_path, p.name)} className="hover:underline" title={p.name}>
                  {p.name.includes("/") && <span className="text-muted-foreground">{dirOf(p.name)}/</span>}
                  {p.title}
                </Link>
              </li>
            ))}
          </ul>
          {wiki.truncated && <p className="mt-2 text-xs text-muted-foreground">Only the first pages are listed.</p>}
        </Card>
      )}
      <div className="space-y-1.5">
        <p className="text-xs text-muted-foreground">Clone this wiki</p>
        <CopyField label="Wiki clone URL" value={`${window.location.origin}/${repo.full_path}.wiki.git`} />
      </div>
    </aside>
  );
}

function Empty({ wiki }: { wiki: WikiInfo }) {
  const repo = useRepo();
  return (
    <Card className="space-y-3 p-8 text-center">
      <BookOpen className="mx-auto h-8 w-8 text-muted-foreground" />
      <h2 className="text-lg font-semibold">This wiki is empty</h2>
      <p className="text-sm text-muted-foreground">
        {wiki.can_edit ? "Write documentation for this repository, page by page." : "Nobody has written a page yet."}
      </p>
      {wiki.can_edit && (
        <ButtonLink to={`/${repo.full_path}/wiki?new=Home`}>
          <FilePlus className="h-4 w-4" /> Create the first page
        </ButtonLink>
      )}
    </Card>
  );
}

function ViewPage({ wiki, name, version }: { wiki: WikiInfo; name: string; version?: string }) {
  const repo = useRepo();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const view = useWikiPage(repo.full_path, name, version);
  const pages = useMemo(() => new Set(wiki.pages.map((p) => p.name)), [wiki.pages]);
  const revert = useMutation({
    mutationFn: () => api("POST", `${wikiBase(repo.full_path)}/revert`, { name, version, base: view.data?.head }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: wikiKey(repo.full_path) });
      navigate(wikiPagePath(repo.full_path, name));
    },
  });
  if (view.error instanceof ApiError && view.error.status === 404) {
    return (
      <Card className="space-y-3 p-6">
        <h2 className="text-lg font-semibold">{name.split("/").pop()!.replaceAll("-", " ")}</h2>
        <p className="text-sm text-muted-foreground">This page doesn't exist{version ? " in that version" : ""}.</p>
        {wiki.can_edit && !version && (
          <ButtonLink to={`/${repo.full_path}/wiki?new=${encodeURIComponent(name)}`} size="sm">
            Create it
          </ButtonLink>
        )}
      </Card>
    );
  }
  if (view.isError) return <Alert variant="destructive">{view.error.message}</Alert>;
  if (!view.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { page, sidebar, footer } = view.data;
  const gitRef = version ?? "HEAD";
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-2xl font-semibold">{page.title}</h2>
        <span className="ml-auto flex gap-1">
          {wiki.can_edit && !version && (
            <ButtonLink to={`${wikiPagePath(repo.full_path, page.name)}?edit`} variant="outline" size="sm">
              <Pencil className="h-4 w-4" /> Edit
            </ButtonLink>
          )}
          <ButtonLink to={`${wikiPagePath(repo.full_path, page.name)}?history`} variant="ghost" size="sm">
            <History className="h-4 w-4" /> History
          </ButtonLink>
        </span>
      </div>
      {page.commit && (
        <p className="text-xs text-muted-foreground">
          {page.commit.author.name} edited this page {relativeTime(page.commit.author.when)}
        </p>
      )}
      {version && (
        <Alert>
          <div className="flex flex-wrap items-center gap-2">
            <span>
              You're reading this page as it was at <code>{shortHash(version)}</code>.{" "}
              <Link to={wikiPagePath(repo.full_path, page.name)} className="underline">
                Latest version
              </Link>
            </span>
            {wiki.can_edit && (
              <Button size="sm" variant="outline" className="ml-auto" onClick={() => revert.mutate()} disabled={revert.isPending}>
                <RotateCcw className="h-4 w-4" /> Revert to this version
              </Button>
            )}
          </div>
          <ErrorText error={revert.error} />
        </Alert>
      )}
      <div className="grid gap-4 md:grid-cols-[1fr_14rem]">
        <Card className="min-w-0 p-5">
          {page.content.trim() ? (
            <Markdown text={page.content} repo={repo} name={page.name} gitRef={gitRef} pages={pages} />
          ) : (
            <p className="text-sm text-muted-foreground">This page is empty.</p>
          )}
        </Card>
        {sidebar && (
          <Card className="h-fit p-4" aria-label="Sidebar">
            <Markdown text={sidebar.content} repo={repo} name={sidebar.name} gitRef={gitRef} pages={pages} />
          </Card>
        )}
      </div>
      {footer && (
        <Card className="p-4" aria-label="Footer">
          <Markdown text={footer.content} repo={repo} name={footer.name} gitRef={gitRef} pages={pages} />
        </Card>
      )}
    </div>
  );
}

function EditPage({ wiki, name }: { wiki: WikiInfo; name: string }) {
  const repo = useRepo();
  const view = useWikiPage(repo.full_path, name);
  if (view.isError) return <Alert variant="destructive">{view.error.message}</Alert>;
  if (!view.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return <Editor wiki={wiki} page={view.data.page} base={view.data.head} />;
}

// Editor creates a page (no page) or edits one, from base (the head it was
// read at: the server refuses the edit if the page changed since).
function Editor({ wiki, page, base, initialName = "" }: { wiki: WikiInfo; page?: Page; base?: string; initialName?: string }) {
  const repo = useRepo();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(page?.name ?? initialName);
  const [content, setContent] = useState(page?.content ?? "");
  const [message, setMessage] = useState("");
  const [preview, setPreview] = useState(false);
  const [uploadError, setUploadError] = useState<unknown>(null);
  const [uploading, setUploading] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);
  const pages = useMemo(() => new Set(wiki.pages.map((p) => p.name)), [wiki.pages]);

  const done = (target?: string) => {
    qc.invalidateQueries({ queryKey: wikiKey(repo.full_path) });
    navigate(target ? wikiPagePath(repo.full_path, target) : `/${repo.full_path}/wiki`);
  };
  const save = useMutation({
    mutationFn: () =>
      page
        ? api<{ name: string }>("PUT", `${wikiBase(repo.full_path)}/page`, {
            name: page.name,
            new_name: name.trim() && pageName(name) !== page.name ? name : "",
            content,
            message,
            base,
          })
        : api<{ name: string }>("POST", `${wikiBase(repo.full_path)}/pages`, { name, content, message }),
    onSuccess: (r) => done(r.name),
  });
  const remove = useMutation({
    mutationFn: () => api("DELETE", `${wikiBase(repo.full_path)}/page?name=${encodeURIComponent(page!.name)}&base=${base ?? ""}`),
    onSuccess: () => done(),
  });

  async function attach(files: FileList | null) {
    const f = files?.[0];
    if (!f) return;
    setUploadError(null);
    if (f.size > maxAttachment) {
      setUploadError(new Error(`${f.name} is larger than ${formatSize(maxAttachment)}.`));
      return;
    }
    setUploading(true);
    try {
      const a = await uploadAttachment(repo.full_path, f);
      const link = `${f.type.startsWith("image/") ? "!" : ""}[${f.name}](/${a.path})`;
      const at = area.current?.selectionStart ?? content.length;
      setContent(content.slice(0, at) + link + content.slice(at));
      qc.invalidateQueries({ queryKey: [...wikiKey(repo.full_path), "info"] });
    } catch (err) {
      setUploadError(err);
    } finally {
      setUploading(false);
      if (file.current) file.current.value = "";
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  const conflict = save.error instanceof ApiError && save.error.type === "WIKI_CONFLICT";
  return (
    <Card className="space-y-4 p-5">
      <h2 className="text-lg font-semibold">{page ? `Edit ${page.title}` : "New page"}</h2>
      <form className="space-y-4" onSubmit={submit}>
        <div className="space-y-1.5">
          <Label htmlFor="wiki-name">Title</Label>
          <Input id="wiki-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Getting Started" required />
          <p className="text-xs text-muted-foreground">
            Folders with <code>/</code> (<code>Guides/Install</code>). <code>_Sidebar</code> and <code>_Footer</code> are shown with every page.
            {page && " Changing the title renames the page."}
          </p>
        </div>
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <Label htmlFor="wiki-content">Content</Label>
            <span className="ml-auto flex gap-1">
              <Button type="button" size="sm" variant={preview ? "ghost" : "outline"} onClick={() => setPreview(false)}>
                Write
              </Button>
              <Button type="button" size="sm" variant={preview ? "outline" : "ghost"} onClick={() => setPreview(true)}>
                Preview
              </Button>
              <Button type="button" size="sm" variant="ghost" disabled={uploading} onClick={() => file.current?.click()}>
                <Paperclip className="h-4 w-4" /> {uploading ? "Uploading…" : "Attach a file"}
              </Button>
              <input ref={file} type="file" className="hidden" aria-label="Attachment" onChange={(e) => attach(e.target.files)} />
            </span>
          </div>
          {preview ? (
            <div className="min-h-64 rounded-md border p-3">
              {content.trim() ? (
                <Markdown text={content} repo={repo} name={pageName(name || "Home")} gitRef="HEAD" pages={pages} />
              ) : (
                <p className="text-sm text-muted-foreground">Nothing to preview.</p>
              )}
            </div>
          ) : (
            <Textarea
              id="wiki-content"
              ref={area}
              className="min-h-64 font-mono"
              placeholder="Markdown. Link pages with [[Page Name]] or [[text|Page Name]]."
              value={content}
              onChange={(e) => setContent(e.target.value)}
            />
          )}
          <ErrorText error={uploadError} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="wiki-message">Edit message</Label>
          <Input id="wiki-message" value={message} onChange={(e) => setMessage(e.target.value)} placeholder={page ? `Updated ${page.title}` : "Created page"} />
        </div>
        {conflict ? (
          <Alert variant="destructive">
            Someone changed this page while you were editing it. Copy your text, then{" "}
            <Link to={wikiPagePath(repo.full_path, page?.name ?? name)} className="underline">
              open the latest version
            </Link>{" "}
            and apply your changes again.
          </Alert>
        ) : (
          <ErrorText error={save.error} />
        )}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={save.isPending || !name.trim()}>
            {page ? "Save page" : "Create page"}
          </Button>
          <ButtonLink to={page ? wikiPagePath(repo.full_path, page.name) : `/${repo.full_path}/wiki`} variant="ghost">
            Cancel
          </ButtonLink>
          {page && (
            <Button type="button" variant="ghost" className="ml-auto text-destructive" onClick={() => setConfirmDelete(true)}>
              <Trash2 className="h-4 w-4" /> Delete page
            </Button>
          )}
        </div>
      </form>
      {page && confirmDelete && (
        <Alert variant="destructive" className="space-y-2">
          <p>Delete {page.title}? Its history stays in the wiki's git repository.</p>
          <div className="flex gap-2">
            <Button variant="destructive" size="sm" onClick={() => remove.mutate()} disabled={remove.isPending}>
              Delete
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setConfirmDelete(false)}>
              Keep it
            </Button>
          </div>
          <ErrorText error={remove.error} />
        </Alert>
      )}
    </Card>
  );
}

function PageHistory({ wiki, name }: { wiki: WikiInfo; name: string }) {
  const repo = useRepo();
  const [params, setParams] = useSearchParams();
  const page = pageParam(params);
  const file = wiki.pages.find((p) => p.name === name)?.path ?? `${name}.md`;
  const commits = useCommits(wikiRepo(repo.full_path), `HEAD/${file}`, page, historyPerPage);
  const title = name.split("/").pop()!.replaceAll("-", " ");
  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">
        History of{" "}
        <Link to={wikiPagePath(repo.full_path, name)} className="hover:underline">
          {title}
        </Link>
      </h2>
      {commits.isError && <Alert variant="destructive">{commits.error.message}</Alert>}
      {commits.data && (
        <Card className="divide-y">
          {commits.data.commits.map((c) => (
            <div key={c.hash} className="flex flex-wrap items-center gap-2 px-4 py-3 text-sm">
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">{summary(c.message)}</p>
                <p className="text-xs text-muted-foreground">
                  {c.author.name} · {relativeTime(c.author.when)}
                </p>
              </div>
              <ButtonLink to={`${wikiPagePath(repo.full_path, name)}?diff=${c.hash}`} variant="ghost" size="sm" aria-label={`Changes in ${shortHash(c.hash)}`}>
                <code>{shortHash(c.hash)}</code>
              </ButtonLink>
              <ButtonLink to={`${wikiPagePath(repo.full_path, name)}?version=${c.hash}`} variant="outline" size="sm" aria-label={`View at ${shortHash(c.hash)}`}>
                View
              </ButtonLink>
            </div>
          ))}
          {commits.data.commits.length === 0 && <p className="p-4 text-sm text-muted-foreground">No history for this page.</p>}
        </Card>
      )}
      {commits.data && (
        <Pager page={page} perPage={historyPerPage} count={commits.data.commits.length} onPage={(p) => setParams({ history: "", page: String(p + 1) })} />
      )}
    </div>
  );
}

function Revision({ name, sha }: { name: string; sha: string }) {
  const repo = useRepo();
  const c = useCommit(wikiRepo(repo.full_path), sha);
  if (c.isError) return <Alert variant="destructive">{c.error.message}</Alert>;
  if (!c.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold">{summary(c.data.commit.message)}</h2>
        <p className="text-xs text-muted-foreground">
          {c.data.commit.author.name} · {relativeTime(c.data.commit.author.when)} · <code>{shortHash(sha)}</code> ·{" "}
          <Link to={`${wikiPagePath(repo.full_path, name)}?history`} className="underline">
            History
          </Link>
        </p>
      </div>
      <DiffView
        diff={c.data.diff}
        fullPath={wikiRepo(repo.full_path)}
        gitRef={sha}
        fileHref={(path) => {
          const page = /\.(md|markdown)$/i.test(path) ? pageName(path) : "";
          return page ? `${wikiPagePath(repo.full_path, page)}?version=${sha}` : `/${wikiRepo(repo.full_path)}/raw/${sha}/${path}`;
        }}
      />
    </div>
  );
}

function SearchResults({ query }: { query: string }) {
  const repo = useRepo();
  const results = useWikiSearch(repo.full_path, query);
  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">Pages with “{query}”</h2>
      {results.isError && <Alert variant="destructive">{results.error.message}</Alert>}
      {results.data && results.data.length === 0 && <p className="text-sm text-muted-foreground">No page matches.</p>}
      {results.data && results.data.length > 0 && (
        <Card className="divide-y">
          {results.data.map((r) => (
            <div key={r.name} className="px-4 py-3">
              <Link to={wikiPagePath(repo.full_path, r.name)} className="font-medium hover:underline">
                {r.title}
              </Link>
              {r.name.includes("/") && <span className="ml-2 text-xs text-muted-foreground">{r.name}</span>}
              <p className="text-sm text-muted-foreground">{r.snippet}</p>
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}
