import { Link } from "@tanstack/react-router";
import { createContext, useContext, useMemo } from "react";
import type { ResourceRef } from "../api/types";
import { matchRef, refTitle } from "../lib/answerRefs";
import { detailLink } from "../lib/links";
import { type Inline, type ListItem, parseMarkdown } from "../lib/markdown";
import { CopyButton } from "./CopyButton";

/** Builds the internal link target of a ref; the default is its detail page. */
export type RefLinkBuilder = (ref: ResourceRef) => ReturnType<typeof detailLink>;

const defaultRefLink: RefLinkBuilder = (r) => detailLink(r.cluster, r);

interface RefLinking {
  refs: readonly ResourceRef[];
  build: RefLinkBuilder;
  /** Mention the cluster on hover when the refs span several clusters. */
  multiCluster: boolean;
}

const RefContext = createContext<RefLinking | null>(null);

/**
 * An inline code span: a link to the resource page when it names exactly one of the refs the
 * hub sent with the answer, otherwise plain code. Inside an external link it stays code.
 */
function Code({ text, inLink }: { text: string; inLink: boolean }) {
  const linking = useContext(RefContext);
  const ref = linking && !inLink ? matchRef(text, linking.refs) : null;
  if (!linking || !ref) return <code>{text}</code>;
  return (
    <Link {...linking.build(ref)} className="md-ref" title={refTitle(ref, linking.multiCluster)}>
      <code>{text}</code>
    </Link>
  );
}

function InlineNodes({ nodes, inLink = false }: { nodes: Inline[]; inLink?: boolean }) {
  return nodes.map((n, i) => {
    const key = `${i}-${n.t}`;
    switch (n.t) {
      case "text":
        return <span key={key}>{n.text}</span>;
      case "code":
        return <Code key={key} text={n.text} inLink={inLink} />;
      case "strong":
        return (
          <strong key={key}>
            <InlineNodes nodes={n.children} inLink={inLink} />
          </strong>
        );
      case "em":
        return (
          <em key={key}>
            <InlineNodes nodes={n.children} inLink={inLink} />
          </em>
        );
      case "link":
        return (
          <a key={key} href={n.href} target="_blank" rel="noopener noreferrer nofollow">
            <InlineNodes nodes={n.children} inLink />
          </a>
        );
      default:
        return null;
    }
  });
}

function List({ list }: { list: { t: "ul" | "ol"; items: ListItem[] } }) {
  const Tag = list.t;
  return (
    <Tag>
      {list.items.map((item, j) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: list items have no identity beyond position
        <li key={j}>
          <InlineNodes nodes={item.children} />
          {item.sub && <List list={item.sub} />}
        </li>
      ))}
    </Tag>
  );
}

/**
 * Renders the safe Markdown subset of lib/markdown.ts. Never renders HTML or images. With
 * `refs` (an AI answer's meta.refs), inline code naming exactly one ref links to its page;
 * links come only from those refs, never from the text alone.
 */
export function Markdown({
  source,
  className = "md",
  refs,
  refLink = defaultRefLink,
}: {
  source: string;
  className?: string;
  refs?: readonly ResourceRef[];
  refLink?: RefLinkBuilder;
}) {
  const blocks = useMemo(() => parseMarkdown(source), [source]);
  const linking = useMemo<RefLinking | null>(
    () =>
      refs?.length
        ? { refs, build: refLink, multiCluster: new Set(refs.map((r) => r.cluster)).size > 1 }
        : null,
    [refs, refLink],
  );
  return (
    <RefContext.Provider value={linking}>
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
          if (b.t === "h") {
            return (
              <p key={key} className="md-h">
                <strong>
                  <InlineNodes nodes={b.children} />
                </strong>
              </p>
            );
          }
          if (b.t === "table") {
            // Wide tables scroll inside their own box instead of widening the panel.
            return (
              <div key={key} className="md-table">
                <table>
                  <thead>
                    <tr>
                      {b.head.map((cell, j) => (
                        // biome-ignore lint/suspicious/noArrayIndexKey: columns have no identity beyond position
                        <th key={j} scope="col">
                          <InlineNodes nodes={cell} />
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((row, r) => (
                      // biome-ignore lint/suspicious/noArrayIndexKey: rows have no identity beyond position
                      <tr key={r}>
                        {row.map((cell, j) => (
                          // biome-ignore lint/suspicious/noArrayIndexKey: columns have no identity beyond position
                          <td key={j}>
                            <InlineNodes nodes={cell} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
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
          return <List key={key} list={b} />;
        })}
      </div>
    </RefContext.Provider>
  );
}
