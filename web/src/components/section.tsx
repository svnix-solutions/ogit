import type { ReactNode } from "react";
import { Card } from "./ui/card";

export function Section({ title, description, children, danger }: { title: string; description?: ReactNode; children: ReactNode; danger?: boolean }) {
  return (
    <Card className={danger ? "border-destructive/40" : undefined}>
      <div className="border-b px-5 py-3">
        <h2 className={danger ? "font-semibold text-destructive" : "font-semibold"}>{title}</h2>
        {description && <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>}
      </div>
      <div className="space-y-4 p-5">{children}</div>
    </Card>
  );
}

export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <p role="alert" className="text-sm text-destructive">
      {error instanceof Error ? error.message : String(error)}
    </p>
  );
}
