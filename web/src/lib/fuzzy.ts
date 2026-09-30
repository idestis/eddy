// Match ranking for the command palette.
//
// Each whitespace-separated query term is matched against an item's fields, and the best
// match of each term counts. A match on the primary field (a resource name, a command
// label) always beats one on a secondary field (namespace, kind, cluster, keywords):
//
//   name:      exact > prefix > word-boundary substring > substring > subsequence
//   secondary: exact > prefix > word-boundary substring > substring
//
// Scattered subsequence matches count only on the primary field and only when most query
// characters land on consecutive characters or word starts, so "flux-sy" does not match
// "apps" through its namespace letters. Matched positions come back for highlighting.

const WORD_BREAK = /[\s\-/_.:@]/;

/** How a single field matched. Higher is better; tiers are 100 apart. */
export const TIER = {
  exact: 900,
  prefix: 800,
  word: 700,
  substring: 600,
  secondaryExact: 500,
  secondaryPrefix: 400,
  secondaryWord: 300,
  secondarySubstring: 200,
  subsequence: 100,
} as const;

/** Half-open [start, end) ranges of matched characters. */
export type Ranges = Array<[number, number]>;

export interface FieldMatch {
  score: number;
  ranges: Ranges;
}

const isWordStart = (t: string, i: number) => i === 0 || WORD_BREAK.test(t.charAt(i - 1));

function wordBoundaryIndex(t: string, q: string): number {
  let i = t.indexOf(q);
  while (i > 0 && !isWordStart(t, i)) i = t.indexOf(q, i + 1);
  return i;
}

/** Scores one term against one field, or returns null when it does not match well enough. */
export function matchField(term: string, text: string, primary: boolean): FieldMatch | null {
  const q = term.toLowerCase();
  const t = text.toLowerCase();
  if (!q) return { score: 1, ranges: [] };
  if (!t) return null;
  // Shorter targets win ties inside a tier, so "redis" beats "redis-replicas-long-name".
  // The in-tier bonus stays below 100, so it never crosses into the next tier.
  const tidy = (tier: number, at: number) =>
    tier + 40 * (q.length / t.length) + (40 - Math.min(at, 40)) * 0.5;
  const whole = (tier: number, at: number): FieldMatch => ({
    score: tidy(tier, at),
    ranges: [[at, at + q.length]],
  });

  if (t === q) return whole(primary ? TIER.exact : TIER.secondaryExact, 0);
  if (t.startsWith(q)) return whole(primary ? TIER.prefix : TIER.secondaryPrefix, 0);
  const w = wordBoundaryIndex(t, q);
  if (w >= 0) return whole(primary ? TIER.word : TIER.secondaryWord, w);
  const s = t.indexOf(q);
  if (s >= 0) return whole(primary ? TIER.substring : TIER.secondarySubstring, s);
  if (!primary) return null;

  // Subsequence: every character in order, preferring word starts.
  const ranges: Ranges = [];
  let from = 0;
  let last = -2;
  let good = 0;
  for (const ch of q) {
    let j = t.indexOf(ch, from);
    if (j < 0) return null;
    // Jump to a word start of the same character if there is one before the next hit.
    if (j !== last + 1) {
      for (let k = j; k >= 0 && k < t.length; k = t.indexOf(ch, k + 1)) {
        if (isWordStart(t, k)) {
          j = k;
          break;
        }
      }
    }
    if (j === last + 1 || isWordStart(t, j)) good++;
    const tail = ranges[ranges.length - 1];
    if (tail && tail[1] === j) tail[1] = j + 1;
    else ranges.push([j, j + 1]);
    last = j;
    from = j + 1;
  }
  // Minimum quality: most characters must be contiguous or start a word.
  if (good / q.length < 0.6) return null;
  const span = (ranges[ranges.length - 1]?.[1] ?? 0) - (ranges[0]?.[0] ?? 0);
  const bonus = good * 4 - ranges.length * 3 - span * 0.2;
  return { score: TIER.subsequence + Math.max(0, Math.min(99, 50 + bonus)), ranges };
}

export interface Fields {
  /** The primary text (resource name, command label). */
  primary: string;
  /** Secondary texts in priority order (namespace, kind, cluster, keywords). */
  secondary?: readonly string[];
}

export interface Match {
  score: number;
  /** Matched ranges in the primary text. */
  primary: Ranges;
  /** Matched ranges per secondary text, by index. */
  secondary: Ranges[];
}

const mergeRanges = (ranges: Ranges): Ranges => {
  const sorted = [...ranges].sort((a, b) => a[0] - b[0]);
  const out: Ranges = [];
  for (const r of sorted) {
    const tail = out[out.length - 1];
    if (tail && r[0] <= tail[1]) tail[1] = Math.max(tail[1], r[1]);
    else out.push([r[0], r[1]]);
  }
  return out;
};

/** Matches a whole query (AND of its terms) against an item's fields. */
export function matchItem(query: string, fields: Fields): Match | null {
  const terms = query.trim().split(/\s+/).filter(Boolean);
  const secondary = fields.secondary ?? [];
  const out: Match = { score: 0, primary: [], secondary: secondary.map(() => []) };
  if (!terms.length) return { ...out, score: 1 };
  for (const term of terms) {
    let best: FieldMatch | null = matchField(term, fields.primary, true);
    let where = -1;
    secondary.forEach((text, i) => {
      const m = matchField(term, text, false);
      // Earlier secondary fields win ties.
      if (m && (!best || m.score - i > best.score)) {
        best = m;
        where = i;
      }
    });
    if (!best) return null;
    const found: FieldMatch = best;
    out.score += found.score;
    if (where < 0) out.primary.push(...found.ranges);
    else out.secondary[where]?.push(...found.ranges);
  }
  out.score /= terms.length;
  out.primary = mergeRanges(out.primary);
  out.secondary = out.secondary.map(mergeRanges);
  return out;
}

export interface Ranked<T> {
  item: T;
  match: Match;
}

/**
 * Matches, drops non-matches, and sorts best first. `tiebreak` orders items whose match
 * quality is equal (failing first, current cluster first); it never beats a better match.
 */
export function rank<T>(
  query: string,
  items: readonly T[],
  fields: (item: T) => Fields,
  limit = 30,
  tiebreak: (a: T, b: T) => number = () => 0,
): Ranked<T>[] {
  const scored: Ranked<T>[] = [];
  for (const item of items) {
    const match = matchItem(query, fields(item));
    if (match) scored.push({ item, match });
  }
  // Tiers are 100 apart: equal tier means equal match quality, so the tiebreak decides
  // before the finer in-tier score (shorter names, earlier hits).
  const tier = (m: Match) => Math.floor(m.score / 100);
  scored.sort(
    (a, b) => tier(b.match) - tier(a.match) || tiebreak(a.item, b.item) || b.match.score - a.match.score,
  );
  return scored.slice(0, limit);
}

/** Splits text into plain and matched parts for rendering highlights. */
export function highlightParts(text: string, ranges: Ranges): Array<{ text: string; hit: boolean }> {
  const parts: Array<{ text: string; hit: boolean }> = [];
  let at = 0;
  for (const [start, end] of ranges) {
    if (start > at) parts.push({ text: text.slice(at, start), hit: false });
    parts.push({ text: text.slice(start, end), hit: true });
    at = end;
  }
  if (at < text.length) parts.push({ text: text.slice(at), hit: false });
  return parts;
}
