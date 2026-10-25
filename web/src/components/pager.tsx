import { Button } from "./ui/button";

// Pager moves through a list page by page. With total it shows the range;
// without, Next is enabled while pages come back full.
export function Pager({ page, perPage, count, total, onPage }: { page: number; perPage: number; count: number; total?: number; onPage: (p: number) => void }) {
  const hasNext = total !== undefined ? (page + 1) * perPage < total : count === perPage;
  if (page === 0 && !hasNext) return null;
  const first = page * perPage + 1;
  return (
    <div className="flex items-center justify-between gap-2 text-sm text-muted-foreground">
      <span>
        {count > 0 && `${first}–${first + count - 1}`}
        {total !== undefined && ` of ${total}`}
      </span>
      <span className="flex gap-2">
        <Button variant="outline" size="sm" disabled={page === 0} onClick={() => onPage(page - 1)}>
          Previous
        </Button>
        <Button variant="outline" size="sm" disabled={!hasNext} onClick={() => onPage(page + 1)}>
          Next
        </Button>
      </span>
    </div>
  );
}

// pageParam reads ?page= (1-based in the URL, 0-based here).
export function pageParam(params: URLSearchParams): number {
  return Math.max(0, (Number(params.get("page")) || 1) - 1);
}
