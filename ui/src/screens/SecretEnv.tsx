import { useState } from "react";
import { apiList } from "../api/client";
import type { Application, ComponentSecretEnv, Condition, Secret } from "../api/types";
import { SelectField, TextField } from "../components/Field";
import { Icon } from "../components/Icon";
import { StatusText, findCondition, type StatusKind } from "../components/Status";
import { usePoll } from "../hooks/usePoll";
import { Link } from "../lib/router";
import { mayBeInScope, nextRowKey, validateDraft, type SecretEnvDraft, type SecretEnvErrors, type SecretEnvRow } from "../lib/secretEnv";

// Secret variables (spec 026) on the Component and Deployment forms: a
// table of what's named (variable, Secret, key) and a form that adds one.
// Only names pass through here. No value is ever fetched: the console
// never calls /v1/secrets/{id}/value (ui/CLAUDE.md).

const secretPath = (id: string) => `/secrets/${encodeURIComponent(id)}`;

/** The Secret's name, linked to its page; its ID when the caller can't list it. */
function SecretName({ id, known }: { id: string; known: Secret[] | undefined }) {
  const s = known?.find((x) => x.metadata.id === id);
  return s ? (
    <Link to={secretPath(id)}>{s.metadata.name}</Link>
  ) : (
    <span className="mono" translate="no">
      {`Secret ${id}`}
    </span>
  );
}

/** The Secrets the caller can list, and the Applications to tell which may be in scope. */
function useSecretChoices() {
  const secrets = usePoll((s) => apiList<Secret>("/v1/secrets", s), 60_000, "secretenv-secrets");
  const apps = usePoll((s) => apiList<Application>("/v1/applications", s), 60_000, "secretenv-apps");
  return { secrets, apps };
}

/** A read-only table of secret variables (variable, Secret, key). */
export function SecretEnvTable({ list, caption }: { list: ComponentSecretEnv[]; caption: string }) {
  const { secrets } = useSecretChoices();
  return (
    <div className="table-wrap">
      <table className="table">
        <caption className="visually-hidden">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">Variable</th>
            <th scope="col">Secret</th>
            <th scope="col">Key</th>
          </tr>
        </thead>
        <tbody>
          {list.map((e) => (
            <tr key={e.env}>
              <td className="mono" translate="no">
                {e.env}
              </td>
              <td>
                <SecretName id={e.secret} known={secrets.data} />
              </td>
              <td className="mono" translate="no">
                {e.key}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * SecretEnvEditor edits one list of secret variables inside a form: the
 * table with Remove, and an add form that picks a Secret from those in
 * scope, then takes the key and the variable's name. Changing an entry is
 * removing it and adding it again. taken names the variables the server
 * would refuse as clashes, with what they are; listErrors are the clashes
 * found at submit, by variable.
 */
export function SecretEnvEditor({
  idPrefix,
  legend,
  hint,
  rows,
  onChange,
  taken,
  projectId,
  listErrors,
}: {
  idPrefix: string;
  legend: string;
  hint: string;
  rows: SecretEnvRow[];
  onChange: (rows: SecretEnvRow[]) => void;
  taken: Map<string, string>;
  projectId: string | undefined;
  listErrors: Record<string, string>;
}) {
  const { secrets, apps } = useSecretChoices();
  const [draft, setDraft] = useState<SecretEnvDraft>({ secret: "", key: "", env: "" });
  const [errors, setErrors] = useState<SecretEnvErrors>({});
  const known = secrets.data ?? [];
  const choices = known.filter((s) => mayBeInScope(s, projectId, apps.data));

  function add() {
    const e = validateDraft(draft, rows, taken);
    setErrors(e);
    if (Object.keys(e).length > 0) return;
    onChange([...rows, { env: draft.env.trim(), secret: draft.secret, key: draft.key, rowKey: nextRowKey() }]);
    setDraft({ secret: "", key: "", env: "" });
  }

  return (
    <fieldset>
      <legend>{legend}</legend>
      <p className="field-hint">{hint}</p>
      <div className="rows">
        {rows.length === 0 ? (
          <p className="field-hint">None yet.</p>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <caption className="visually-hidden">{legend}</caption>
              <thead>
                <tr>
                  <th scope="col">Variable</th>
                  <th scope="col">Secret</th>
                  <th scope="col">Key</th>
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.rowKey}>
                    <td>
                      <span className="mono" translate="no">
                        {r.env}
                      </span>
                      {listErrors[r.env] && (
                        <span id={`${idPrefix}-${r.rowKey}-error`} className="field-error">
                          {" "}
                          {listErrors[r.env]}
                        </span>
                      )}
                    </td>
                    <td>
                      <SecretName id={r.secret} known={known} />
                    </td>
                    <td className="mono" translate="no">
                      {r.key}
                    </td>
                    <td>
                      <button type="button" className="button" aria-label={`Remove secret variable ${r.env} from this form`} onClick={() => onChange(rows.filter((x) => x.rowKey !== r.rowKey))}>
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="row">
          <SelectField
            id={`${idPrefix}-secret`}
            label="Secret"
            value={draft.secret}
            onChange={(e) => setDraft({ ...draft, secret: e.target.value })}
            options={[{ value: "", label: "Choose a Secret" }, ...choices.map((s) => ({ value: s.metadata.id, label: s.metadata.name }))]}
            {...(errors.secret ? { error: errors.secret } : {})}
          />
          <TextField id={`${idPrefix}-key`} label="Key" mono value={draft.key} onChange={(e) => setDraft({ ...draft, key: e.target.value })} {...(errors.key ? { error: errors.key } : {})} />
          <TextField id={`${idPrefix}-env`} label="Variable" mono value={draft.env} onChange={(e) => setDraft({ ...draft, env: e.target.value })} {...(errors.env ? { error: errors.env } : {})} />
          <button type="button" className="button" onClick={add}>
            <Icon name="plus" /> Add secret variable
          </button>
        </div>
        <p className="field-hint">
          The list offers the Secrets you can see in this Project or above it. Naming one needs permission to read its value, since the workload gets it; the
          server checks that on save. The key is one key of the Secret's value, typed here because values are never shown.
        </p>
        {secrets.error && <p className="field-hint">The list of Secrets couldn't be read, so none can be added now. Those already named are kept.</p>}
      </div>
    </fieldset>
  );
}

// SecretEnvDelivered's reasons (core/reconcile/secretenv.go) as the
// console's status words.
const waitingReasons = new Set(["ProviderNotReady", "SecretEnvPending"]);

/** Whether a Deployment's secret variables reached its workload (SecretEnvDelivered). */
export function SecretEnvDelivery({ conditions, count }: { conditions: Condition[] | undefined; count: number }) {
  const c = findCondition(conditions, "SecretEnvDelivered");
  const n = `${count} secret variable${count === 1 ? "" : "s"}`;
  if (!c) return <StatusText kind="unknown">{`${n}, not checked yet`}</StatusText>;
  const kind: StatusKind = c.status === "True" ? "ok" : waitingReasons.has(c.reason ?? "") || c.status === "Unknown" ? "progress" : "error";
  const word = kind === "ok" ? "delivered" : kind === "progress" ? "waiting" : "not delivered";
  const why = c.status === "True" ? "" : `: ${c.message || c.reason || "no reason given"}`;
  return <StatusText kind={kind}>{`${n}, ${word}${why}`}</StatusText>;
}
