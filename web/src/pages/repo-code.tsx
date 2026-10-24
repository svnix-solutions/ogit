import { File, FileText, Folder, FolderGit2, History, Link2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { CodeView } from "@/components/code-view";
import { Breadcrumbs, RefPicker, useRepo } from "@/components/repo-layout";
import { Alert } from "@/components/ui/alert";
import { ButtonLink } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ApiError } from "@/lib/api";
import {
  encodePath,
  formatSize,
  joinSpec,
  rawURL,
  relativeTime,
  shortHash,
  summary,
  useBlame,
  useBlob,
  useTree,
  type Commit,
} from "@/lib/browse";
import { renderMarkdown } from "@/lib/markdown";
import { cn } from "@/lib/utils";

function notFound(error: unknown, what: string) {
  if (error instanceof ApiError && error.status === 404) return <Alert variant="destructive">{error.type === "REF_NOT_FOUND" ? "No such branch, tag or commit." : `No such ${what}.`}</Alert>;
  return <Alert variant="destructive">{error instanceof Error ? error.message : String(error)}</Alert>;
}

function LastCommit({ commit }: { commit: Commit }) {
  const repo = useRepo();
  return (
    <div className="flex items-center gap-3 border-b bg-muted/40 px-4 py-2 text-sm">
      <span className="font-medium">{commit.author.name}</span>
      <Link to={`/${repo.full_path}/commit/${commit.hash}`} className="min-w-0 flex-1 truncate text-muted-foreground hover:text-foreground hover:underline">
        {summary(commit.message)}
      </Link>
      <Link to={`/${repo.full_path}/commit/${commit.hash}`} className="font-mono text-xs text-muted-foreground hover:underline">
        {shortHash(commit.hash)}
      </Link>
      <span className="text-xs text-muted-foreground">{relativeTime(commit.committer.when)}</span>
    </div>
  );
}

