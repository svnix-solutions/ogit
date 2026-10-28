import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { Button } from "./ui/button";

export function CopyField({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-center gap-2">
      <input
        readOnly
        aria-label={label}
        value={value}
        onFocus={(e) => e.target.select()}
        className="h-9 min-w-0 flex-1 rounded-md border bg-muted px-3 font-mono text-xs"
      />
      <Button
        type="button"
        variant="outline"
        size="icon"
        aria-label={`Copy ${label}`}
        onClick={() =>
          navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
      </Button>
    </div>
  );
}
