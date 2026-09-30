const keys: Array<{ keys: string[]; label: string }> = [
  { keys: ["j"], label: "move down" },
  { keys: ["k"], label: "move up" },
  { keys: ["l"], label: "open" },
  { keys: ["h"], label: "back" },
  { keys: ["r"], label: "reconcile" },
  { keys: ["R"], label: "reconcile with source" },
  { keys: ["s"], label: "suspend" },
  { keys: ["L"], label: "logs" },
  { keys: ["a"], label: "Ask AI" },
  { keys: ["1", "2", "3"], label: "switch cluster" },
  { keys: ["?"], label: "all keys" },
];

/**
 * Every item is one row: a fixed-width key column and a label that never wraps on wider screens, so
 * the columns line up whatever the label length. On phones it becomes a two-column list where the
 * keys sit above the label.
 */
export function Keymap() {
  return (
    <ul
      aria-label="Key bindings"
      className="keys m-0 mt-4 grid list-none grid-cols-2 gap-x-5 p-0 sm:grid-cols-[repeat(auto-fill,minmax(16rem,1fr))] sm:gap-x-8"
    >
      {keys.map((k) => (
        <li
          key={k.label}
          className="flex flex-col items-start gap-1.5 border-b border-dashed border-line py-3 text-[0.93rem] text-ink-2 sm:min-h-12 sm:flex-row sm:items-center sm:gap-3 sm:py-2"
        >
          <span className="flex gap-1 sm:w-[5.5rem] sm:shrink-0">
            {k.keys.map((key) => (
              <kbd key={key} className="min-w-[2.2em] text-center text-[0.85em]">
                {key}
              </kbd>
            ))}
          </span>
          <span className="sm:whitespace-nowrap">{k.label}</span>
        </li>
      ))}
    </ul>
  );
}
