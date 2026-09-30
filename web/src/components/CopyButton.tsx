import { Icon } from "./Icon";
import { useToast } from "./Toasts";

/** Copies `text` to the clipboard and says so in a toast. */
export function CopyButton({
  text,
  label,
  className = "btn shrink-0",
}: {
  text: string;
  label: string;
  className?: string;
}) {
  const toast = useToast();
  return (
    <button
      type="button"
      className={className}
      aria-label={label}
      onClick={() =>
        navigator.clipboard.writeText(text).then(
          () => toast("Copied to the clipboard", "ok"),
          () => toast("Couldn't copy. Select the text and copy it by hand.", "bad"),
        )
      }
    >
      <Icon name="copy" />
      Copy
    </button>
  );
}
