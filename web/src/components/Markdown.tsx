import { useMemo } from "react";
import { type Inline, parseMarkdown } from "../lib/markdown";
import { CopyButton } from "./CopyButton";

function InlineNodes({ nodes }: { nodes: Inline[] }) {
  return nodes.map((n, i) => {
    const key = `${i}-${n.t}`;
    switch (n.t) {
      case "text":
        return <span key={key}>{n.text}</span>;
      case "code":
        return <code key={key}>{n.text}</code>;
      case "strong":
        return (
          <strong key={key}>
            <InlineNodes nodes={n.children} />
          </strong>
        );
      case "em":
        return (
          <em key={key}>
            <InlineNodes nodes={n.children} />
          </em>
        );
      case "link":
        return (
          <a key={key} href={n.href} target="_blank" rel="noopener noreferrer nofollow">
            <InlineNodes nodes={n.children} />
          </a>
        );
      default:
        return null;
    }
  });
}

/** Renders the safe Markdown subset of lib/markdown.ts. Never renders HTML or images. */
export function Markdown({ source, className = "md" }: { source: string; className?: string }) {
  const blocks = useMemo(() => parseMarkdown(source), [source]);
  return (
    <div className={className}>
      {blocks.map((b, i) => {
        const key = `${i}-${b.t}`;
        if (b.t === "pre") {
          // Long lines scroll sideways instead of being cut off; Copy sits top-right.
          return (
            <div key={key} className="md-pre group relative">
              <pre>
                <code>{b.text}</code>
              </pre>
              <CopyButton
                text={b.text}
                label="Copy code"
                className="absolute top-1.5 right-1.5 inline-flex h-7 items-center gap-1.5 rounded-lg border border-white/14 bg-code-bg/90 px-2 text-11-5 font-medium text-code-ink opacity-80 hover:opacity-100 focus-visible:opacity-100 [&_svg]:size-3.5"
              />
            </div>
          );
        }
        if (b.t === "p") {
          return (
            <p key={key}>
              <InlineNodes nodes={b.children} />
            </p>
          );
        }
        const List = b.t;
        return (
          <List key={key}>
            {b.items.map((item, j) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: list items have no identity beyond position
              <li key={j}>
                <InlineNodes nodes={item} />
              </li>
            ))}
          </List>
        );
      })}
    </div>
  );
}
