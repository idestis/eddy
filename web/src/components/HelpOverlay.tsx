import { useAppState } from "../lib/appState";
import { BINDINGS, type Binding, displayKeys, KEY_GROUPS } from "../lib/keys";
import { Modal } from "./Modal";
import { Keys } from "./Status";

/** Every binding in lib/keys.ts, grouped. Letter keys are shown before arrow aliases. */
export function HelpOverlay() {
  const { setHelp } = useAppState();
  const bindings = Object.values(BINDINGS) as Binding[];
  return (
    <Modal label="Keyboard shortcuts" onClose={() => setHelp(false)} className="w-[min(820px,100%)]!">
      <div className="px-[22px] pt-[22px] pb-1">
        <h3 className="mb-1.5 text-18 font-semibold">Keyboard shortcuts</h3>
        <p className="text-ink-2">
          Everything works without a mouse. Press <kbd>?</kbd> or <kbd>esc</kbd> to close. For two-key
          shortcuts such as <kbd>g f</kbd>, press <kbd>g</kbd>, then the second key within a second.
        </p>
      </div>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-x-7 gap-y-1 overflow-auto px-[22px] pt-1 pb-[22px]">
        {KEY_GROUPS.map((group) => (
          <HelpGroup key={group} group={group} bindings={bindings.filter((b) => b.group === group)} />
        ))}
      </div>
    </Modal>
  );
}

function HelpGroup({ group, bindings }: { group: string; bindings: Binding[] }) {
  return (
    <>
      <h4 className="col-span-full mt-3.5 mb-1 text-13 font-medium text-ink-3">{group}</h4>
      {bindings.map((b) => {
        // Digit bindings collapse to "1…9"; others show their first two alternatives.
        const alternatives =
          b.keys.length > 3
            ? [[`${b.keys[0]}…${b.keys[b.keys.length - 1]}`]]
            : b.keys.slice(0, 2).map((k) => displayKeys(k));
        return (
          <div
            className="flex items-center justify-between gap-3 border-b border-line py-1.5 text-13"
            key={b.label}
          >
            <span>{b.label}</span>
            <span className="inline-flex gap-1.5">
              {alternatives.map((caps) => (
                <Keys key={caps.join(" ")} keys={caps} sequence={caps.length > 1} />
              ))}
            </span>
          </div>
        );
      })}
    </>
  );
}
