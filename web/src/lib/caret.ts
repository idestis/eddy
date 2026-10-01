// Where the caret of a textarea is drawn, for popups that open under it (the @mention list).
// A hidden mirror element copies the textarea's box and text styles, holds the text up to the
// caret, and a marker span then sits where the caret would be.

const COPIED = [
  "boxSizing",
  "width",
  "borderTopWidth",
  "borderRightWidth",
  "borderBottomWidth",
  "borderLeftWidth",
  "paddingTop",
  "paddingRight",
  "paddingBottom",
  "paddingLeft",
  "fontFamily",
  "fontSize",
  "fontWeight",
  "fontStyle",
  "letterSpacing",
  "lineHeight",
  "textTransform",
  "wordSpacing",
  "tabSize",
] as const;

/** The caret's offset from the textarea's top-left corner (scrolling included), and the line height. */
export function caretOffset(
  el: HTMLTextAreaElement,
  pos: number,
): { top: number; left: number; height: number } {
  const style = getComputedStyle(el);
  const mirror = document.createElement("div");
  for (const p of COPIED) mirror.style[p] = style[p];
  mirror.style.position = "absolute";
  mirror.style.visibility = "hidden";
  mirror.style.whiteSpace = "pre-wrap";
  mirror.style.overflowWrap = "break-word";
  mirror.style.top = "0";
  mirror.style.left = "-9999px";
  mirror.textContent = el.value.slice(0, pos);
  const marker = document.createElement("span");
  marker.textContent = el.value.slice(pos) || ".";
  mirror.appendChild(marker);
  document.body.appendChild(mirror);
  const lineHeight = Number.parseFloat(style.lineHeight) || Number.parseFloat(style.fontSize) * 1.4 || 18;
  const out = {
    top: marker.offsetTop - el.scrollTop,
    left: marker.offsetLeft - el.scrollLeft,
    height: lineHeight,
  };
  mirror.remove();
  return out;
}