export function TreePage() {
  const repo = useRepo();
  const spec = useParams()["*"] ?? "";
  const tree = useTree(repo.full_path, spec);

  if (tree.isError) return notFound(tree.error, "directory");
  if (!tree.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (tree.data.empty) return <Card className="p-8 text-center text-sm text-muted-foreground">This repository is empty. Push a first commit to see it here.</Card>;
  const { ref, path, entries, commit, readme } = tree.data;
  const base = `/${repo.full_path}`;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <RefPicker current={ref} view="tree" path={path} />
        <Breadcrumbs gitRef={ref} path={path} />
        <ButtonLink variant="ghost" size="sm" className="ml-auto" to={`${base}/commits/${encodePath(joinSpec(ref, path))}`}>
          <History className="h-4 w-4" /> History
        </ButtonLink>
      </div>
      <Card className="overflow-hidden">
        {commit && <LastCommit commit={commit} />}
        <table className="w-full text-sm">
          <tbody className="divide-y">
            {path && (
              <tr>
                <td className="px-4 py-2" colSpan={2}>
                  <Link to={`${base}/tree/${encodePath(ref)}${path.includes("/") ? "/" + encodePath(path.slice(0, path.lastIndexOf("/"))) : ""}`} className="text-primary hover:underline">
                    ..
                  </Link>
                </td>
              </tr>
            )}
            {entries.map((e) => {
              const Icon = e.type === "dir" ? Folder : e.type === "submodule" ? FolderGit2 : e.type === "symlink" ? Link2 : File;
              const view = e.type === "dir" ? "tree" : "blob";
              return (
                <tr key={e.path} className="hover:bg-muted/40">
                  <td className="px-4 py-2">
                    <span className="flex items-center gap-2">
                      <Icon className={cn("h-4 w-4 shrink-0", e.type === "dir" ? "text-primary" : "text-muted-foreground")} />
                      {e.type === "submodule" ? (
                        <span>{e.name}</span>
                      ) : (
                        <Link to={`${base}/${view}/${encodePath(ref)}/${encodePath(e.path)}`} className="hover:text-primary hover:underline">
                          {e.name}
                        </Link>
                      )}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-right text-xs text-muted-foreground">{e.size !== undefined && e.type === "file" ? formatSize(e.size) : ""}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Card>
      {readme && <Readme repoPath={repo.full_path} gitRef={ref} dir={path} file={readme} />}
    </div>
  );
}

function Readme({ repoPath, gitRef, dir, file }: { repoPath: string; gitRef: string; dir: string; file: { path: string; content?: string } }) {
  const isMarkdown = /\.(md|markdown)$/i.test(file.path);
  const html = useMemo(() => (isMarkdown ? renderMarkdown(file.content ?? "", { fullPath: repoPath, ref: gitRef, dir }) : ""), [isMarkdown, file.content, repoPath, gitRef, dir]);
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 border-b px-4 py-2 text-sm font-medium">
        <FileText className="h-4 w-4" /> {file.path.split("/").pop()}
      </div>
      {isMarkdown ? <div className="markdown p-6" dangerouslySetInnerHTML={{ __html: html }} /> : <pre className="overflow-x-auto p-6 text-sm whitespace-pre-wrap">{file.content}</pre>}
    </Card>
  );
}

export function BlobPage() {
  const repo = useRepo();
  const spec = useParams()["*"] ?? "";
  const blob = useBlob(repo.full_path, spec);
  const [source, setSource] = useState(false);

  if (blob.isError) return notFound(blob.error, "file");
  if (!blob.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { ref, file } = blob.data;
  const base = `/${repo.full_path}`;
  const dir = file.path.includes("/") ? file.path.slice(0, file.path.lastIndexOf("/")) : "";
  const markdown = /\.(md|markdown)$/i.test(file.path) && !file.binary;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <RefPicker current={ref} view="blob" path={file.path} />
        <Breadcrumbs gitRef={ref} path={file.path} />
      </div>
      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/40 px-4 py-2 text-sm">
          <span className="text-muted-foreground">{formatSize(file.size)}</span>
          {markdown && (
            <button className="text-primary hover:underline" onClick={() => setSource(!source)}>
              {source ? "Preview" : "Source"}
            </button>
          )}
          <span className="ml-auto flex gap-1">
            <ButtonLink variant="ghost" size="sm" to={`${base}/commits/${encodePath(joinSpec(ref, file.path))}`}>
              History
            </ButtonLink>
            {!file.binary && (
              <ButtonLink variant="ghost" size="sm" to={`${base}/blame/${encodePath(ref)}/${encodePath(file.path)}`}>
                Blame
              </ButtonLink>
            )}
            <a href={rawURL(repo.full_path, ref, file.path)} className="inline-flex h-8 items-center rounded-md px-3 text-sm font-medium hover:bg-muted">
              Raw
            </a>
          </span>
        </div>
        {file.binary ? (
          <p className="p-6 text-sm text-muted-foreground">
            Binary file. <a href={rawURL(repo.full_path, ref, file.path)} className="text-primary hover:underline">Download it</a>.
          </p>
        ) : markdown && !source ? (
          <Readme repoPath={repo.full_path} gitRef={ref} dir={dir} file={file} />
        ) : (
          <>
            {file.truncated && <Alert className="m-3">Only the first 1 MB is shown. Use Raw for the whole file.</Alert>}
            <CodeView path={file.path} content={file.content ?? ""} />
          </>
        )}
      </Card>
    </div>
  );
}

export function BlamePage() {
  const repo = useRepo();
  const spec = useParams()["*"] ?? "";
  const blame = useBlame(repo.full_path, spec);

  if (blame.error instanceof ApiError && blame.error.type === "TOO_LARGE") return <Alert>This file is binary or too large to blame.</Alert>;
  if (blame.isError) return notFound(blame.error, "file");
  if (!blame.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { ref, path, lines } = blame.data;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <RefPicker current={ref} view="blame" path={path} />
        <Breadcrumbs gitRef={ref} path={path} />
      </div>
      <Card className="overflow-x-auto font-mono text-xs leading-5">
        <table className="w-full border-collapse">
          <tbody>
            {lines.map((l, i) => {
              const first = i === 0 || lines[i - 1].hash !== l.hash;
              return (
                <tr key={i} className={cn(first && i > 0 && "border-t")}>
                  <td className="w-64 max-w-64 truncate px-3 align-top font-sans text-muted-foreground">
                    {first && (
                      <span className="flex gap-2">
                        <Link to={`/${repo.full_path}/commit/${l.hash}`} className="font-mono hover:underline">
                          {shortHash(l.hash)}
                        </Link>
                        <span className="truncate">{l.author}</span>
                        <span className="ml-auto shrink-0">{relativeTime(l.when)}</span>
                      </span>
                    )}
                  </td>
                  <td className="w-px select-none px-3 text-right text-muted-foreground">{i + 1}</td>
                  <td className="whitespace-pre pr-4">{l.text}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
