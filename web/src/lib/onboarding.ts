// Pure helpers for the "Add cluster" wizard and the Connection panel (ADR-0005):
// field validation that mirrors the hub, the colour palette, and human wording for
// checks and rejected connection attempts.

import type { AttemptReason, CheckId, CheckState, ConnectionInfo } from "../api/types";

/** A DNS-1123 label, as the hub requires for a cluster name. */
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
export const NAME_MAX = 63;
export const LABEL_MAX = 63;

/** Returns why a cluster name is invalid, or undefined when the hub will accept it. */
export function nameError(name: string): string | undefined {
  if (!name) return "A name is required.";
  if (name.length > NAME_MAX) return `Use at most ${NAME_MAX} characters.`;
  if (/[A-Z]/.test(name)) return "Use lower-case letters only.";
  if (/^-|-$/.test(name)) return "Start and end with a letter or digit.";
  if (!DNS_LABEL.test(name)) return "Use lower-case letters, digits and hyphens only.";
  return undefined;
}

/** Display name, environment and region: optional, at most 63 printable characters. */
export function labelError(value: string): string | undefined {
  if ([...value].length > LABEL_MAX) return `Use at most ${LABEL_MAX} characters.`;
  // biome-ignore lint/suspicious/noControlCharactersInRegex: rejecting control characters is the point
  if (/[\u0000-\u001f\u007f]/.test(value)) return "Use printable characters only.";
  return undefined;
}

/** Suggests a cluster name from a display name: "Prod EU 2" → "prod-eu-2". */
export function suggestName(display: string): string {
  return display
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, NAME_MAX)
    .replace(/-+$/, "");
}

/**
 * The cluster colour choices. Each entry names a token in design/tokens.css
 * (--color-<token>) and repeats its value, because the API stores a hex colour.
 * lib/onboarding.test.ts checks the two stay in sync. `swatch` is the Tailwind class
 * that paints it, spelled out so Tailwind finds it.
 */
export const CLUSTER_PALETTE = [
  { token: "cluster-1", hex: "#6d28d9", name: "Violet", swatch: "bg-cluster-1" },
  { token: "cluster-2", hex: "#0f766e", name: "Teal", swatch: "bg-cluster-2" },
  { token: "cluster-3", hex: "#c2410c", name: "Orange", swatch: "bg-cluster-3" },
  { token: "cluster-4", hex: "#2563eb", name: "Blue", swatch: "bg-cluster-4" },
  { token: "cluster-5", hex: "#be185d", name: "Pink", swatch: "bg-cluster-5" },
  { token: "cluster-6", hex: "#4d7c0f", name: "Green", swatch: "bg-cluster-6" },
  { token: "cluster-none", hex: "#475569", name: "Slate", swatch: "bg-cluster-none" },
] as const;

/** The palette entry for a stored colour, compared case-insensitively. */
export const paletteEntry = (hex: string | undefined) =>
  hex ? CLUSTER_PALETTE.find((p) => p.hex === hex.toLowerCase()) : undefined;

export const REASON_LABEL: Record<AttemptReason, string> = {
  bad_token: "Unknown or expired agent token",
  join_expired: "Join token expired",
  join_used: "Join token already used",
  wrong_cluster: "Token belongs to another cluster",
  protocol_mismatch: "Agent protocol version not supported",
  hello_rejected: "Agent handshake rejected",
  credentials_failed: "Agent could not store its permanent token",
};

/** Human wording for a rejected attempt's reason; unknown reasons are shown as sent. */
export const reasonLabel = (reason: string): string =>
  REASON_LABEL[reason as AttemptReason] ?? reason.replace(/_/g, " ");

export const CHECK_STATE_LABEL: Record<CheckState, string> = {
  ok: "Passed",
  warn: "Warning",
  fail: "Failed",
  pending: "Waiting",
  info: "Info",
};

/** The checks that must pass before the connect screen stops polling. */
const CORE_CHECKS: readonly CheckId[] = ["connected", "protocol", "informers"];

/** True once the agent is connected, compatible and synced. */
export function coreReady(info: ConnectionInfo | undefined): boolean {
  if (!info) return false;
  return CORE_CHECKS.every((id) => info.checks.find((c) => c.id === id)?.state === "ok");
}

/** True when the "agent connected" check has passed. */
export const isConnected = (info: ConnectionInfo | undefined): boolean =>
  info?.checks.find((c) => c.id === "connected")?.state === "ok";

/** "in 59m", "in 3h" or "expired", for a join token's expiry. */
export function expiresIn(iso: string, now: number = Date.now()): string {
  const ms = Date.parse(iso) - now;
  if (Number.isNaN(ms)) return "";
  if (ms <= 0) return "expired";
  const m = Math.ceil(ms / 60_000);
  if (m < 60) return `in ${m}m`;
  const h = Math.floor(m / 60);
  const rest = m % 60;
  return rest ? `in ${h}h ${rest}m` : `in ${h}h`;
}
