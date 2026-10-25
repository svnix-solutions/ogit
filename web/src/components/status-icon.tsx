import { CheckCircle2, CircleDot, XCircle } from "lucide-react";
import { stateLabel, type Combined, type StatusState } from "@/lib/statuses";
import { cn } from "@/lib/utils";

// StatusIcon shows a commit status state: ✓ passed, ✗ failed or errored,
// ● pending.
export function StatusIcon({ state, className, title }: { state: StatusState; className?: string; title?: string }) {
  const label = title ?? stateLabel[state];
  if (state === "success") return <CheckCircle2 role="img" aria-label={label} className={cn("h-4 w-4 shrink-0 text-green-600", className)} />;
  if (state === "failure" || state === "error") return <XCircle role="img" aria-label={label} className={cn("h-4 w-4 shrink-0 text-red-600", className)} />;
  return <CircleDot role="img" aria-label={label} className={cn("h-4 w-4 shrink-0 text-amber-500", className)} />;
}

// CombinedIcon summarises a commit's statuses ("2 of 3 checks passed").
export function CombinedIcon({ combined }: { combined: Combined | undefined }) {
  if (!combined || combined.total_count === 0) return null;
  const passed = combined.statuses.filter((s) => s.state === "success").length;
  return <StatusIcon state={combined.state} title={`${passed} of ${combined.total_count} checks passed`} />;
}
