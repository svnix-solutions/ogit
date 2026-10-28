import { ForksPage } from "@/components/forks";
import { useRepo } from "@/components/repo-layout";

export function RepoForksPage() {
  return <ForksPage repo={useRepo()} />;
}
