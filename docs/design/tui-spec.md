# tokenomnom TUI: Dense Desk

Status: layout foundation for quests 145-152.

Direction A is a dense desk: the terminal is a working surface, not a stack of
cards. The shell stays quiet, repeated values line up, and every page gets a
bounded viewport. Pages may become richer later, but they must not change the
navigation, loader, or request model to do it.

## 1. Vocabulary

The shell has a frame, global chrome, a body, and an ambient rail.

- `W`, `H`: the requested terminal width and height.
- `IW`: inner frame width, `W - 2` because the shell keeps one cell of padding
  on either side.
- `BH`: body height after global chrome.
- `RW`: rail width, including its divider.
- `CW`: page content width.
- `Band`: a full-width horizontal region with exact width and height.
- `Pane`: a borderless region inside a band. A pane title consumes one row and
  ends in a rule; panes never resize themselves to fit their content.
- `B1`, `B2`, `B3`: the page's vertical bands, from primary data to supporting
  analysis to the lowest-height detail or warning band.

The implementation lives in `internal/tui/layout.go`. `RenderBand` is pure and
can be used by a page without starting a loader or reading a store.

## 2. Tiers

Width and height are independent. Boundary values belong to the larger tier.

| Tier | Width | Height |
| --- | --- | --- |
| floor / short | `< 100` | `< 30` |
| standard | `100 <= W < 160` | `30 <= H < 50` |
| wide / tall | `160 <= W` | `50 <= H` |

The code names are `WidthFloor`, `WidthStandard`, `WidthWide`,
`HeightShort`, `HeightStandard`, and `HeightTall`. Use `WidthTierFor` and
`HeightTierFor` instead of duplicating thresholds in a page.

The floor is a real layout, not a scaled-down wide layout. It removes the
rail, gives the page the full inner width, and puts this compact hint in the
top bar:

```text
FLOOR 80x24  ·  1-8 pages
```

## 3. Global chrome and arithmetic

The rows are fixed before a page is rendered:

```text
top bar       1
summary       1
size badge    1 at standard/wide; inline in the floor top bar
body          BH
status        1
footer        3
```

`BH = H - chrome.Total()`. The frame must have exactly `H` rows and exactly
`W` cells on every row. The current page is passed `CW` and `BH` and is
rendered inside one untitled compatibility band. Existing page content stays
unchanged; later pages can add a visible band title and panes without changing
the shell arithmetic.

The size badge names the horizontal tier and viewport, for example
`STANDARD 120x40`. The status bar is one right-aligned segment. Optional status
facts are removed from right to left when the segment is too wide; the sync
state and the first warning remain.

### 3.1 Ambient rail

The rail is a stat column, not a second navigation page. It contains these
blocks, in this order:

1. Navigation groups and page numbers.
2. `FILTERS`: provider and range.
3. `SNAPSHOT`: total, tokens, and active days from the already loaded summary.
4. `MIX`: current provider, range, and sync state.
5. `PROJECTS`: current project options from the already loaded sessions page.

At standard width `RW = min(22, max(18, IW/5))`; at wide width
`RW = min(30, max(24, IW/6))`. `CW = IW - RW - 2` when the rail is present.
The rail keeps the navigation and `FILTERS` blocks first. Optional blocks are
added from top to bottom and dropped from the bottom as soon as the available
height is reached. This prevents a lower block from jumping above a block that
was dropped. At floor width the rail is absent and no rail data is loaded.

### 3.2 Warnings

Warnings are one-row treatments: a warning marker, receiver-readable text,
and truncation at the owning width. `WarningRow` is the shared primitive. A
warning never creates a new row outside the band budget and never displaces
the sync state from the status bar.

## 4. Band, chart, and density primitives

Band titles are uppercase, occupy one row, and use a trailing horizontal rule.
Pane titles use the same rule inside their pane. Panes are separated by a
two-cell gap and contain no rounded boxes. `RenderBand` assigns the remaining
width exactly, including the gap, and pads or truncates every row.

### Share bars

`ShareBar` accepts a raw value and total. It normalizes against the total,
reserves the label width, and pads to the requested width. Callers do not need
to precompute percentages.

### Sparklines

`Sparkline` uses eight stable levels from low to high and keeps the newest
values when the input is wider than the requested cell count. Empty input is
safe. A sparkline is a compact trend signal, not a substitute for a labeled
chart.

### Intensity cells

`IntensityCells` maps non-negative values to `.`/`░`/`▒`/`▓`/`█`, using the
largest value in the supplied slice as the denominator. Zero stays visibly
empty and positive values never disappear.

### Full-range charts

Charts show the requested range rather than silently cropping to a library
default. For `N` points and a column width `colW`:

```text
colW  = clamp(1, 4, floor((CW - (N - 1)) / N))
chartW = N * colW + (N - 1)
```

`ChartColumnWidth` and `FullRangeChartWidth` implement this arithmetic. If the
requested `chartW` is wider than the current viewport, the page must make an
explicit tier decision about aggregation or scrolling. It must not let a chart
library crop the tail without saying so.

## 5. Page migration contract

Quest 145 only supplies the shell. Existing pages remain their current
content inside one band; their keys, zoom behavior, async loaders, and request
fields remain unchanged. The current `Snapshot.Views` strings are still the
source for the Daily, Models, and Heatmap pages, while Ledger, Sessions, Vault,
System, and Search keep their existing page adapters.

Later quests may use the same shell to add:

- Daily: full-range chart, analysis panes, and cursor-day sessions.
- Ledger: period master-detail and reusable session preview.
- Models: dense master table and model-by-day matrix.
- Heatmap: scaled year grid and profile panes.
- Sessions and Search: master-detail and two-column session detail.
- Vault and System: two-column compositions.
- Overlays: grouped wide help and palette treatments.

Those pages may add DATA fields only where their quest lists an existing-table
query. Quest 145 adds no store query and no new persistence.

## 6. Evidence and test contract

The reference viewports are `192x66`, `120x40`, and `80x24`. Every final page
must satisfy:

- exact fill: exactly the requested width on every row and exactly the
  requested height;
- band arithmetic: the sum of band heights plus global chrome equals `H`;
- height-derived lists: row capacity comes from `BH` or the page pane height,
  never from a fixed terminal-size guess;
- no void at wide+tall: a populated band may not leave more than three
  consecutive blank content rows;
- keyboard-only behavior: Tab, Shift-Tab, arrows, Enter, Escape, and the
  existing page commands continue to work.

Committed frames under `docs/design/frames/` are cell-exact snapshots. Each
frame includes `Source:` and `Command:` provenance so a later quest can tell
whether it is a hand mockup or a rendered test artifact. Quest 145's frames are
the shell reference; later quests add page-specific frames without rewriting
this contract.
