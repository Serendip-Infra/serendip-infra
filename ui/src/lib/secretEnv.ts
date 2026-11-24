// Secret variables (spec 026): a variable set from one key of a Secret's
// value. The console handles names only (the Secret's ID and the key); it
// never reads a value. The rules mirror core/resource/secretenv.go, so the
// form doesn't send what the server would refuse for its shape. Whether
// the caller may read the value, and whether the Secret is in the Project
// or above it, the server decides (403 or 422).
import type { Application, ComponentSecretEnv, OwnerRef, Secret } from "../api/types";

export interface SecretEnvRow extends ComponentSecretEnv {
  /** React's key for the row. */
  rowKey: number;
}

/** core/resource MaxSecretEnv: at most this many in one list. */
export const maxSecretEnv = 64;

export const envNameRE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** A key the server accepts: 1 to 256 bytes, no control characters. */
export function secretKeyOK(k: string): boolean {
  const bytes = new TextEncoder().encode(k).length;
  return bytes >= 1 && bytes <= 256 && !/\p{Cc}/u.test(k);
}

let next = 9000;
/** A fresh row key. */
export const nextRowKey = () => next++;
export const toRows = (list: ComponentSecretEnv[] | undefined): SecretEnvRow[] => (list ?? []).map((e) => ({ env: e.env, secret: e.secret, key: e.key, rowKey: next++ }));
export const fromRows = (rows: SecretEnvRow[]): ComponentSecretEnv[] => rows.map(({ env, secret, key }) => ({ env, secret, key }));

/** What the add form holds before it becomes a row. */
export interface SecretEnvDraft {
  secret: string;
  key: string;
  env: string;
}

export type SecretEnvErrors = Partial<Record<"secret" | "key" | "env", string>>;

/**
 * Checks a draft before it's added: its own fields, a variable already in
 * the list, and a clash with taken, the names that are plain or
 * dependency variables where the server would refuse the same name (with
 * what says what they are, for the message).
 */
export function validateDraft(d: SecretEnvDraft, rows: SecretEnvRow[], taken: Map<string, string>): SecretEnvErrors {
  const errors: SecretEnvErrors = {};
  if (d.secret === "") errors.secret = "Choose a Secret.";
  if (!secretKeyOK(d.key)) errors.key = "Enter the key in the Secret's value: 1 to 256 bytes, no control characters.";
  const env = d.env.trim();
  if (!envNameRE.test(env)) errors.env = "Use letters, digits and underscores, not starting with a digit.";
  else if (rows.some((r) => r.env === env)) errors.env = "This variable is already a secret variable: remove it first to change it.";
  else if (taken.has(env)) errors.env = `${env} is ${taken.get(env)}. A variable comes from one place only.`;
  if (rows.length >= maxSecretEnv) errors.env = `At most ${maxSecretEnv} secret variables.`;
  return errors;
}

/**
 * Checks the list as it stands at submit: a clash can appear after a row
 * was added (a plain variable of the same name added later). Keyed by the
 * row's variable name.
 */
export function listErrors(rows: SecretEnvRow[], taken: Map<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) if (taken.has(r.env)) out[r.env] = `${r.env} is also ${taken.get(r.env)}: remove one of the two.`;
  return out;
}

/**
 * Whether a Secret could be named by a workload in Project projectId: the
 * server accepts one in the Project or above it. The console can rule out
 * a Secret of another Project, or of an Application in another Project;
 * anything else is offered and the server has the last word.
 */
export function mayBeInScope(s: Secret, projectId: string | undefined, apps: Application[] | undefined): boolean {
  if (!projectId) return true;
  const owner: OwnerRef | undefined = s.metadata.ownerRefs?.[0];
  if (!owner) return true;
  if (owner.kind === "Project") return owner.id === projectId;
  if (owner.kind === "Application") {
    const app = apps?.find((a) => a.metadata.id === owner.id);
    const p = app?.metadata.ownerRefs?.find((r) => r.kind === "Project")?.id;
    return !app || p === projectId;
  }
  return true;
}
