import { useSearchParams } from "react-router";
import { ErrorText, Section } from "@/components/section";
import { Alert } from "@/components/ui/alert";
import { Button, ButtonLink } from "@/components/ui/button";
import { relativeTime } from "@/lib/browse";
import { formatDate } from "@/lib/queries";
import { ssoStart, useIdentities, useSSOProviders, useUnlink } from "@/lib/sso";
import { confirmSudo } from "@/lib/sudo";

// SignInProviders: the providers the user signs in with, linking more
// (sudo) and unlinking them (never the last way to sign in).
export function SignInProviders() {
  const providers = useSSOProviders();
  const identities = useIdentities();
  const unlink = useUnlink();
  const [params, setParams] = useSearchParams();
  const ssoError = params.get("sso_error");
  if (!providers.data?.length && !identities.data?.length) return null;
  const linked = new Set(identities.data?.map((i) => i.provider));
  const linkable = providers.data?.filter((p) => !linked.has(p.slug)) ?? [];

  // Linking needs sudo: confirm, then go to the provider.
  async function link(slug: string) {
    if (ssoError?.startsWith("confirm it's you") && !(await confirmSudo())) return;
    window.location.assign(ssoStart(slug, "/settings", "link"));
  }

  return (
    <section id="sign-in">
      <Section title="Sign-in providers" description="Accounts elsewhere you can sign in with.">
        {ssoError && (
          <Alert variant="destructive">
            {ssoError}{" "}
            <Button size="sm" variant="ghost" onClick={() => setParams({}, { replace: true })}>
              Dismiss
            </Button>
          </Alert>
        )}
        {identities.data?.map((i) => (
          <div key={i.id} className="flex flex-wrap items-center justify-between gap-2 border-b pb-2 text-sm last:border-b-0">
            <div>
              <strong>{i.provider_name}</strong> <span className="text-muted-foreground">{i.username || i.email}</span>
              <div className="text-xs text-muted-foreground">
                Linked {formatDate(i.created_at)}
                {i.last_login_at && <> · last used {relativeTime(i.last_login_at)}</>}
              </div>
            </div>
            <Button variant="outline" size="sm" onClick={() => confirm(`Stop signing in with ${i.provider_name}?`) && unlink.mutate(i.id)} disabled={unlink.isPending}>
              Unlink
            </Button>
          </div>
        ))}
        <ErrorText error={unlink.error} />
        {linkable.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {linkable.map((p) =>
              ssoError?.startsWith("confirm it's you") ? (
                <Button key={p.slug} variant="outline" onClick={() => link(p.slug)}>
                  Link {p.name}
                </Button>
              ) : (
                <ButtonLink key={p.slug} variant="outline" to={ssoStart(p.slug, "/settings", "link")} reloadDocument>
                  Link {p.name}
                </ButtonLink>
              ),
            )}
          </div>
        )}
      </Section>
    </section>
  );
}
