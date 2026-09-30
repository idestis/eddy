// Typed confirmation for writes on protected clusters. The hub enforces it
// (428 confirm_required unless `confirm` equals the cluster name); the UI asks
// up front when it knows the cluster is protected, and again whenever the
// hub says so.

import { isApiError } from "../api/client";

export interface ConfirmRequest {
  title: string;
  body: string;
  /** The text the user must type; the cluster name. */
  expected: string;
  action: string;
}

/** Resolves to the typed text, or null when the user cancels. */
export type AskConfirm = (req: ConfirmRequest) => Promise<string | null>;

export const CANCELLED = Symbol("cancelled");

/**
 * Runs `call`. When `upfront` is set the user confirms before the first
 * attempt; otherwise a 428 from the hub triggers the dialog and one retry
 * with the typed confirmation.
 */
export async function withConfirm<T>(
  call: (confirm?: string) => Promise<T>,
  ask: AskConfirm,
  req: ConfirmRequest,
  upfront: boolean,
): Promise<T | typeof CANCELLED> {
  if (upfront) {
    const typed = await ask(req);
    return typed === null ? CANCELLED : call(typed);
  }
  try {
    return await call();
  } catch (err) {
    if (!isApiError(err, "confirm_required")) throw err;
    const typed = await ask(req);
    return typed === null ? CANCELLED : call(typed);
  }
}
