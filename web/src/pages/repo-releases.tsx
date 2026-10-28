import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Download, FileArchive, Package, Pencil, Tag, Trash2, Upload } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { pageParam, Pager } from "@/components/pager";
import { useRepo } from "@/components/repo-layout";
import { ErrorText } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button, ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, ApiError, atLeast, type Repository } from "@/lib/api";
import { formatSize, relativeTime, useRefs } from "@/lib/browse";
import { renderMarkdown } from "@/lib/markdown";
import { useInstance } from "@/lib/queries";
import {
  archiveURL,
  downloadURL,
  releasePath,
  releasesBase,
  releasesKey,
  uploadAsset,
  useLatestRelease,
  useRelease,
  useReleaseByTag,
  useReleases,
  type Asset,
  type Release,
} from "@/lib/releases";
import { NotFoundPage } from "./not-found";

const perPage = 10;

function Markdown({ text, fullPath }: { text: string; fullPath: string }) {
  const html = useMemo(() => renderMarkdown(text, { fullPath, ref: "HEAD", dir: "", links: true }), [text, fullPath]);
  return <div className="markdown text-sm" dangerouslySetInnerHTML={{ __html: html }} />;
}

export function ReleaseBadges({ release, latestId }: { release: Release; latestId?: string }) {
  return (
    <>
      {release.draft && <Badge className="border-amber-500 text-amber-700 dark:text-amber-400">Draft</Badge>}
      {release.prerelease && <Badge className="border-orange-500 text-orange-700 dark:text-orange-400">Pre-release</Badge>}
      {release.id === latestId && <Badge className="border-green-600 text-green-700 dark:text-green-400">Latest</Badge>}
    </>
  );
}

export function ReleasesPage() {
  const repo = useRepo();
  const [params, setParams] = useSearchParams();
  const page = pageParam(params);
  const list = useReleases(repo.full_path, page, perPage);
  const latest = useLatestRelease(repo.full_path);
  const canWrite = atLeast(repo.role, "developer") && !repo.archived;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-lg font-semibold">Releases</h2>
        {canWrite && (
          <ButtonLink to={`/${repo.full_path}/releases/new`} className="ml-auto" size="sm">
            Draft a new release
          </ButtonLink>
        )}
      </div>
      <ErrorText error={list.error} />
      {list.data?.releases.length === 0 && (
        <Card className="p-8 text-center text-sm text-muted-foreground">
          <Package className="mx-auto mb-2 h-6 w-6" />
          There are no releases yet.
        </Card>
      )}
      {list.data?.releases.map((r) => (
        <ReleaseCard key={r.id} repo={repo} release={r} latestId={latest.data?.id} link />
      ))}
      {list.data && (
        <Pager page={page} perPage={perPage} count={list.data.releases.length} total={list.data.total} onPage={(p) => setParams({ page: String(p + 1) })} />
      )}
    </div>
  );
}

function ReleaseCard({ repo, release: r, latestId, link, children }: { repo: Repository; release: Release; latestId?: string; link?: boolean; children?: React.ReactNode }) {
  const title = r.name || r.tag_name;
  return (
    <Card className="space-y-4 p-5">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-lg font-semibold">
          {link ? (
            <Link to={releasePath(repo.full_path, r.tag_name)} className="hover:underline">
              {title}
            </Link>
          ) : (
            title
          )}
        </h3>
        <ReleaseBadges release={r} latestId={latestId} />
      </div>
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="flex items-center gap-1">
          <Tag className="h-3 w-3" />
          <code>{r.tag_name}</code>
        </span>
        {r.author && <span>by {r.author}</span>}
        <span>{r.published_at ? `released ${relativeTime(r.published_at)}` : `drafted ${relativeTime(r.created_at)}`}</span>
      </p>
      {r.body.trim() ? <Markdown text={r.body} fullPath={repo.full_path} /> : <p className="text-sm text-muted-foreground">No release notes.</p>}
      <Assets repo={repo} release={r} />
      {children}
    </Card>
  );
}

