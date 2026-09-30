import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { createToken, revokeToken } from "../../../api/endpoints";
import { keys, tokensQuery, useMe } from "../../../api/queries";
import type { CreatedToken, TokenScope } from "../../../api/types";
import { Icon } from "../../../components/Icon";
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
      className="btn"
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

function Created({ created, onDone }: { created: CreatedToken; onDone: () => void }) {
  const { data: me } = useMe();
  const command = claudeCommand(window.location.origin, created.token);
  return (
    <section className="panel callout" aria-live="polite">
      <h2>Token “{created.item.name}” created</h2>
      <p>Copy it now. Eddy stores only a hash, so it cannot be shown again.</p>
      <div className="secret">
        <code>{created.token}</code>
        <CopyButton text={created.token} label="Copy token" />
      </div>
      {me?.features.mcp && (
        <>
          <h2>Connect Claude Code</h2>
          <p>
            Run this in a terminal to add Eddy as an MCP server. The token only works as a bearer header on
            /mcp.
          </p>
          <div className="secret">
            <pre className="snippet">{command}</pre>
            <CopyButton text={command} label="Copy command" />
          </div>
        </>
      )}
      <div className="row-btns">
        <button type="button" className="btn sm" onClick={onDone}>
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
    <Screen crumbs={["Settings", "Access tokens"]}>
      <h1>Access tokens</h1>
      <p className="page-sub">
        Personal access tokens let MCP clients such as Claude Code act as you. They carry your groups at
        creation time and always expire.
      </p>
      {!me?.features.mcp && (
        <div className="banner warn" role="status">
          <Icon name="alert" />
          MCP is turned off on this hub
          <span>Tokens can be created, but /mcp will refuse them until an administrator enables it.</span>
        </div>
      )}
      {created ? (
        <Created created={created} onDone={() => setCreated(null)} />
      ) : (
        <form
          className="panel"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <h2>New token</h2>
          <div className="form-row">
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
          <fieldset className="field plain">
            <legend>Scopes</legend>
            <div className="radio-row">
              <label className="radio">
                <input
                  type="radio"
                  name="scope"
                  checked={scope === "read"}
                  onChange={() => setScope("read")}
                />
                Read: resources, events, threads
              </label>
              <label className="radio">
                <input
                  type="radio"
                  name="scope"
                  checked={scope === "operate"}
                  onChange={() => setScope("operate")}
                  disabled={!me?.features.mcpWrites}
                />
                Read and operate: reconcile, suspend, resume
              </label>
            </div>
          </fieldset>
          <div className="row-btns">
            <button type="submit" className="btn primary" disabled={!name.trim() || create.isPending}>
              <Icon name="key" />
              Create token
            </button>
          </div>
        </form>
      )}
      <h3 className="section-title">Your tokens</h3>
      {tokens.isPending && <p className="muted">Loading…</p>}
      {tokens.error && <p className="error-text">{tokens.error.message}</p>}
      {tokens.data && tokens.data.length === 0 && <p className="muted">You have no tokens.</p>}
      {tokens.data && tokens.data.length > 0 && (
        <table className="dtable">
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
                  <b>{t.name}</b> <span className="muted mono">{t.id}</span>
                </td>
                <td>
                  {t.scopes.map((s) => (
                    <span key={s} className={`badge${s === "operate" ? " accent" : ""}`}>
                      {s}
                    </span>
                  ))}
                </td>
                <td>{dateTime(t.createdAt)}</td>
                <td>{dateTime(t.expiresAt)}</td>
                <td className="muted">{t.lastUsedAt ? ago(t.lastUsedAt) : "never"}</td>
                <td className="num">
                  <button
                    type="button"
                    className="btn sm"
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
        </table>
      )}
    </Screen>
  );
}
