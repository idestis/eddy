// The cluster colour picker (backlog #21): "Auto" (a stable colour from the name), the
// curated swatches, and "Custom…" (the native colour input plus a hex field) with a
// contrast check against both themes' surfaces and the tile's initials.

import { type CSSProperties, useId, useState } from "react";
import type { ClusterInfo } from "../api/types";
import { autoColor, checkColor } from "../lib/clusterColor";
import { CLUSTER_PALETTE, paletteEntry } from "../lib/onboarding";
import { ClusterTile } from "./ClusterSwitch";
import { Icon } from "./Icon";

const HEX = /^#[0-9a-f]{6}$/i;
const SWATCH =
  "relative size-8 rounded-full ring-ink ring-offset-2 ring-offset-surface transition-shadow duration-(--duration-fast) aria-pressed:ring-2";

export function ClusterColorPicker({
  value,
  onChange,
  name,
  preview,
}: {
  /** A #RRGGBB colour, or undefined for Auto. */
  value: string | undefined;
  onChange: (color: string | undefined) => void;
  /** The cluster name, for the Auto colour. */
  name: string;
  preview: ClusterInfo;
}) {
  const id = useId();
  const isCustom = value !== undefined && !paletteEntry(value);
  const [customOpen, setCustomOpen] = useState(isCustom);
  const [hex, setHex] = useState(isCustom ? (value ?? "") : "");
  const auto = autoColor(name || "cluster");
  const effective = value ?? auto;
  const check = checkColor(effective);
  const customValue = isCustom ? value : hex && HEX.test(hex) ? hex : "#7c3aed";

  const pickCustom = (next: string) => {
    setHex(next);
    if (HEX.test(next)) onChange(next.toLowerCase());
  };

  return (
    <fieldset className="m-0 flex flex-col gap-2.5 border-0 p-0">
      <legend className="mb-2 p-0 text-13 text-ink-2">Colour</legend>
      <div className="flex flex-wrap items-center gap-2.5">
        <button
          type="button"
          className={`${SWATCH} flex items-center justify-center bg-cc text-10-5 font-bold text-white`}
          style={{ "--cc": auto } as CSSProperties}
          aria-label={`Auto: a colour from the name (${auto})`}
          aria-pressed={value === undefined}
          title="Auto: picked from the cluster name, the same everywhere"
          onClick={() => {
            setCustomOpen(false);
            onChange(undefined);
          }}
        >
          A
        </button>
        {CLUSTER_PALETTE.map((p) => (
          <button
            key={p.token}
            type="button"
            className={`${SWATCH} ${p.swatch}`}
            aria-label={p.name}
            aria-pressed={value === p.hex}
            title={p.name}
            onClick={() => {
              setCustomOpen(false);
              onChange(p.hex);
            }}
          />
        ))}
        <button
          type="button"
          className={`${SWATCH} flex items-center justify-center border border-dashed border-line-strong ${isCustom ? "bg-cc" : "bg-surface-sunken"} text-ink-3`}
          style={isCustom ? ({ "--cc": value } as CSSProperties) : undefined}
          aria-label="Custom colour"
          aria-pressed={isCustom}
          aria-expanded={customOpen}
          aria-controls={`${id}-custom`}
          title="Custom…"
          onClick={() => {
            setCustomOpen(true);
            if (!isCustom) pickCustom(customValue ?? "#7c3aed");
          }}
        >
          {!isCustom && <Icon name="plus" className="size-3.5" />}
        </button>
        <span className="ml-auto flex items-center gap-2 text-12-5 text-ink-3">
          Preview
          <ClusterTile cluster={preview} />
        </span>
      </div>
      {customOpen && (
        <div id={`${id}-custom`} className="anim-fade-in flex flex-wrap items-center gap-2.5">
          <input
            type="color"
            className="color-input"
            aria-label="Pick a colour"
            value={HEX.test(hex) ? hex : (customValue ?? "#7c3aed")}
            onChange={(e) => pickCustom(e.target.value)}
          />
          <label className="flex items-center gap-2 text-12-5 text-ink-3">
            Hex
            <input
              className="input w-[110px] font-mono text-13!"
              value={hex}
              maxLength={7}
              spellCheck={false}
              autoComplete="off"
              placeholder="#7c3aed"
              aria-invalid={hex !== "" && !HEX.test(hex)}
              onChange={(e) => {
                const v = e.target.value.trim();
                pickCustom(v.startsWith("#") ? v : `#${v}`);
              }}
            />
          </label>
          {hex !== "" && !HEX.test(hex) && <span className="text-12 text-bad">Use #RRGGBB.</span>}
        </div>
      )}
      {check.warnings.length > 0 && (
        <div
          className="anim-fade-in flex items-start gap-2 rounded-xl border border-warn/40 bg-warn/12 px-3 py-2 text-12-5 text-ink-2"
          role="status"
        >
          <Icon name="alert" className="mt-px size-4 shrink-0 text-warn" />
          <span>
            <b className="font-semibold text-ink">Low contrast.</b> {check.warnings.join(" ")}
          </span>
        </div>
      )}
    </fieldset>
  );
}
