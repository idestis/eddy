// Subsequence fuzzy scoring for the command palette. Every query character
// must appear in order. Consecutive matches and matches at word starts score
// higher; longer targets lose a little so exact short names float up.

const WORD_BREAK = /[\s\-/_.:@]/;

export function fuzzyScore(query: string, target: string): number {
  const q = query.trim().toLowerCase();
  if (!q) return 1;
  const t = target.toLowerCase();
  let from = 0;
  let last = -2;
  let score = 0;
  for (const ch of q) {
    if (ch === " ") continue;
    const j = t.indexOf(ch, from);
    if (j < 0) return 0;
    score += j === last + 1 ? 3 : 1;
    if (j === 0 || WORD_BREAK.test(t.charAt(j - 1))) score += 2;
    last = j;
    from = j + 1;
  }
  // Whole-substring hits beat scattered ones.
  if (t.includes(q)) score += q.length;
  return score - t.length * 0.01;
}

/** Scores items, drops non-matches, sorts best first and keeps `limit`. */
export function rank<T>(
  query: string,
  items: readonly T[],
  text: (item: T) => string,
  limit = 30,
  boost: (item: T) => number = () => 0,
): T[] {
  const scored: Array<[number, T]> = [];
  for (const item of items) {
    const s = fuzzyScore(query, text(item));
    if (s > 0) scored.push([s + boost(item), item]);
  }
  scored.sort((a, b) => b[0] - a[0]);
  return scored.slice(0, limit).map(([, item]) => item);
}