function Assets({ repo, release: r }: { repo: Repository; release: Release }) {
  const qc = useQueryClient();
  const remove = useMutation({
    mutationFn: (a: Asset) => api("DELETE", `${releasesBase(repo.full_path)}/${r.id}/assets/${a.id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: releasesKey(repo.full_path) }),
  });
  const sources = !r.draft
    ? (["zip", "tar.gz"] as const).map((f) => ({ format: f, url: archiveURL(repo.full_path, r.tag_name, f) }))
    : [];
  if (r.assets.length === 0 && sources.length === 0) return null;
  return (
    <div className="space-y-1">
      <h4 className="text-sm font-medium">Assets</h4>
      <ul className="divide-y rounded-md border text-sm">
        {r.assets.map((a) => (
          <li key={a.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
            <Package className="h-4 w-4 shrink-0 text-muted-foreground" />
            <a href={downloadURL(a)} className="min-w-0 truncate font-medium text-primary hover:underline">
              {a.name}
            </a>
            {a.label && <span className="text-xs text-muted-foreground">{a.label}</span>}
            <span className="ml-auto flex items-center gap-3 text-xs text-muted-foreground">
              <span>{formatSize(a.size)}</span>
              <span className="flex items-center gap-1" title="Downloads">
                <Download className="h-3 w-3" />
                {a.download_count}
              </span>
              {r.can_edit && (
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Delete ${a.name}`}
                  disabled={remove.isPending}
                  onClick={() => window.confirm(`Delete ${a.name}?`) && remove.mutate(a)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              )}
            </span>
          </li>
        ))}
        {sources.map((s) => (
          <li key={s.format} className="flex items-center gap-2 px-3 py-2">
            <FileArchive className="h-4 w-4 shrink-0 text-muted-foreground" />
            <a href={s.url} className="text-primary hover:underline">
              Source code ({s.format})
            </a>
          </li>
        ))}
      </ul>
      <ErrorText error={remove.error} />
    </div>
  );
}

