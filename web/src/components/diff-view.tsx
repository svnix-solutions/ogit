import { ChevronDown, ChevronRight, Plus } from "lucide-react";
import { Fragment, useState } from "react";
import { Link } from "react-router";
import type { Diff, FileDiff } from "@/lib/browse";
import { encodePath } from "@/lib/browse";
import { cn } from "@/lib/utils";
import type { PullComment } from "@/lib/pulls";
import { relativeTime } from "@/lib/browse";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card } from "./ui/card";
import { Textarea } from "./ui/textarea";

// Inline review comments on a diff (pull requests).
export type InlineComments = {
  comments: PullComment[];
  canComment: boolean;
  onSubmit: (target: { path: string; line: number; side: "old" | "new" }, body: string) => Promise<unknown>;
};

export function DiffStats({ diff }: { diff: Diff }) {
  return (
    <p className="text-sm text-muted-foreground">
      {diff.files.length} {diff.files.length === 1 ? "file" : "files"} changed,{" "}
      <span className="text-green-600 dark:text-green-400">+{diff.additions}</span>{" "}
      <span className="text-red-600 dark:text-red-400">−{diff.deletions}</span>
      {diff.truncated && " · some files are too large to show"}
    </p>
  );
}

export function DiffView({ diff, fullPath, gitRef, inline }: { diff: Diff; fullPath: string; gitRef: string; inline?: InlineComments }) {
  return (
    <div className="space-y-4">
      <DiffStats diff={diff} />
      {diff.files.map((f) => (
        <FileDiffView key={f.old_path + "→" + f.path} file={f} fullPath={fullPath} gitRef={gitRef} inline={inline} />
      ))}
    </div>
  );
}

function FileDiffView({ file, fullPath, gitRef, inline }: { file: FileDiff; fullPath: string; gitRef: string; inline?: InlineComments }) {
  const [open, setOpen] = useState(true);
  const Chevron = open ? ChevronDown : ChevronRight;
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center gap-2 border-b bg-muted/50 px-3 py-2 text-sm">
        <button aria-label={open ? "Collapse" : "Expand"} onClick={() => setOpen(!open)}>
          <Chevron className="h-4 w-4" />
        </button>
        <span className="font-mono text-xs">
          {file.old_path && <span className="text-muted-foreground">{file.old_path} → </span>}
          {file.status === "deleted" ? (
            file.path
          ) : (
            <Link to={`/${fullPath}/blob/${encodePath(gitRef)}/${encodePath(file.path)}`} className="hover:underline">
              {file.path}
            </Link>
          )}
        </span>
        {file.status !== "modified" && <Badge>{file.status}</Badge>}
        <span className="ml-auto text-xs">
          <span className="text-green-600 dark:text-green-400">+{file.additions}</span> <span className="text-red-600 dark:text-red-400">−{file.deletions}</span>
        </span>
      </div>
      {open &&
        (file.binary ? (
          <p className="p-4 text-sm text-muted-foreground">Binary file not shown.</p>
        ) : file.truncated ? (
          <p className="p-4 text-sm text-muted-foreground">This diff is too large to show.</p>
        ) : file.hunks.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">No content changes (mode or rename only).</p>
        ) : (
          <div className="overflow-x-auto font-mono text-xs leading-5">
            <table className="w-full border-collapse">
              <tbody>
                {file.hunks.map((h, i) => (
                  <HunkRows key={i} hunk={h} path={file.path} inline={inline} />
                ))}
              </tbody>
            </table>
          </div>
        ))}
    </Card>
  );
}

function HunkRows({ hunk, path, inline }: { hunk: FileDiff["hunks"][number]; path: string; inline?: InlineComments }) {
  const [open, setOpen] = useState<string | null>(null); // "side:line" with a form open
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <>
      <tr className="bg-primary/5 text-muted-foreground">
        <td colSpan={3} className="px-3 py-0.5">
          @@ -{hunk.old_start},{hunk.old_lines} +{hunk.new_start},{hunk.new_lines} @@
        </td>
      </tr>
      {hunk.lines.map((l, i) => {
        // Comments anchor on the new side, except on deleted lines.
        const side: "old" | "new" = l.kind === "-" ? "old" : "new";
        const line = (side === "old" ? l.old : l.new) ?? 0;
        const key = `${side}:${line}`;
        const thread = inline?.comments.filter((c) => c.path === path && c.side === side && c.line === line) ?? [];
        return (
          <Fragment key={i}>
            <tr className="group" style={{ background: l.kind === "+" ? "var(--diff-add)" : l.kind === "-" ? "var(--diff-del)" : undefined }}>
              <td className="w-px select-none px-2 text-right text-muted-foreground">{l.old ?? ""}</td>
              <td className="relative w-px select-none px-2 text-right text-muted-foreground">
                {inline?.canComment && (
                  <button
                    aria-label={`Comment on ${path} line ${line}`}
                    className="absolute -left-2 top-0 hidden rounded bg-primary text-primary-foreground group-hover:block"
                    onClick={() => setOpen(key)}
                  >
                    <Plus className="h-4 w-4" />
                  </button>
                )}
                {l.new ?? ""}
              </td>
              <td className={cn("whitespace-pre pr-4")}>
                <span className="select-none text-muted-foreground">{l.kind}</span>
                {l.text}
              </td>
            </tr>
            {(thread.length > 0 || open === key) && (
              <tr>
                <td colSpan={3} className="border-y bg-card px-4 py-3 font-sans">
                  <div className="max-w-2xl space-y-3">
                    {thread.map((c) => (
                      <div key={c.id} className="text-sm">
                        <p className="text-xs text-muted-foreground">
                          <span className="font-medium text-foreground">{c.author || "ghost"}</span> {relativeTime(c.created_at)}
                        </p>
                        <p className="whitespace-pre-wrap">{c.body}</p>
                      </div>
                    ))}
                    {open === key && inline && (
                      <form
                        className="space-y-2"
                        onSubmit={async (e) => {
                          e.preventDefault();
                          setBusy(true);
                          try {
                            await inline.onSubmit({ path, line, side }, text);
                            setText("");
                            setOpen(null);
                          } finally {
                            setBusy(false);
                          }
                        }}
                      >
                        <Textarea aria-label="Inline comment" value={text} onChange={(e) => setText(e.target.value)} autoFocus required />
                        <div className="flex gap-2">
                          <Button size="sm" type="submit" disabled={busy}>
                            Comment
                          </Button>
                          <Button size="sm" variant="ghost" type="button" onClick={() => setOpen(null)}>
                            Cancel
                          </Button>
                        </div>
                      </form>
                    )}
                  </div>
                </td>
              </tr>
            )}
          </Fragment>
        );
      })}
    </>
  );
}
