import { act, render, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BINDINGS,
  type Binding,
  buildKeymap,
  displayKeys,
  installKeyboard,
  isSequence,
  pushOverlay,
  shouldIgnore,
  useKeys,
  usePendingSequence,
} from "./keys";

const all = Object.entries(BINDINGS) as [string, Binding][];

function press(target: EventTarget, key: string, init: KeyboardEventInit = {}) {
  const code = key.length === 1 ? `Key${key.toUpperCase()}` : key;
  target.dispatchEvent(new KeyboardEvent("keydown", { key, code, bubbles: true, cancelable: true, ...init }));
}

describe("key registry", () => {
  it("binds every key string to exactly one action", () => {
    const seen = new Map<string, string>();
    for (const [id, b] of all) {
      for (const key of b.keys) {
        const norm = key.toLowerCase();
        expect(seen.get(norm), `"${key}" is bound to both ${seen.get(norm)} and ${id}`).toBeUndefined();
        seen.set(norm, id);
      }
    }
  });

  it("never binds a single key that starts a sequence", () => {
    const firsts = new Set(
      all.flatMap(([, b]) => b.keys.filter((k) => k.includes(" ")).map((k) => k.split(" ")[0])),
    );
    const singles = all.flatMap(([, b]) => b.keys.filter((k) => !k.includes(" ")));
    for (const s of singles) expect(firsts.has(s), `"${s}" shadows a sequence`).toBe(false);
  });

  it("checks a sequence before any single key that could end it", () => {
    // tinykeys stops at the first complete match, so "g f" must come before "f".
    const order = Object.keys(buildKeymap(() => {}));
    for (const seq of order.filter((k) => k.includes(" "))) {
      for (const tail of seq.split(" ").slice(1)) {
        const single = order.indexOf(tail);
        if (single >= 0) expect(order.indexOf(seq), `"${seq}" must precede "${tail}"`).toBeLessThan(single);
      }
    }
  });

  it("gives every binding a label and a group", () => {
    for (const [, b] of all) {
      expect(b.label).not.toBe("");
      expect(b.group).not.toBe("");
    }
  });

  it("formats keycaps", () => {
    expect(displayKeys("$mod+k", true)).toEqual(["⌘K"]);
    expect(displayKeys("$mod+k", false)).toEqual(["Ctrl K"]);
    expect(displayKeys("Control+k", true)).toEqual(["⌃K"]);
    expect(displayKeys("Control+k", false)).toEqual(["Ctrl K"]);
    expect(displayKeys("g f")).toEqual(["g", "f"]);
    expect(displayKeys("Shift+R")).toEqual(["R"]);
    expect(displayKeys("[Shift]+?")).toEqual(["?"]);
    expect(displayKeys("ArrowDown")).toEqual(["↓"]);
    expect(displayKeys("[Shift]+{")).toEqual(["{"]);
  });

  it("marks two-key sequences so hints render as one keycap", () => {
    expect(isSequence("fleet")).toBe(true);
    expect(isSequence("top")).toBe(true);
    expect(isSequence("reconcile")).toBe(false);
  });

  it("keeps [ and ] for sibling pages and moves cluster cycling to { and }", () => {
    expect(BINDINGS.prevSection.keys).toEqual(["["]);
    expect(BINDINGS.nextSection.keys).toEqual(["]"]);
    expect(BINDINGS.prevCluster.keys).toEqual(["[Shift]+{"]);
    expect(BINDINGS.nextCluster.keys).toEqual(["[Shift]+}"]);
  });
});

describe("shouldIgnore", () => {
  const plain = BINDINGS.reconcile;
  const global = BINDINGS.palette;
  const event = (target: Element, key = "r") => {
    const e = new KeyboardEvent("keydown", { key });
    Object.defineProperty(e, "target", { value: target });
    return e;
  };

  it("suppresses bindings while typing in inputs, textareas and contenteditable", () => {
    const input = document.createElement("input");
    const area = document.createElement("textarea");
    const editable = document.createElement("div");
    editable.setAttribute("contenteditable", "true");
    for (const el of [input, area, editable]) {
      expect(shouldIgnore(event(el), plain, false)).toBe(true);
      expect(shouldIgnore(event(el), global, false)).toBe(false);
    }
  });

  it("lets Enter activate a focused button instead of the list", () => {
    const button = document.createElement("button");
    expect(shouldIgnore(event(button, "Enter"), BINDINGS.open, false)).toBe(true);
    expect(shouldIgnore(event(button, "r"), plain, false)).toBe(false);
  });

  it("pauses non-overlay bindings while a dialog is open", () => {
    const body = document.body;
    expect(shouldIgnore(event(body), plain, true)).toBe(true);
    expect(shouldIgnore(event(body), BINDINGS.help, true)).toBe(false);
  });
});

