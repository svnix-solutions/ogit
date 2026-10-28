import { api } from "./api";

// Security keys and passkeys (WebAuthn) in the browser. The server sends
// options with binary fields as base64url (go-webauthn); the browser API
// wants ArrayBuffers, and its answer goes back as JSON with base64url.

type KeyOptions = { ceremony: string; options: { publicKey: Record<string, unknown> } };

export const webauthnSupported = () => typeof window !== "undefined" && !!window.PublicKeyCredential && !!navigator.credentials;

export function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

export function toB64url(buf: ArrayBuffer | ArrayBufferView | null | undefined): string | undefined {
  if (!buf) return undefined;
  const bytes = buf instanceof ArrayBuffer ? new Uint8Array(buf) : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

type Descriptor = { id: string; type: string; transports?: string[] };
const descriptors = (list?: Descriptor[]) => list?.map((d) => ({ ...d, id: fromB64url(d.id) })) as PublicKeyCredentialDescriptor[] | undefined;

export function creationOptions(o: Record<string, unknown>): PublicKeyCredentialCreationOptions {
  const user = o.user as { id: string; name: string; displayName: string };
  return {
    ...(o as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromB64url(o.challenge as string),
    user: { ...user, id: fromB64url(user.id) },
    excludeCredentials: descriptors(o.excludeCredentials as Descriptor[] | undefined),
  };
}

export function requestOptions(o: Record<string, unknown>): PublicKeyCredentialRequestOptions {
  return {
    ...(o as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromB64url(o.challenge as string),
    allowCredentials: descriptors(o.allowCredentials as Descriptor[] | undefined),
  };
}

// credentialJSON is the answer in the form the server parses.
export function credentialJSON(c: PublicKeyCredential): Record<string, unknown> {
  const r = c.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse;
  const response: Record<string, unknown> = { clientDataJSON: toB64url(r.clientDataJSON) };
  if ("attestationObject" in r && r.attestationObject) {
    response.attestationObject = toB64url(r.attestationObject);
    if (typeof r.getTransports === "function") response.transports = r.getTransports();
  } else {
    response.authenticatorData = toB64url(r.authenticatorData);
    response.signature = toB64url(r.signature);
    response.userHandle = toB64url(r.userHandle);
  }
  return { id: c.id, rawId: toB64url(c.rawId), type: c.type, response, clientExtensionResults: c.getClientExtensionResults?.() ?? {} };
}

async function create(k: KeyOptions) {
  const cred = (await navigator.credentials.create({ publicKey: creationOptions(k.options.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new Error("The security key didn't answer.");
  return credentialJSON(cred);
}

async function get(k: KeyOptions) {
  const cred = (await navigator.credentials.get({ publicKey: requestOptions(k.options.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new Error("The security key didn't answer.");
  return { ceremony: k.ceremony, credential: credentialJSON(cred) };
}

// addKey registers a security key (or a passkey) and returns the recovery
// codes when it turned two-factor authentication on.
export async function addKey(name: string, passkey: boolean): Promise<string[]> {
  const k = await api<KeyOptions>("POST", "/user/2fa/webauthn", { passkey });
  const credential = await create(k);
  const r = await api<{ recovery_codes: string[] }>("POST", "/user/2fa/webauthn/finish", { ceremony: k.ceremony, name, passkey, credential });
  return r.recovery_codes ?? [];
}

// loginKey answers a login's second-factor challenge with a key.
export async function loginKey(challenge: string) {
  return get(await api<KeyOptions>("POST", "/auth/login/2fa/webauthn", { challenge }));
}

export async function sudoKey() {
  return get(await api<KeyOptions>("POST", "/auth/sudo/webauthn"));
}

export async function passkeyAnswer() {
  return get(await api<KeyOptions>("POST", "/auth/passkey"));
}

// keyError says what went wrong in words (the browser's errors are terse).
export function keyError(e: unknown): string {
  const name = (e as { name?: string })?.name;
  if (name === "NotAllowedError") return "The security key request was cancelled or timed out.";
  if (name === "InvalidStateError") return "This security key is already registered.";
  return (e as Error)?.message ?? String(e);
}
