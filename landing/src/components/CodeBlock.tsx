import { useEffect, useState } from "react";

const PROMPT = "$ ";

/**
 * A dark command block with a Copy button. Lines starting with "$ " get a dimmed, non-selectable
 * prompt. One-line blocks centre the button vertically; longer blocks pin it to the top-right with
 * the same inset on both sides.
 */
export function CodeBlock({ lines, className = "" }: { lines: string[]; className?: string }) {
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
      {/* Focusable so keyboard users can scroll long lines. */}
      <pre
        // biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable region must be reachable by keyboard
        tabIndex={0}
        className="m-0 min-w-0 flex-1 overflow-x-auto py-4 pl-4 font-mono text-[0.82rem] leading-relaxed"
      >
        {lines.map((line) => (
          <span key={line} className="block whitespace-pre">
            {line.startsWith(PROMPT) ? (
              <>
                <span className="select-none text-code-dim">{PROMPT}</span>
                {line.slice(PROMPT.length)}
              </>
            ) : (
              line
            )}
          </span>
        ))}
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
