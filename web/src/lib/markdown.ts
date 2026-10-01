// A deliberately small Markdown subset for AI answers and thread bodies:
// paragraphs, headings (shown as bold lines), bullet and numbered lists (one
// level of nesting), GitHub-style tables, fenced code, inline code, bold, italic and http(s) links. Everything else, including raw HTML and images,
// stays plain text. The output is a tree of plain objects that the Markdown
// component renders with React, so nothing is ever injected as HTML.

export type Inline =
  | { t: "text"; text: string }
  | { t: "code"; text: string }
  | { t: "strong"; children: Inline[] }
  | { t: "em"; children: Inline[] }
  | { t: "link"; href: string; children: Inline[] };

/** A list item: its text and, for a top-level item, an optional nested list. */
export interface ListItem {
  children: Inline[];
  sub?: { t: "ul" | "ol"; items: ListItem[] };
}

export type Block =
  | { t: "p"; children: Inline[] }
  | { t: "ul" | "ol"; items: ListItem[] }
  | { t: "pre"; text: string }
  | { t: "h"; children: Inline[] }
  | { t: "table"; head: Inline[][]; rows: Inline[][][] };

/** Returns the URL if it is an absolute http(s) link, otherwise null. */
export function safeHref(raw: string): string | null {
  const href = raw.trim();
  if (!/^https?:\/\//i.test(href)) return null;
  try {
    const url = new URL(href);
    return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
  } catch {
    return null;
  }
}

// Order matters: code spans first so their content is not parsed further.
const INLINE =
  /`([^`\n]+)`|!\[([^\]\n]*)\]\(([^)\s]*)\)|\[([^\]\n]+)\]\(([^)\s]+)\)|\*\*([^*\n]+)\*\*|__([^_\n]+)__|(?<![\w*])\*([^*\n]+)\*(?!\w)|(?<![\w_])_([^_\n]+)_(?!\w)|(https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g;

export function parseInline(src: string): Inline[] {
  const out: Inline[] = [];
  let last = 0;
  const push = (text: string) => {
    if (!text) return;
    const prev = out[out.length - 1];
    if (prev?.t === "text") prev.text += text;
    else out.push({ t: "text", text });
  };
  for (const m of src.matchAll(INLINE)) {
    const start = m.index ?? 0;
    push(src.slice(last, start));
    last = start + m[0].length;
    const [whole, code, imgAlt, , linkText, linkHref, strong1, strong2, em1, em2, bare] = m;
    if (code !== undefined) out.push({ t: "code", text: code });
    else if (imgAlt !== undefined) {
      // Images are never loaded; keep only the alt text.
      push(imgAlt);
    } else if (linkText !== undefined && linkHref !== undefined) {
      const href = safeHref(linkHref);
      if (href) out.push({ t: "link", href, children: parseInline(linkText) });
      else push(linkText);
    } else if (strong1 !== undefined || strong2 !== undefined) {
      out.push({ t: "strong", children: parseInline(strong1 ?? strong2 ?? "") });
    } else if (em1 !== undefined || em2 !== undefined) {
      out.push({ t: "em", children: parseInline(em1 ?? em2 ?? "") });
    } else if (bare !== undefined) {
      const href = safeHref(bare);
      if (href) out.push({ t: "link", href, children: [{ t: "text", text: bare }] });
      else push(bare);
    } else push(whole);
  }
  push(src.slice(last));
  return out;
}

const FENCE = /^\s*```/;
const BULLET = /^(\s*)[-*•+]\s+(.*)$/;
const NUMBERED = /^(\s*)\d+[.)]\s+(.*)$/;

/** Indent width of a list marker; a tab counts as a nesting step. */
const indentOf = (ws: string): number => ws.replace(/\t/g, "  ").length;
const HEADING = /^\s*#{1,6}\s+(.*)$/;
const TABLE_ROW = /^\s*\|.*\|\s*$/;
const TABLE_RULE = /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$/;
const MAX_TABLE_ROWS = 200;

/** Splits "| a | b \| c |" into cells, keeping escaped pipes. */
function cells(line: string): string[] {
  const inner = line.trim().replace(/^\|/, "").replace(/\|$/, "");
  return inner.split(/(?<!\\)\|/).map((c) => c.replace(/\\\|/g, "|").trim());
}

export function parseMarkdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let para: string[] = [];
  // The cast keeps TypeScript from narrowing to null; the closures below reassign it.
  let list = null as { t: "ul" | "ol"; items: ListItem[] } | null;

  const flushPara = () => {
    if (para.length) blocks.push({ t: "p", children: parseInline(para.join(" ")) });
    para = [];
  };
  const flushList = () => {
    if (list) blocks.push(list);
    list = null;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (FENCE.test(line)) {
      flushPara();
      flushList();
      const code: string[] = [];
      i++;
      while (i < lines.length && !FENCE.test(lines[i] ?? "")) code.push(lines[i++] ?? "");
      blocks.push({ t: "pre", text: code.join("\n") });
      continue;
    }
    // A table is a header row, a |---| rule, then rows; anything else stays a paragraph.
    if (TABLE_ROW.test(line) && TABLE_RULE.test(lines[i + 1] ?? "")) {
      flushPara();
      flushList();
      const head = cells(line);
      const rows: Inline[][][] = [];
      i += 2;
      while (i < lines.length && TABLE_ROW.test(lines[i] ?? "") && rows.length < MAX_TABLE_ROWS) {
        const row = cells(lines[i] ?? "");
        rows.push(head.map((_, c) => parseInline(row[c] ?? "")));
        i++;
      }
      i--;
      blocks.push({ t: "table", head: head.map((h) => parseInline(h)), rows });
      continue;
    }
    const heading = line.match(HEADING);
    if (heading) {
      flushPara();
      flushList();
      blocks.push({ t: "h", children: parseInline(heading[1] ?? "") });
      continue;
    }
    const bullet = line.match(BULLET);
    const numbered = bullet ? null : line.match(NUMBERED);
    if (bullet || numbered) {
      flushPara();
      const kind = bullet ? "ul" : "ol";
      const m = bullet ?? numbered;
      const text = parseInline(m?.[2] ?? "");
      // An item indented by 2+ spaces (or a tab) under an item is a child of that item;
      const parent = list?.items[list.items.length - 1];
      // deeper levels, and a change of marker, stay in that one child list.
      if (parent && indentOf(m?.[1] ?? "") >= 2) {
        parent.sub ??= { t: kind, items: [] };
        parent.sub.items.push({ children: text });
        continue;
      }
      if (list?.t !== kind) {
        flushList();
        list = { t: kind, items: [] };
      }
      list.items.push({ children: text });
      continue;
    }
    if (!line.trim()) {
      flushPara();
      flushList();
      continue;
    }
    flushList();
    para.push(line.trim());
  }
  flushPara();
  flushList();
  return blocks;
}
