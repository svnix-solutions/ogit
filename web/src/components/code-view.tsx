import { useEffect, useState } from "react";
import { useLocation } from "react-router";
import { highlightLines } from "@/lib/highlight";
import { cn } from "@/lib/utils";

// CodeView shows text with line numbers and syntax highlighting. Lines are
// anchors (#L12), and the targeted line is marked.
export function CodeView({ path, content }: { path: string; content: string }) {
  const plain = content.replace(/\n$/, "").split("\n");
  const [html, setHtml] = useState<string[] | undefined>();
  const { hash } = useLocation();
  const target = /^#L(\d+)$/.exec(hash)?.[1];

  useEffect(() => {
    let live = true;
    setHtml(undefined);
    // Large files stay plain: highlighting them would freeze the page.
    if (content.length < 300_000) {
      highlightLines(path, content.replace(/\n$/, "")).then((h) => live && setHtml(h));
    }
    return () => {
      live = false;
    };
  }, [path, content]);

  useEffect(() => {
    if (target) document.getElementById(`L${target}`)?.scrollIntoView({ block: "center" });
  }, [target, html]);

  return (
    <div className="overflow-x-auto font-mono text-xs leading-5">
      <table className="w-full border-collapse">
        <tbody>
          {plain.map((text, i) => (
            <tr key={i} id={`L${i + 1}`} className={cn(target === String(i + 1) && "bg-primary/10")}>
              <td className="w-px select-none px-3 text-right align-top text-muted-foreground">
                <a href={`#L${i + 1}`} className="hover:text-foreground">
                  {i + 1}
                </a>
              </td>
              {html ? (
                <td className="whitespace-pre pr-4" dangerouslySetInnerHTML={{ __html: html[i] ?? "" }} />
              ) : (
                <td className="whitespace-pre pr-4">{text}</td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
