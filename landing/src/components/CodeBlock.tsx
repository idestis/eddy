import type React from "react";
import { useEffect, useState } from "react";

const PROMPT = "$ ";

/**
 * A dark command block with a Copy button. Lines starting with "$ " get a dimmed, non-selectable
 * prompt. Long lines wrap with a hanging indent: a continuation starts two columns right of the
 * line's own indent, so YAML nesting stays readable on narrow screens. One-line blocks centre the button vertically; longer blocks pin it to the top-right with
 * the same inset on both sides.
 */
export function CodeBlock({
  lines: given,
  code,
  className = "",
}: {
  /** One entry per line. */
  lines?: string[];
  /** Or a whole block as text; one leading and one trailing newline are dropped. */
  code?: string;
  className?: string;
}) {
  const lines = given ?? (code ?? "").replace(/^\n/, "").replace(/\n$/, "").split("\n");
  const [ready, setReady] = useState(false);
  const [copied, setCopied] = useState(false);
  useEffect(() => setReady(true), []);

  const multi = lines.length > 1;
  const copy = () => {
    const text = lines.map((l) => (l.startsWith(PROMPT) ? l.slice(PROMPT.length) : l)).join("\n");
    navigator.clipboard?.writeText(text).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1600);
      },
      () => {},
    );
  };

  return (
    <div
      className={`code flex rounded-xl bg-code-bg text-code-ink ${multi ? "items-start" : "items-center"} ${className}`}
    >
      <pre className="m-0 min-w-0 flex-1 py-4 pl-4 font-mono text-[0.82rem] leading-relaxed">
        {lines.map((line, i) => {
          const indent = line.length - line.trimStart().length;
          const text = line.slice(indent);
          return (
            <span
              // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional and may repeat
              key={`${i}:${line}`}
              className="block min-h-[1.4em] whitespace-pre-wrap break-words pl-[calc(var(--ind)+2ch)] -indent-[2ch]"
              style={{ "--ind": `${indent}ch` } as React.CSSProperties}
            >
              {text.startsWith(PROMPT) ? (
                <>
                  <span className="select-none text-code-dim">{PROMPT}</span>
                  {text.slice(PROMPT.length)}
                </>
              ) : (
                text
              )}
            </span>
          );
        })}
      </pre>
      <button
        type="button"
        onClick={copy}
        aria-hidden={!ready}
        tabIndex={ready ? 0 : -1}
        className={`m-2.5 min-h-10 min-w-[4.5rem] shrink-0 cursor-pointer rounded-lg border border-white/20 bg-white/10 px-3 text-xs font-semibold text-code-ink hover:bg-white/20 focus-visible:outline-violet-300 ${ready ? "" : "invisible"}`}
      >
        <span aria-live="polite">{copied ? "Copied" : "Copy"}</span>
      </button>
    </div>
  );
}
