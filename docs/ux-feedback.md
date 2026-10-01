# UX feedback and motion

How the web UI answers every change of state. There are two sources: what the user does,
and what the cluster reports over SSE. Each change gets feedback at up to three moments:

- **Immediate:** the control reacts before the network answers.
- **Confirmation:** the hub accepted the request.
- **Result:** the cluster reports the new state.

Motion uses the tokens in `design/tokens.css`:

| Token | Value |
|---|---|
| `--duration-fast` | 120 ms |
| `--duration-base` | 180 ms |
| `--duration-slow` | 260 ms |
| `--duration-flash` | 1.2 s |
| `--ease-standard` | settles softly |
| `--ease-emphasized` | overshoots slightly |

The classes live in `web/src/styles/app.css`. Under `prefers-reduced-motion` every duration
is 0. JavaScript that would wait for an exit skips the wait, so nothing animates and every
change lands at once.

## User actions

| Action | Immediate | Confirmation | Result |
|---|---|---|---|
| Reconcile (`r`), with source (`R`) | The button shows a spinner and "Requesting…", and is disabled. The row and header show a "Reconcile requested" badge. | Toast "Reconcile requested: Kind/name". A 403, 428 or 503 gives a red toast and clears the badge. | The status cell swaps in the new status. The row flashes in its colour, for example Reconciling (blue) and then Ready or Completed (green). The badge clears on the first status, message or last-change update. If none arrives within 30 s: toast "No change reported yet… check Events". |
| Suspend / resume (`s`) | The button shows a spinner and "Suspending…" or "Resuming…". The row shows a "requested" badge. On a protected cluster, typed confirmation comes first. | Toast "Suspended: Kind/name" with **Undo**, which resumes or suspends again. | The status swaps to Suspended (grey flash) or back. The badge clears. |
| Action on a disconnected or read-only cluster | Buttons are disabled. A key press gets a toast explaining why. | None | None |
| Create thread | "Start thread" is disabled while it sends. | Toast "Thread created". The composer closes. | The list refetches through the SSE `thread` event. |
| Reply | Send is disabled while it sends. | The draft clears. | The message appears after the refetch. |
| Resolve / reopen thread | The button shows a spinner and "Resolving…". | Toast "Resolved “title”" with **Undo**. | The thread moves between the Open and Resolved lists. |
| Delete thread | Not in the web UI (author only, API and MCP) | None | None |
| Create token | "Create" is disabled while it sends. | The token is shown once in a panel, with Copy. | The table refetches. |
| Revoke token | The button is disabled while it sends. | Toast "Token revoked". | The row leaves the table after the refetch. |
| Add cluster | "Adding…" on the submit button. | Step 2: the install guide and join token. | The live checklist ticks as the agent connects. The cluster card appears through the `clusters` event. |
| Regenerate join token | "Issuing…" | Toast, and the new guide inline | None |
| Delete cluster | Two-step: the button arms ("Confirm delete"), or typed confirmation when protected. | Toast "name deleted". The dialog closes. | The card leaves the fleet. |
| Cluster colour | The swatch ring moves. The preview tile updates. A contrast warning appears when needed. | Wizard: saved with the cluster. Connection dialog: "Save colour", then a toast. | The tiles, chips and ribbon take the colour. `--c` animates over 0.7 s. |
| Pin / unpin | The star fills or empties at once (optimistic, local first). | Saved to prefs after 5 s (debounced) | The cluster order changes. |
| Ask AI | The question appears in the transcript. "Thinking…" shows and Send is disabled. | None | The answer appears in the transcript. Errors show inline. |
| Ask AI from logs (toolbar, or a selection's "Ask AI about selection" / "Ask AI with context") | Opens a new chat. The composer shows a lines chip and the pre-filled question, selected. | The sent message keeps an "attached" chip. | The answer appears. |
| Tab, segment, list view, palette scope | The indicator slides (260 ms). A detail tab panel slides in from the side of the new tab. | None | Saved per user (`view` prefs): the list view (`listView`: `grouped`, `flat`, or on Flux pages `graph` and `outline`), the detail page's Manages view (`managesView`: `tree`, `graph` or `outline`), the graph's focus hops (`graphHops`, 1–3), the log format, follow, tail/since, panel width, panel tab, nav groups and the theme. The URL wins when it names a view (`?view=`). A saved `graph` or `outline` reads as `grouped` on a page without Flux kinds, and every view reads as `flat` on a windowed list (more than 25k rows). |
| View switch (Tree \| Graph \| Outline, Grouped \| Flat \| Graph \| Outline) | One segmented control; Outline is a view, not a separate button. Focus rings are inset, so they never touch a neighbour. | None | The section header stays on one line. As it narrows, the caption hides first, then the "18 nodes · 32 edges" meta moves into the control's tooltip, then the control shows icons only (each button keeps its aria-label). |
| ⌘K, This cluster / All clusters | This cluster ranks the list the page already loaded, at once. All clusters asks the hub (`GET /search`) after 120 ms without typing; a spinner replaces the search icon and the group reads "searching…". | None | The newer query aborts the one in flight. Earlier results stay, dimmed, while they can still match. ↑ ↓ and Enter keep working on what is shown. A hub without `/search` gets the old way (every snapshot, ranked in the browser). |
| Filters, status chips | Instant. Rows do not glide on filter changes (speed first). | None | None |
| Cluster switch | The identity colour cross-fades (0.7 s). | None | None |
| Modal, popover, Select | Fade and scale in from 0.98 (180 ms). | None | None |
| Toasts | Slide up in (260 ms, emphasized). Slide out (180 ms) at 3.4 s, or 6 s with an action. | None | None |
| Narrow-screen Ask AI sheet | Slides in from the right. | None | None |

## Server-driven changes (SSE)

| Change | Feedback |
|---|---|
| Status transition (`change` upsert) | The status icon and label swap in (scale and fade, 180 ms). The row, or the detail header, flashes in the new status colour for 1.2 s on an overlay, so the selection and hover backgrounds are untouched. The message fades to its new text. |
| Message only | The message fades to its new text. No flash. |
| Row appears | It slides and fades in. The rows below glide down (transform, 180 ms). |
| Row deleted | It stays for 180 ms as a leaving row: it fades and collapses, cannot be selected, and is not counted. Then the rows below glide up. On a detail page the object stays until deleted, then reads "Kind/name was deleted" instead of "not found". |
| Re-sort after a status change | The moved row glides to its new place during the settle window. |
| Bulk delta (more than 20 rows) or `resync` refetch | No per-row motion, only the update. |
| First load | Counts first: the sidebar, the status chips and the total come from `ClusterInfo.counts` and `kinds` (skeleton digits while `countsPending`). Then 12 skeleton rows (40 px, static under reduced motion) until the first page of names. No motion. |
| Large cluster (paged hub) | Up to 25k rows: the first 200 rows show at once and the full list loads behind them. Above 25k the list stays windowed: rows on screen load 500 at a time as skeleton rows turn into names, the hub filters and sorts, and live changes refetch the pages on screen at most every 2 s instead of animating rows. |
| Graph (list Graph view, detail Manages graph) | Live like the list: a node's status swaps in and its box flashes in the new colour; a node waiting on a dependency gets the amber "waiting" outline and message. A reconcile marches the edges downstream of the object (animated dashes) until the wave settles: 4 s quiet after the root has its result, at most 45 s. Under reduced motion the dashes and flashes are off and changes are instant. |
| Cluster disconnects | The last known rows and details stay, greyed (grayscale and 55% opacity, 260 ms). The banner reads "Stale · last seen 2m ago…". Actions are disabled, and keys explain why. |
| Cluster reconnects | The greying fades out. The refetch lands without per-row flashes. |
| Findings (`clusters` event, or `attention` with `findings`) | The callout above the Jobs list and the "Needs attention" card update in place. They are calm: an amber outline, no animation. |
| Completed | A muted green outline check and a quiet green box outline on the detail header. Distinct from Ready (a filled check). |

### Live updates across clusters (ADR-0006)

- **Full live updates only for what is on screen.** The stream watches the route's cluster and the side panel's cluster when it differs (`watch=`, at most 5; recent clusters stay watched so going back does not reconnect). Rows, details and the graph of those clusters move live as above.
- **Every other cluster is live in summary.** Its counts (sidebar, fleet cards, status chips) and its "Needs attention" rows (fleet table, ⌘K empty state) update from `counts` and `attention` events, without row motion. Its names are found through ⌘K → All clusters (`GET /search`), never by loading its list.
- **Changing what is watched reconnects the stream**, 300 ms after the route settles. The hub then sends `resync` for each watched cluster, so nothing is missed; a cluster that leaves the set keeps its rows in the cache, marked stale, and refetches when it is shown again.
- **The fleet page watches nothing**: counts and attention only.
- **Disconnected clusters** keep their last view, greyed and labelled "Stale · last seen 12m ago", in the list, the fleet cards and search results.

Implementation:

- `web/src/api/stream.tsx`: the watch set and the stream; `web/src/api/delta.ts`: applying `change`, `counts` and `attention`.
- `web/src/lib/useClusterList.ts`: first page, background fill and windowed pages.
- `web/src/lib/liveMotion.ts`: detection from SSE deltas, the bulk limit, leaving rows and requested badges.
- `web/src/components/ResourceList.tsx`: the row classes.
- `web/src/components/TabIndicator.tsx`: the indicator.
- `web/src/components/Toasts.tsx`: exits and actions.

## Key map

Bindings live in `web/src/lib/keys.ts`. `components/keyHints.test.tsx` fails if two
bindings share a key (optional Shift included), or if one screen hints the same key for
two actions.

| Keys | Action | Where |
|---|---|---|
| `j` `k` / `↓` `↑` | Move down / up. In logs they scroll. | Lists, logs |
| `l` `↵` `→` | Open the selected item | Lists |
| `h` `esc` `←` | Go back up | Everywhere. In the nav tree, `→` and `←` expand and collapse instead. |
| `g g` / `G` | Top / bottom. In logs `G` follows again. | Lists, logs |
| `u` | Jump to the owner | Lists, detail |
| `[` `]` | Previous / next page in the nav group | Cluster list |
| `{` `}` | Previous / next cluster | Everywhere |
| `1`…`9` | Switch to cluster 1…9 | Everywhere |
| `g f` `g t` `g k` `g a` | Fleet, Threads, Tokens, Audit | Everywhere |
| `⌘K` / `Ctrl K` | Search every cluster | Everywhere |
| `:` | Commands only | Everywhere |
| `/` | Filter this list | Cluster list |
| `a` | Ask AI (again: back to Details) | Cluster and detail |
| `d` | Details panel | Cluster list |
| `?` | Keyboard shortcuts | Everywhere |
| `n` | Add a cluster | Fleet |
| `r` / `R` | Reconcile / with source | Selection |
| `s` | Suspend or resume | Selection |
| `L` | Logs tab | Pods, and Deployments, StatefulSets, DaemonSets and Jobs when `features.workloadLogs` is on |
| `o` `e` `y` | Overview, Events, YAML tabs | Selection |
| `t` | Threads tab only, with no side effects | Selection |
| `c` | New thread | Threads tab only (the button shows the hint) |
| `f` | Toggle follow | Logs |
| `0` | Fit the graph to the screen | In the graph |
| `+` / `=` | Zoom in | In the graph |
| `-` | Zoom out | In the graph |
| `⇧F` | Focus on the selected node and its neighbours (again: the whole graph). `esc` also leaves focus. | In the graph (list Graph view) |
| `←` `↓` `↑` `→` / `h` `j` `k` `l` | Move along edges: left to what goes first, right to what follows, up and down within a step. `↵` opens; `l` and `→` move instead. | In the graph, while it has focus |
| `⇧↑` `⇧↓` | Extend the line selection | Focused log list |

Audit notes:

- `t` used to open the Threads tab and start a thread. It is now split into `t` and `c`.
- `l` and `L` are different keys: `L` needs Shift.
- `a` and `d` only switch the side panel.
- `f` does not clash with `g f`: sequences are matched first.
- `[` `]` are page keys and `{` `}` (Shift) are cluster keys.
- `n` is bound on the fleet page only.
- The graph keys reuse the Move around keys while the graph has focus. `0`, `+` `=` and `-` are off in the Outline view.
- Inside ⌘K, `↑` `↓` (and `Ctrl N` / `Ctrl P`, `Ctrl J` / `Ctrl K`) move and wrap, `↵` runs, `Tab` switches This cluster / All clusters, and `1`…`9` switch cluster while the input is empty. These are the combobox's own keys, not global bindings.

## Flat-list sorting

- **Headers:** in the flat list, column headers sort the list: ascending, then descending, then back to the default attention-first order. Grouped view keeps plain labels.
- **Remembered:** the sort is saved per list page (`listSort` in view prefs) and mirrored in the URL as `?sort=&order=`. The URL wins when present.
- **Clusters up to 25k rows:** sorting is instant and local.
- **Windowed clusters (over 25k rows):** the hub sorts name, kind, status and age and the pages refetch. Message, version and ready columns are disabled there, with a tooltip.
