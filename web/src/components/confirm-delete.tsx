import { useState } from "react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

// A destructive action that needs a word typed first (the API asks for it
// too where it matters, e.g. ?confirm=<slug>).
export function ConfirmDelete({
  word,
  action,
  onConfirm,
  pending,
}: {
  word: string;
  action: string;
  onConfirm: () => void;
  pending?: boolean;
}) {
  const [typed, setTyped] = useState("");
  return (
    <div className="space-y-2">
      <Label htmlFor="confirm-delete">
        Type <code className="rounded bg-muted px-1">{word}</code> to confirm
      </Label>
      <div className="flex gap-2">
        <Input id="confirm-delete" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
        <Button variant="destructive" disabled={typed !== word || pending} onClick={onConfirm}>
          {action}
        </Button>
      </div>
    </div>
  );
}
