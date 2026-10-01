// Stroke icons from the design prototype (16×16 grid), plus the Flux and Helm brand marks.

import { BRAND } from "./brandIcons";

const PATHS = {
  chev: "M6 3.5L10.5 8 6 12.5",
  star: "M8 1.9l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.5l-3.9 2.2.9-4.3-3.2-3 4.3-.5z",
  plus: "M8 3v10M3 8h10",
  updown: "M5 6l3-3 3 3M5 10l3 3 3-3",
  lock: "M3.5 8.6a1.6 1.6 0 0 1 1.6-1.6h5.8a1.6 1.6 0 0 1 1.6 1.6v3.3a1.6 1.6 0 0 1-1.6 1.6H5.1a1.6 1.6 0 0 1-1.6-1.6zM5.5 7V5.3a2.5 2.5 0 0 1 5 0V7",
  info: "M8 1.7a6.3 6.3 0 1 1 0 12.6A6.3 6.3 0 0 1 8 1.7zM8 7.3v3.7M8 5v.1",
  sync: "M13 8a5 5 0 0 1-8.6 3.5M3 8a5 5 0 0 1 8.6-3.5M11.8 1.8v2.9H9M4.2 14.2v-2.9H7",
  pause: "M5.5 3.5v9M10.5 3.5v9",
  play: "M5 3.2v9.6l7.6-4.8z",
  term: "M3.8 2.8h8.4a2 2 0 0 1 2 2v6.4a2 2 0 0 1-2 2H3.8a2 2 0 0 1-2-2V4.8a2 2 0 0 1 2-2zM4.8 6.3l2 1.7-2 1.7M8.6 10h2.6",
  open: "M3 8h9M8.5 4.5L12 8l-3.5 3.5",
  back: "M13 8H4M7.5 4.5L4 8l3.5 3.5",
  search: "M7 2.2a4.8 4.8 0 1 1 0 9.6 4.8 4.8 0 0 1 0-9.6zM10.6 10.6l3.4 3.4",
  alert: "M8 2.2l6.3 11H1.7zM8 6.5v3M8 11.5v.1",
  moon: "M13.3 9.6A5.6 5.6 0 0 1 6.4 2.7a5.6 5.6 0 1 0 6.9 6.9z",
  user: "M8 2.8a2.8 2.8 0 1 1 0 5.6 2.8 2.8 0 0 1 0-5.6zM2.8 14c.6-2.7 2.7-4.2 5.2-4.2s4.6 1.5 5.2 4.2",
  trace: "M4 2a2 2 0 1 1 0 4 2 2 0 0 1 0-4zM12 10a2 2 0 1 1 0 4 2 2 0 0 1 0-4zM4 6v2.5a3 3 0 0 0 3 3h3",
  keyboard:
    "M3.3 4h9.4a1.8 1.8 0 0 1 1.8 1.8v4.4a1.8 1.8 0 0 1-1.8 1.8H3.3a1.8 1.8 0 0 1-1.8-1.8V5.8A1.8 1.8 0 0 1 3.3 4zM4 7h.1M6.5 7h.1M9 7h.1M11.5 7h.1M5 9.5h6",
  spark:
    "M8 1.8l1.4 3.6a2 2 0 0 0 1.2 1.2L14.2 8l-3.6 1.4a2 2 0 0 0-1.2 1.2L8 14.2l-1.4-3.6a2 2 0 0 0-1.2-1.2L1.8 8l3.6-1.4a2 2 0 0 0 1.2-1.2z",
  x: "M4.5 4.5l7 7M11.5 4.5l-7 7",
  arrowUp: "M8 13V3.5M4 7.5l4-4 4 4",
  grid: "M3.5 2.5h2.5a1 1 0 0 1 1 1V6a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1zM10 2.5h2.5a1 1 0 0 1 1 1V6a1 1 0 0 1-1 1H10a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1zM3.5 9h2.5a1 1 0 0 1 1 1v2.5a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V10a1 1 0 0 1 1-1zM10 9h2.5a1 1 0 0 1 1 1v2.5a1 1 0 0 1-1 1H10a1 1 0 0 1-1-1V10a1 1 0 0 1 1-1z",
  layers: "M8 2l6 3.3L8 8.6 2 5.3zM2 8.2l6 3.3 6-3.3M2 11l6 3.3 6-3.3",
  git: "M4.5 2.2a1.8 1.8 0 1 1 0 3.6 1.8 1.8 0 0 1 0-3.6zM4.5 10.2a1.8 1.8 0 1 1 0 3.6 1.8 1.8 0 0 1 0-3.6zM11.5 4.2a1.8 1.8 0 1 1 0 3.6 1.8 1.8 0 0 1 0-3.6zM4.5 5.8v4.4M11.5 7.8c0 2.2-2.5 2.6-5.3 3.4",
  box: "M8 1.8l5.5 3v6.4L8 14.2l-5.5-3V4.8zM2.5 4.8L8 7.9l5.5-3.1M8 7.9v6.3",
  bucket:
    "M2.5 4.5h11l-1.3 8.2a1.5 1.5 0 0 1-1.5 1.3H5.3a1.5 1.5 0 0 1-1.5-1.3zM2.5 4.5c0-1.4 2.5-2.5 5.5-2.5s5.5 1.1 5.5 2.5",
  cube: "M8 2l5 2.8v6.4L8 14l-5-2.8V4.8zM3 4.8l5 2.8 5-2.8M8 7.6V14",
  globe:
    "M8 1.7a6.3 6.3 0 1 1 0 12.6A6.3 6.3 0 0 1 8 1.7zM1.7 8h12.6M8 1.7c1.7 1.8 2.5 3.9 2.5 6.3S9.7 12.5 8 14.3C6.3 12.5 5.5 10.4 5.5 8S6.3 3.5 8 1.7z",
  chat: "M2.5 3.5h11v7.5h-6l-3.5 2.5V11h-1.5z",
  key: "M5.5 7.2a3 3 0 1 1 0 .1zM8.3 8.3l5.2 5.2M11.2 11.2l1.6-1.6",
  list: "M2.5 4h11M2.5 8h11M2.5 12h11",
  filter: "M2.5 4h11M4.5 8h7M6.5 12h3",
  copy: "M5.5 5.5h7v7h-7zM3.5 10.5v-7h7",
  external: "M9 2.5h4.5V7M13.5 2.5l-6 6M12 9.5v3a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h3",
  logout: "M6 13.5H3.5v-11H6M10.5 11l3-3-3-3M13.5 8H6",
  check: "M3.5 8.5l3 3 6-7",
  shield: "M8 1.8l5 2v4c0 3-2.2 5.3-5 6.4-2.8-1.1-5-3.4-5-6.4v-4z",
  clock: "M8 1.7a6.3 6.3 0 1 1 0 12.6A6.3 6.3 0 0 1 8 1.7zM8 4.5V8l2.5 1.5",
  db: "M3 4c0-1.1 2.2-2 5-2s5 .9 5 2-2.2 2-5 2-5-.9-5-2zM3 4v8c0 1.1 2.2 2 5 2s5-.9 5-2V4M3 8c0 1.1 2.2 2 5 2s5-.9 5-2",
  nodes:
    "M3 2.5h3.5a.5.5 0 0 1 .5.5v3.5a.5.5 0 0 1-.5.5H3a.5.5 0 0 1-.5-.5V3a.5.5 0 0 1 .5-.5zM9.5 2.5H13a.5.5 0 0 1 .5.5v3.5a.5.5 0 0 1-.5.5H9.5a.5.5 0 0 1-.5-.5V3a.5.5 0 0 1 .5-.5zM6.2 9H9.8a.5.5 0 0 1 .5.5V13a.5.5 0 0 1-.5.5H6.2a.5.5 0 0 1-.5-.5V9.5a.5.5 0 0 1 .5-.5z",
  scale: "M2.5 8h11M2.5 8L5 5.5M2.5 8L5 10.5M13.5 8L11 5.5M13.5 8L11 10.5",
  plug: "M5.5 2v3M10.5 2v3M4 5h8v2.5a4 4 0 0 1-8 0zM8 11.5V14",
  route: "M2.5 8h4.5l2-4.5h4.5M7 8l2 4.5h4.5M12 1.8l1.7 1.7L12 5.2M12 10.8l1.7 1.7L12 14.2",
  disk: "M3.5 2.5h9a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1zM2.5 9.5h11M11 11.6v.1",
  file: "M4 1.8h5l3 3v9.4H4zM9 1.8v3h3",
  sun: "M8 5a3 3 0 1 1 0 6 3 3 0 0 1 0-6zM8 1.5v1.2M8 13.3v1.2M1.5 8h1.2M13.3 8h1.2M3.4 3.4l.9.9M11.7 11.7l.9.9M12.6 3.4l-.9.9M4.3 11.7l-.9.9",
  send: "M2.5 8h9M8 4l4 4-4 4",
  ns: "M5 2.5H3.5a1 1 0 0 0-1 1V5M11 2.5h1.5a1 1 0 0 1 1 1V5M5 13.5H3.5a1 1 0 0 1-1-1V11M11 13.5h1.5a1 1 0 0 0 1-1V11M8 8h.1",
  stack:
    "M3.5 2.5h9a1 1 0 0 1 1 1v2a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-2a1 1 0 0 1 1-1zM3.5 9h9a1 1 0 0 1 1 1v2a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-2a1 1 0 0 1 1-1zM5 4.5h.1M5 11h.1",
  shieldCheck: "M8 1.8l5 2v4c0 3-2.2 5.3-5 6.4-2.8-1.1-5-3.4-5-6.4v-4zM5.8 8l1.6 1.6L10.4 6.6",
  wall: "M2.5 3.5h11v9h-11zM2.5 6.5h11M2.5 9.5h11M6.5 3.5v3M10 6.5v3M6.5 9.5v3",
  badge:
    "M3 3.5h10a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1zM5.6 6a1.2 1.2 0 1 1 0 2.4 1.2 1.2 0 0 1 0-2.4zM3.8 11c.3-1.2 1-1.8 1.8-1.8s1.5.6 1.8 1.8M9 6.5h3M9 9h2",
  server: "M3 3h10v3.5H3zM3 9.5h10V13H3zM5 4.8h.1M5 11.2h.1M8 6.5v3",
  claim: "M3.5 2.5h9v11h-9zM5.8 5.5h4.4M5.8 8h4.4M5.8 10.5h2.2",
  chip: "M5 5h6v6H5zM6.5 2v3M9.5 2v3M6.5 11v3M9.5 11v3M2 6.5h3M2 9.5h3M11 6.5h3M11 9.5h3",
  vault:
    "M3 2.5h10a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1zM8 5.2a2.3 2.3 0 1 1 0 4.6 2.3 2.3 0 0 1 0-4.6zM5 12.5v1.2M11 12.5v1.2",
  upload: "M8 13V5M4.5 8.5L8 5l3.5 3.5M4 2.5h8",
} as const;

export type IconName = keyof typeof PATHS | keyof typeof BRAND;

export function Icon({
  name,
  className = "size-4 shrink-0",
  label,
}: {
  name: IconName;
  className?: string;
  label?: string;
}) {
  if (name in BRAND) {
    const brand = BRAND[name as keyof typeof BRAND];
    return (
      <svg
        className={className}
        viewBox={brand.viewBox}
        fill="currentColor"
        role={label ? "img" : undefined}
        aria-label={label}
        aria-hidden={label ? undefined : true}
      >
        <path d={brand.d} />
      </svg>
    );
  }
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <path d={PATHS[name as keyof typeof PATHS]} />
    </svg>
  );
}
