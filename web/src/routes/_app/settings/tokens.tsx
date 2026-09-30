import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { createToken, revokeToken } from "../../../api/endpoints";
import { keys, tokensQuery, useMe } from "../../../api/queries";
import type { CreatedToken, TokenScope } from "../../../api/types";
import { DataTable } from "../../../components/DataTable";
import { Icon } from "../../../components/Icon";
import { PageHead } from "../../../components/PageHead";
import { Screen } from "../../../components/Screen";
import { useToast } from "../../../components/Toasts";
import { ago, dateTime } from "../../../lib/format";
import { claudeCommand } from "../../../lib/mcp";

export const Route = createFileRoute("/_app/settings/tokens")({
  component: TokensPage,
});

const TTLS = [
  { value: "24h", label: "1 day" },
  { value: "168h", label: "7 days" },
  { value: "720h", label: "30 days" },
  { value: "2160h", label: "90 days" },
];

function CopyButton({ text, label }: { text: string; label: string }) {
  const toast = useToast();
  return (
    <button
      type="button"
      className="btn shrink-0"
      aria-label={label}
      onClick={() =>
        navigator.clipboard.writeText(text).then(
          () => toast("Copied to the clipboard", "ok"),
          () => toast("Couldn't copy. Select the text and copy it by hand.", "bad"),
        )
      }
    >
      <Icon name="copy" />
      Copy
    </button>
  );
}

const PANEL = "flex flex-col gap-3.5 rounded-card border border-line bg-surface p-[18px]";
const H2 = "text-15 font-semibold";
const P = "text-13 text-ink-2";
const SNIPPET =
  "m-0 min-w-0 flex-1 overflow-x-auto rounded-control bg-code-bg px-3 py-2.5 font-mono text-12-5 whitespace-pre text-code-ink";

interface ScopeOption {
  value: TokenScope;
  title: string;
  description: string;
  icon: "shield" | "sync";
}

const SCOPES: ScopeOption[] = [
  {
    value: "read",
    title: "Read",
    description: "List resources, read events, YAML and threads, and post to threads.",
    icon: "shield",
  },
  {
    value: "operate",
    title: "Read and operate",
    description: "Everything in Read, plus reconcile, suspend and resume, within your RBAC.",
    icon: "sync",
  },
];

/** A radio card: a small radio on the left, then a title and a description, filling its box. */
function ScopeCard({
  option,
  checked,
  disabled,
  hint,
  onChange,
}: {
  option: ScopeOption;
  checked: boolean;
  disabled?: boolean;
  hint?: string;
  onChange: () => void;
}) {
  return (
    <label
      className={`flex h-full min-w-0 items-start gap-3 rounded-xl border px-3.5 py-3 has-focus-visible:outline-2 has-focus-visible:outline-offset-2 has-focus-visible:outline-c ${checked ? "border-c bg-c-soft" : "border-line-strong bg-surface hover:border-ink-3"} ${disabled ? "cursor-not-allowed opacity-50" : ""}`}
    >
      <input
        type="radio"
        name="scope"
        value={option.value}
        checked={checked}
        disabled={disabled}
        onChange={onChange}
        className="peer mt-0.5 size-4 shrink-0 accent-(--c) focus-visible:outline-none"
      />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex items-center gap-1.5 text-13-5 font-semibold text-ink">
          <Icon name={option.icon} className="size-3.5 text-ink-3" />
          {option.title}
        </span>
        <span className="text-12-5 leading-snug text-ink-3">{option.description}</span>
        {hint && <span className="text-12 text-warn">{hint}</span>}
      </span>
    </label>
  );
}

function Created({ created, onDone }: { created: CreatedToken; onDone: () => void }) {
  const { data: me } = useMe();
  const command = claudeCommand(window.location.origin, created.token);
  return (
    <section className={`${PANEL} border-ok/40 bg-ok/6`} aria-live="polite">
      <h2 className={H2}>Token “{created.item.name}” created</h2>
      <p className={P}>Copy it now. Eddy stores only a hash, so it cannot be shown again.</p>
      <div className="flex items-stretch gap-2">
        <code className={SNIPPET}>{created.token}</code>
        <CopyButton text={created.token} label="Copy token" />
      </div>
      {me?.features.mcp && (
        <>
          <h2 className={H2}>Connect Claude Code</h2>
          <p className={P}>
            Run this in a terminal to add Eddy as an MCP server. The token only works as a bearer header on
            /mcp.
          </p>
          <div className="flex items-stretch gap-2">
            <pre className={SNIPPET}>{command}</pre>
            <CopyButton text={command} label="Copy command" />
          </div>
        </>
      )}
      <div className="flex gap-2">
        <button type="button" className="btn btn-sm" onClick={onDone}>
          Done
        </button>
      </div>
    </section>
  );
}

function TokensPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const { data: me } = useMe();
  const tokens = useQuery(tokensQuery);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<TokenScope>("read");
  const [ttl, setTtl] = useState("720h");
  const [created, setCreated] = useState<CreatedToken | null>(null);

  const create = useMutation({
    mutationFn: () =>
      createToken({ name: name.trim(), scopes: scope === "operate" ? ["read", "operate"] : ["read"], ttl }),
    onSuccess: (res) => {
      setCreated(res);
      setName("");
      void qc.invalidateQueries({ queryKey: keys.tokens });
    },
    onError: (err) => toast(`Couldn't create the token: ${err.message}`, "bad"),
  });
  const revoke = useMutation({
    mutationFn: revokeToken,
    onSuccess: () => {
      toast("Token revoked", "ok");
      void qc.invalidateQueries({ queryKey: keys.tokens });
    },
    onError: (err) => toast(`Couldn't revoke: ${err.message}`, "bad"),
  });

  return (
    <Screen crumbs={["Settings", "Access tokens"]} title="tokens" width="narrow">
      <PageHead title="Access tokens">
        Personal access tokens let MCP clients such as Claude Code act as you. They carry your groups at
        creation time and always expire.
      </PageHead>
      {!me?.features.mcp && (
        <div
          className="flex items-center gap-2 rounded-xl border border-warn/40 bg-warn/12 px-4 py-2.5 text-12-5 font-semibold text-warn"
          role="status"
        >
          <Icon name="alert" />
          MCP is turned off on this hub
          <span className="font-normal text-ink-2">
            Tokens can be created, but /mcp will refuse them until an administrator enables it.
          </span>
        </div>
      )}
      {created ? (
        <Created created={created} onDone={() => setCreated(null)} />
      ) : (
        <form
          className={PANEL}
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <h2 className={H2}>New token</h2>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
            <label className="field">
              Name
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="laptop claude code"
                maxLength={100}
                required
              />
            </label>
            <label className="field">
              Expires after
              <select value={ttl} onChange={(e) => setTtl(e.target.value)}>
                {TTLS.map((t) => (
                  <option key={t.value} value={t.value}>
                    {t.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <fieldset className="m-0 flex flex-col gap-1.5 border-0 p-0">
            <legend className="mb-1.5 p-0 text-13 text-ink-2">Scopes</legend>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {SCOPES.map((o) => (
                <ScopeCard
                  key={o.value}
                  option={o}
                  checked={scope === o.value}
                  disabled={o.value === "operate" && !me?.features.mcpWrites}
                  hint={
                    o.value === "operate" && !me?.features.mcpWrites
                      ? "Writes over MCP are turned off on this hub."
                      : undefined
                  }
                  onChange={() => setScope(o.value)}
                />
              ))}
            </div>
          </fieldset>
          <div className="flex gap-2">
            <button type="submit" className="btn btn-primary" disabled={!name.trim() || create.isPending}>
              <Icon name="key" />
              Create token
            </button>
          </div>
        </form>
      )}
      <h3 className="mt-2 text-14 font-semibold">Your tokens</h3>
      {tokens.isPending && <p className="text-ink-3">Loading…</p>}
      {tokens.error && <p className="text-12-5 text-bad">{tokens.error.message}</p>}
      {tokens.data && tokens.data.length === 0 && <p className="text-ink-3">You have no tokens.</p>}
      {tokens.data && tokens.data.length > 0 && (
        <DataTable>
          <thead>
            <tr>
              <th>Name</th>
              <th>Scopes</th>
              <th>Created</th>
              <th>Expires</th>
              <th>Last used</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {tokens.data.map((t) => (
              <tr key={t.id}>
                <td>
                  <b className="font-semibold">{t.name}</b>{" "}
                  <span className="font-mono text-12 text-ink-3">{t.id}</span>
                </td>
                <td>
                  <span className="inline-flex gap-1">
                    {t.scopes.map((s) => (
                      <span key={s} className={`badge${s === "operate" ? " badge-accent" : ""}`}>
                        {s}
                      </span>
                    ))}
                  </span>
                </td>
                <td>{dateTime(t.createdAt)}</td>
                <td>{dateTime(t.expiresAt)}</td>
                <td className="text-ink-3">{t.lastUsedAt ? ago(t.lastUsedAt) : "never"}</td>
                <td className="text-right">
                  <button
                    type="button"
                    className="btn btn-sm"
                    onClick={() => revoke.mutate(t.id)}
                    disabled={revoke.isPending}
                    aria-label={`Revoke ${t.name}`}
                  >
                    Revoke
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </DataTable>
      )}
    </Screen>
  );
}
