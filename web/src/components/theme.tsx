import { Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "./ui/button";

type Theme = "system" | "light" | "dark";
const KEY = "ogit-theme";

function stored(): Theme {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    // private mode: follow the system
  }
  return "system";
}

function apply(theme: Theme) {
  if (theme === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", theme);
}

// Cycles system -> light -> dark.
export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(stored);
  useEffect(() => {
    apply(theme);
    try {
      if (theme === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, theme);
    } catch {
      // ignore
    }
  }, [theme]);
  const next: Record<Theme, Theme> = { system: "light", light: "dark", dark: "system" };
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  return (
    <Button variant="ghost" size="icon" title={`Theme: ${theme}`} aria-label={`Theme: ${theme}`} onClick={() => setTheme(next[theme])}>
      <Icon className="h-4 w-4" />
    </Button>
  );
}
