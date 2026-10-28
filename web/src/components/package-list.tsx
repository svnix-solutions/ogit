import { Box, Lock, Package as PackageIcon } from "lucide-react";
import { Link } from "react-router";
import { relativeTime } from "@/lib/browse";
import { packagePath, type Package } from "@/lib/packages";
import { Badge } from "./ui/badge";
import { Card } from "./ui/card";

export const typeLabels: Record<string, string> = { container: "Container", generic: "Generic", npm: "npm", go: "Go" };

export function PackageTypeIcon({ type, className }: { type: string; className?: string }) {
  return type === "container" ? <Box aria-label="container" className={className} /> : <PackageIcon aria-label={type} className={className} />;
}

// PackageList lists packages with their type and visibility.
export function PackageList({ packages, showNamespace }: { packages: Package[]; showNamespace?: boolean }) {
  return (
    <Card className="divide-y">
      {packages.map((p) => (
        <div key={p.id} className="flex items-start gap-3 px-4 py-3">
          <PackageTypeIcon type={p.type} className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <Link to={packagePath(p)} className="font-medium text-primary hover:underline">
                {showNamespace && <span className="font-normal">{p.namespace} / </span>}
                {p.name}
              </Link>
              <Badge>{typeLabels[p.type] ?? p.type}</Badge>
              {!p.public && (
                <Badge>
                  <Lock className="h-3 w-3" /> private
                </Badge>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {p.repository && <>from {p.repository} · </>}updated {relativeTime(p.updated_at)}
            </p>
          </div>
        </div>
      ))}
    </Card>
  );
}

