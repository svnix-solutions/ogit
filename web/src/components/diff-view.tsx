import { ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import type { Diff, FileDiff } from "@/lib/browse";
import { encodePath } from "@/lib/browse";
import { cn } from "@/lib/utils";
import { Badge } from "./ui/badge";
import { Card } from "./ui/card";

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

export function DiffView({ diff, fullPath, gitRef }: { diff: Diff; fullPath: string; gitRef: string }) {
  return (
    <div className="space-y-4">
      <DiffStats diff={diff} />
      {diff.files.map((f) => (
        <FileDiffView key={f.old_path + "→" + f.path} file={f} fullPath={fullPath} gitRef={gitRef} />
      ))}
    </div>
  );
}

function FileDiffView({ file, fullPath, gitRef }: { file: FileDiff; fullPath: string; gitRef: string }) {
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
                  <HunkRows key={i} hunk={h} />
                ))}
              </tbody>
            </table>
          </div>
        ))}
    </Card>
  );
}

function HunkRows({ hunk }: { hunk: FileDiff["hunks"][number] }) {
  return (
    <>
      <tr className="bg-primary/5 text-muted-foreground">
        <td colSpan={3} className="px-3 py-0.5">
          @@ -{hunk.old_start},{hunk.old_lines} +{hunk.new_start},{hunk.new_lines} @@
        </td>
      </tr>
      {hunk.lines.map((l, i) => (
        <tr key={i} style={{ background: l.kind === "+" ? "var(--diff-add)" : l.kind === "-" ? "var(--diff-del)" : undefined }}>
          <td className="w-px select-none px-2 text-right text-muted-foreground">{l.old ?? ""}</td>
          <td className="w-px select-none px-2 text-right text-muted-foreground">{l.new ?? ""}</td>
          <td className={cn("whitespace-pre pr-4")}>
            <span className="select-none text-muted-foreground">{l.kind}</span>
            {l.text}
          </td>
        </tr>
      ))}
    </>
  );
}
