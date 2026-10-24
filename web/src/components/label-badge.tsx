import type { Label } from "@/lib/pulls";
import { textColor } from "@/lib/pulls";

export function LabelBadge({ label }: { label: Label }) {
  return (
    <span
      title={label.description || undefined}
      className="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium"
      style={{ background: label.color, color: textColor(label.color) }}
    >
      {label.name}
    </span>
  );
}
