// One anatomy for a chip with a remove button: a pill whose label fills the WHOLE pill on
// hover (never an inner pill), and a round × that turns red on its own. While the × is
// hovered the label shows no hover. Focus rings follow the same split: the whole pill for
// the label, a small ring for the ×.

import type { ReactNode } from "react";
import { Icon, type IconName } from "./Icon";

/** Put these on the label element (a link, a button or plain text) inside the chip. */
export const CHIP_LABEL_PROPS = { "data-chip-label": "" } as const;
export const CHIP_LABEL_CLASS =
  "-my-1 -ml-[9px] inline-flex min-w-0 items-center gap-[7px] rounded-full py-1 pl-[9px] pr-1 text-ink no-underline outline-none";

const ROOT =
  "inline-flex min-h-[30px] min-w-0 max-w-full items-center gap-[7px] rounded-full border border-line-strong bg-surface py-1 pr-1.5 pl-[9px] font-mono text-12 text-ink " +
  "has-[[data-chip-label]:hover]:bg-surface-sunken has-[[data-chip-label]:focus-visible]:ring-2 has-[[data-chip-label]:focus-visible]:ring-c";

export const REMOVE_BUTTON_CLASS =
  "inline-flex size-[22px] shrink-0 items-center justify-center rounded-full text-ink-3 outline-none hover:bg-bad/12 hover:text-bad focus-visible:bg-bad/12 focus-visible:text-bad focus-visible:ring-2 focus-visible:ring-bad";

/** The round remove button, for chips with their own layout. */
export function RemoveButton({
  label,
  title,
  icon = "x",
  onClick,
  className = "",
}: {
  label: string;
  title?: string;
  icon?: IconName;
  onClick: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      className={`${REMOVE_BUTTON_CLASS} ${className}`}
      aria-label={label}
      title={title ?? label}
      onClick={onClick}
    >
      <Icon name={icon} className="size-3.5" />
    </button>
  );
}

export function RemovableChip({
  children,
  onRemove,
  removeLabel,
  removeTitle,
  removeIcon,
  className = "",
}: {
  /** The label element, carrying CHIP_LABEL_PROPS and CHIP_LABEL_CLASS. */
  children: ReactNode;
  onRemove?: () => void;
  removeLabel?: string;
  removeTitle?: string;
  removeIcon?: IconName;
  className?: string;
}) {
  return (
    <span className={`${ROOT} ${className}`}>
      {children}
      {onRemove && removeLabel && (
        <RemoveButton label={removeLabel} title={removeTitle} icon={removeIcon} onClick={onRemove} />
      )}
    </span>
  );
}
