// Structured log lines: JSON objects (zap, slog, logrus JSON…) and logfmt
// (`level=info msg="…"`). The Logs tabs show a level badge, the message and a few key
// fields, and expand to every field. Attachments and Copy always use the raw line.

export type StructuredLevel = "error" | "warn" | "info" | "debug";

export interface Structured {
  level?: StructuredLevel;
  msg: string;
  /** Fields worth seeing inline: caller, error, logger. */
  key: Array<[string, string]>;
  /** Every field, for the expanded view. */
  fields: Array<[string, unknown]>;
  format: "json" | "logfmt";
}

const LEVEL_KEYS = ["level", "lvl", "severity", "log.level", "loglevel"];
const MSG_KEYS = ["msg", "message", "@message", "event"];
const TIME_KEYS = ["ts", "time", "timestamp", "@timestamp", "t"];
const KEY_FIELDS = ["caller", "logger", "error", "err", "component", "controller"];

/** Maps the many spellings of a level to four buckets. Exported for tests. */
export function normalizeLevel(v: unknown): StructuredLevel | undefined {
  if (typeof v === "number") {
    // Bunyan/pino numbers.
    if (v >= 50) return "error";
    if (v >= 40) return "warn";
    if (v >= 30) return "info";
    return "debug";
  }
  if (typeof v !== "string") return undefined;
  const s = v.toLowerCase();
  if (/^(err|error|fatal|panic|crit|critical|alert|emerg|dpanic)/.test(s)) return "error";
  if (/^warn/.test(s)) return "warn";
  if (/^(info|notice|information)/.test(s)) return "info";
  if (/^(debug|trace|verbose|dbg)/.test(s)) return "debug";
  return undefined;
}

const text = (v: unknown): string => (typeof v === "string" ? v : JSON.stringify(v));

function pick(fields: ReadonlyMap<string, unknown>, keys: readonly string[]): [string, unknown] | undefined {
  for (const k of keys) if (fields.has(k)) return [k, fields.get(k)];
  return undefined;
}

function build(entries: Array<[string, unknown]>, format: Structured["format"]): Structured | undefined {
  const map = new Map(entries);
  const level = pick(map, LEVEL_KEYS);
  const msg = pick(map, MSG_KEYS);
  if (!level && !msg) return undefined;
  const hidden = new Set([level?.[0], msg?.[0], ...TIME_KEYS]);
  const key: Array<[string, string]> = [];
  for (const k of KEY_FIELDS) {
    if (map.has(k) && !hidden.has(k)) key.push([k, text(map.get(k))]);
  }
  return {
    level: normalizeLevel(level?.[1]),
    msg: msg ? text(msg[1]) : "",
    key,
    fields: entries,
    format,
  };
}

// key=value, key="quoted \"value\"", key= (empty)
const LOGFMT = /([\w.@-]+)=("(?:[^"\\]|\\.)*"|[^\s]*)/g;

function parseLogfmt(line: string): Structured | undefined {
  if (!/\b(level|lvl|msg)=/.test(line)) return undefined;
  const entries: Array<[string, unknown]> = [];
  for (const m of line.matchAll(LOGFMT)) {
    const [, k = "", raw = ""] = m;
    let v: string = raw;
    if (raw.startsWith('"')) {
      try {
        v = JSON.parse(raw) as string;
      } catch {
        v = raw.slice(1, -1);
      }
    }
    entries.push([k, v]);
  }
  return entries.length >= 2 ? build(entries, "logfmt") : undefined;
}

// `kubectl logs --timestamps` style prefix.
const TS_PREFIX = /^\d{4}-\d\d-\d\dT\S+\s+/;

/** Parses a JSON or logfmt line, or returns undefined for plain text. */
export function parseStructured(line: string): Structured | undefined {
  const t = line.trim().replace(TS_PREFIX, "");
  if (t.startsWith("{") && t.endsWith("}")) {
    try {
      const obj: unknown = JSON.parse(t);
      if (obj && typeof obj === "object" && !Array.isArray(obj)) {
        return build(Object.entries(obj as Record<string, unknown>), "json");
      }
    } catch {
      // Not JSON after all; try logfmt.
    }
  }
  return parseLogfmt(t);
}

/**
 * A field's value for the expanded view. Strings keep their real newlines and tabs, so a
 * `stacktrace` reads as a stack trace; other values are pretty-printed JSON.
 */
export function fieldText(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v, null, 2);
}
