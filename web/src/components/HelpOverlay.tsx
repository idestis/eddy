import { useAppState } from "../lib/appState";
import { BINDINGS, type Binding, displayKeys, KEY_GROUPS } from "../lib/keys";
import { Modal } from "./Modal";
import { Keys } from "./Status";

/** Every binding in lib/keys.ts, grouped. Letter keys are shown before arrow aliases. */
export function HelpOverlay() {
  const { setHelp } = useAppState();
  const bindings = Object.values(BINDINGS) as Binding[];
  return (
    <Modal label="Keyboard shortcuts" onClose={() => setHelp(false)}>
      <div className="help-head">
        <h3>Keyboard shortcuts</h3>
        <p>
          Everything works without a mouse. Press <kbd>?</kbd> or <kbd>esc</kbd> to close.
        </p>
      </div>
      <div className="hgrid">
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
      <h4>{group}</h4>
      {bindings.map((b) => {
        // Digit bindings collapse to "1…9"; others show their first two alternatives.
        const caps =
          b.keys.length > 3
            ? [`${b.keys[0]}…${b.keys[b.keys.length - 1]}`]
            : [...new Set(b.keys.slice(0, 2).map((k) => displayKeys(k).join(" ")))];
        return (
          <div className="hk" key={b.label}>
            <span>{b.label}</span>
            <Keys keys={caps} />
          </div>
        );
      })}
    </>
  );
}
