import type { HTMLAttributes } from "react";
import { cn } from "@/lib/utils";

export function Alert({
  className,
  variant = "default",
  ...props
}: HTMLAttributes<HTMLDivElement> & { variant?: "default" | "destructive" }) {
  return (
    <div
      role={variant === "destructive" ? "alert" : "status"}
      className={cn(
        "rounded-md border px-3 py-2 text-sm",
        variant === "destructive" && "border-destructive/40 bg-destructive/10 text-destructive",
        className,
      )}
      {...props}
    />
  );
}