export function ReleasePage() {
  const repo = useRepo();
  const tag = useParams()["*"] ?? "";
  const navigate = useNavigate();
  const qc = useQueryClient();
  const release = useReleaseByTag(repo.full_path, tag);
  const latest = useLatestRelease(repo.full_path);
  const remove = useMutation({
    mutationFn: () => api("DELETE", `${releasesBase(repo.full_path)}/${release.data!.id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: releasesKey(repo.full_path) });
      navigate(`/${repo.full_path}/releases`);
    },
  });

  if (release.error instanceof ApiError && release.error.status === 404) return <NotFoundPage />;
  if (release.isError) return <Alert variant="destructive">{release.error.message}</Alert>;
  if (!release.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const r = release.data;
  return (
    <div className="space-y-4">
      <Link to={`/${repo.full_path}/releases`} className="text-sm text-primary hover:underline">
        ← All releases
      </Link>
      <ReleaseCard repo={repo} release={r} latestId={latest.data?.id}>
        {r.can_edit && (
          <>
            <Uploader repo={repo} release={r} />
            <div className="flex flex-wrap gap-2 border-t pt-4">
              <ButtonLink variant="outline" size="sm" to={`/${repo.full_path}/releases/edit/${encodeURIComponent(r.id)}`}>
                <Pencil className="h-4 w-4" /> Edit
              </ButtonLink>
              <Button
                variant="destructive"
                size="sm"
                className="ml-auto"
                disabled={remove.isPending}
                onClick={() => window.confirm(`Delete the release ${r.name || r.tag_name}? Its tag stays; its files are deleted.`) && remove.mutate()}
              >
                <Trash2 className="h-4 w-4" /> Delete release
              </Button>
            </div>
            <ErrorText error={remove.error} />
          </>
        )}
      </ReleaseCard>
    </div>
  );
}

type Upload = { key: string; name: string; progress: number; error?: string };

// Uploader attaches files to a saved release, one request per file, with
// their progress. Files over the server's limit are refused here.
function Uploader({ repo, release }: { repo: Repository; release: Release }) {
  const qc = useQueryClient();
  const instance = useInstance();
  const input = useRef<HTMLInputElement>(null);
  const [uploads, setUploads] = useState<Upload[]>([]);
  const limit = instance.data?.max_asset_size;
  const set = (key: string, change: Partial<Upload>) => setUploads((us) => us.map((u) => (u.key === key ? { ...u, ...change } : u)));

  const start = (files: FileList | null) => {
    for (const file of Array.from(files ?? [])) {
      const key = `${file.name}-${Math.random()}`;
      setUploads((us) => [...us, { key, name: file.name, progress: 0 }]);
      if (limit && file.size > limit) {
        set(key, { error: `larger than the limit (${formatSize(limit)})` });
        continue;
      }
      uploadAsset(repo.full_path, release.id, file, (p) => set(key, { progress: p }))
        .then(() => {
          setUploads((us) => us.filter((u) => u.key !== key));
          qc.invalidateQueries({ queryKey: releasesKey(repo.full_path) });
        })
        .catch((e: Error) => set(key, { error: e.message }));
    }
    if (input.current) input.current.value = "";
  };

  return (
    <div className="space-y-2">
      <div
        className="flex flex-wrap items-center gap-3 rounded-md border border-dashed p-3 text-sm"
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          e.preventDefault();
          start(e.dataTransfer.files);
        }}
      >
        <Upload className="h-4 w-4 text-muted-foreground" />
        <span className="flex-1 text-muted-foreground">
          Drop files here to attach them{limit ? `, up to ${formatSize(limit)} each` : ""}.
        </span>
        <input ref={input} type="file" multiple className="hidden" aria-label="Attach files" onChange={(e) => start(e.target.files)} />
        <Button size="sm" variant="outline" onClick={() => input.current?.click()}>
          Choose files
        </Button>
      </div>
      {uploads.map((u) => (
        <div key={u.key} className="space-y-1 text-sm">
          <div className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate">{u.name}</span>
            {u.error ? (
              <span className="text-destructive">{u.error}</span>
            ) : (
              <span className="text-xs text-muted-foreground">{Math.round(u.progress * 100)}%</span>
            )}
            {u.error && (
              <Button size="sm" variant="ghost" onClick={() => setUploads((us) => us.filter((x) => x.key !== u.key))}>
                Dismiss
              </Button>
            )}
          </div>
          {!u.error && (
            <div className="h-1.5 overflow-hidden rounded bg-muted" role="progressbar" aria-label={`Uploading ${u.name}`} aria-valuenow={Math.round(u.progress * 100)}>
              <div className="h-full bg-primary transition-all" style={{ width: `${u.progress * 100}%` }} />
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

export function NewReleasePage() {
  const repo = useRepo();
  return <ReleaseForm repo={repo} />;
}

export function EditReleasePage() {
  const repo = useRepo();
  const id = useParams().id ?? "";
  const release = useRelease(repo.full_path, id);
  if (release.error instanceof ApiError && release.error.status === 404) return <NotFoundPage />;
  if (release.isError) return <Alert variant="destructive">{release.error.message}</Alert>;
  if (!release.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!release.data.can_edit) return <Alert variant="destructive">Only developers and up can edit releases.</Alert>;
  return <ReleaseForm repo={repo} release={release.data} />;
}

// The editor: for a new release, an existing tag or a new one (created
// from a branch when published); a published release keeps its tag.
function ReleaseForm({ repo, release }: { repo: Repository; release?: Release }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const refs = useRefs(repo.full_path);
  const [tag, setTag] = useState(release?.tag_name ?? "");
  const [target, setTarget] = useState(release?.target_commitish ?? "");
  const [title, setTitle] = useState(release?.name ?? "");
  const [body, setBody] = useState(release?.body ?? "");
  const [prerelease, setPrerelease] = useState(release?.prerelease ?? false);
  const [preview, setPreview] = useState(false);
  const published = release && !release.draft;
  const tags = refs.data?.tags.map((t) => t.name) ?? [];
  const newTag = tag.trim() !== "" && refs.data !== undefined && !tags.includes(tag.trim());
  const branch = target || refs.data?.default_branch || "";

  const notes = useMutation({
    mutationFn: () =>
      api<{ name: string; body: string }>("POST", `${releasesBase(repo.full_path)}/generate-notes`, {
        tag_name: tag.trim(),
        target_commitish: newTag ? branch : "",
      }),
    onSuccess: (n) => {
      setBody(body.trim() ? `${body.trimEnd()}\n\n${n.body}` : n.body);
      if (!title.trim()) setTitle(n.name);
    },
  });
  const save = useMutation({
    mutationFn: (draft: boolean) => {
      const fields = { tag_name: tag.trim(), target_commitish: newTag ? branch : "", name: title, body, prerelease, draft };
      return release
        ? api<Release>("PATCH", `${releasesBase(repo.full_path)}/${release.id}`, published ? { name: title, body, prerelease } : fields)
        : api<Release>("POST", releasesBase(repo.full_path), fields);
    },
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: releasesKey(repo.full_path) });
      navigate(releasePath(repo.full_path, r.tag_name));
    },
  });

  return (
    <Card className="max-w-3xl space-y-4 p-5">
      <h2 className="text-lg font-semibold">{release ? "Edit release" : "New release"}</h2>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="release-tag">Tag</Label>
          <Input
            id="release-tag"
            list="release-tags"
            placeholder="v1.0.0"
            value={tag}
            onChange={(e) => setTag(e.target.value)}
            disabled={published}
            className="font-mono"
          />
          <datalist id="release-tags">
            {tags.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
          {!published && (
            <p className="text-xs text-muted-foreground">
              {newTag ? "A new tag, created when the release is published." : tag.trim() ? "An existing tag." : "Pick a tag, or type a new one."}
            </p>
          )}
        </div>
        {newTag && !published && (
          <div className="space-y-1.5">
            <Label htmlFor="release-target" className="block">
              Create the tag from
            </Label>
            <Select id="release-target" className="w-full" value={branch} onChange={(e) => setTarget(e.target.value)}>
              {refs.data?.branches.map((b) => (
                <option key={b.name} value={b.name}>
                  {b.name}
                </option>
              ))}
            </Select>
          </div>
        )}
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="release-title">Title</Label>
        <Input id="release-title" placeholder={tag || "Release title"} value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <Label htmlFor="release-body">Release notes</Label>
          <span className="ml-auto flex gap-1">
            <Button type="button" size="sm" variant={preview ? "ghost" : "outline"} onClick={() => setPreview(false)}>
              Write
            </Button>
            <Button type="button" size="sm" variant={preview ? "outline" : "ghost"} onClick={() => setPreview(true)}>
              Preview
            </Button>
            <Button type="button" size="sm" variant="ghost" disabled={!tag.trim() || notes.isPending} onClick={() => notes.mutate()}>
              Generate notes
            </Button>
          </span>
        </div>
        {preview ? (
          <div className="min-h-40 rounded-md border p-3">
            {body.trim() ? <Markdown text={body} fullPath={repo.full_path} /> : <p className="text-sm text-muted-foreground">Nothing to preview.</p>}
          </div>
        ) : (
          <Textarea id="release-body" className="min-h-40" placeholder="Markdown, @mentions and #123 work." value={body} onChange={(e) => setBody(e.target.value)} />
        )}
      </div>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-1" checked={prerelease} onChange={(e) => setPrerelease(e.target.checked)} />
        <span>
          This is a pre-release
          <span className="block text-xs text-muted-foreground">Pre-releases are never shown as the latest release.</span>
        </span>
      </label>
      {!release && <p className="text-xs text-muted-foreground">Files can be attached once the release is saved, as a draft or published.</p>}
      <ErrorText error={save.error ?? notes.error} />
      <div className="flex flex-wrap gap-2">
        {published ? (
          <Button onClick={() => save.mutate(false)} disabled={save.isPending}>
            Update release
          </Button>
        ) : (
          <>
            <Button onClick={() => save.mutate(false)} disabled={!tag.trim() || save.isPending}>
              Publish release
            </Button>
            <Button variant="outline" onClick={() => save.mutate(true)} disabled={!tag.trim() || save.isPending}>
              Save draft
            </Button>
          </>
        )}
        <Button variant="ghost" onClick={() => navigate(release ? releasePath(repo.full_path, release.tag_name) : `/${repo.full_path}/releases`)}>
          Cancel
        </Button>
      </div>
    </Card>
  );
}
