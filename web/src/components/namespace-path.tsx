import { Link } from "react-router";
// NamespacePath links each level of a namespace path (org / team).
export function NamespacePath({ path }: { path: string }) {
  const parts = path.split("/");
  return (
    <>
      {parts.map((part, i) => (
        <span key={i}>
          {i > 0 && <span className="text-muted-foreground"> / </span>}
          <Link to={"/" + parts.slice(0, i + 1).join("/")} className="text-primary hover:underline">
            {part}
          </Link>
        </span>
      ))}
    </>
  );
}