describe("dispatcher", () => {
  let uninstall: () => void;
  beforeEach(() => {
    uninstall = installKeyboard(window);
  });
  afterEach(() => uninstall());

  function Harness({ onReconcile, onFleet }: { onReconcile: () => void; onFleet: () => void }) {
    useKeys({ reconcile: onReconcile, fleet: onFleet });
    return <input aria-label="field" />;
  }

  it("fires handlers for keys pressed outside inputs, and not inside them", () => {
    const onReconcile = vi.fn();
    const { getByLabelText } = render(<Harness onReconcile={onReconcile} onFleet={() => {}} />);
    press(document.body, "r");
    expect(onReconcile).toHaveBeenCalledTimes(1);
    press(getByLabelText("field"), "r");
    expect(onReconcile).toHaveBeenCalledTimes(1);
  });

  it("recognises sequences", () => {
    const onFleet = vi.fn();
    render(<Harness onReconcile={() => {}} onFleet={onFleet} />);
    press(document.body, "g");
    press(document.body, "f");
    expect(onFleet).toHaveBeenCalledTimes(1);
  });

  it("recognises every g sequence", () => {
    const fired: string[] = [];
    function All() {
      useKeys({
        fleet: () => fired.push("fleet"),
        threads: () => fired.push("threads"),
        tokens: () => fired.push("tokens"),
        audit: () => fired.push("audit"),
        top: () => fired.push("top"),
      });
      return null;
    }
    render(<All />);
    for (const second of ["f", "t", "k", "a", "g"]) {
      press(document.body, "g");
      press(document.body, second);
    }
    expect(fired).toEqual(["fleet", "threads", "tokens", "audit", "top"]);
  });

  it("does not let a finished sequence leave others half-typed (g g f is top only)", () => {
    const onTop = vi.fn();
    const onFleet = vi.fn();
    function Both() {
      useKeys({ top: onTop, fleet: onFleet });
      return null;
    }
    render(<Both />);
    press(document.body, "g");
    press(document.body, "g");
    press(document.body, "f");
    expect(onTop).toHaveBeenCalledTimes(1);
    expect(onFleet).not.toHaveBeenCalled();
  });

  it("exposes the pending first key of a sequence and clears it", () => {
    const { result } = renderHook(() => usePendingSequence());
    expect(result.current).toBeNull();
    act(() => press(document.body, "g"));
    expect(result.current).toBe("g");
    act(() => press(document.body, "x"));
    expect(result.current).toBeNull();
    act(() => press(document.body, "g"));
    act(() => press(document.body, "f"));
    expect(result.current).toBeNull();
  });

  it("does not show a pending sequence while typing in an input", () => {
    const input = document.createElement("input");
    document.body.append(input);
    const { result } = renderHook(() => usePendingSequence());
    act(() => press(input, "g"));
    expect(result.current).toBeNull();
    input.remove();
  });

  it("does not fire shifted bindings for the unshifted key", () => {
    const onReconcile = vi.fn();
    render(<Harness onReconcile={onReconcile} onFleet={() => {}} />);
    press(document.body, "R", { shiftKey: true });
    expect(onReconcile).not.toHaveBeenCalled();
  });

  it("stops while an overlay is open", () => {
    const onReconcile = vi.fn();
    render(<Harness onReconcile={onReconcile} onFleet={() => {}} />);
    const release = pushOverlay();
    press(document.body, "r");
    release();
    press(document.body, "r");
    expect(onReconcile).toHaveBeenCalledTimes(1);
  });

  it("routes a key to the most recently mounted handler and restores on unmount", () => {
    const first = vi.fn();
    const second = vi.fn();
    function Handler({ fn }: { fn: () => void }) {
      useKeys({ reconcile: fn });
      return null;
    }
    const { rerender } = render(<Handler key="a" fn={first} />);
    const other = render(<Handler key="b" fn={second} />);
    press(document.body, "r");
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
    other.unmount();
    act(() => rerender(<Handler key="a" fn={first} />));
    press(document.body, "r");
    expect(first).toHaveBeenCalledTimes(1);
  });
});
