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
top divider   1
body          BH
bottom divider 1
status        1
footer hints  1
disclaimer    1 (badge right-aligned here)
```

At standard and wide widths, `BH = H - 7`. At floor width the disclaimer is
folded into the final footer row, so `BH = H - 6`. The frame must have exactly
`H` rows and exactly `W` cells on every row. The current page is passed `CW` and
`BH` and is rendered inside one untitled compatibility band. Existing page
content stays unchanged; later pages can add a visible band title and panes
without changing the shell arithmetic.

The size badge names the viewport and tiers, for example `120x40 · standard`,
and is right-aligned on the disclaimer row. Floor keeps the page-navigation
hint in the top bar and combines its footer hints with the badge. The status
bar is one right-aligned segment. Its optional metadata is
`last sync <t> · <n> sources · <m> models`; optional facts are removed from
right to left when the segment is too wide, while sync state and warnings
remain.

### 3.1 Ambient rail

The rail is a stat column, not a second navigation page. It contains these
blocks, in this order:

1. Navigation groups and page numbers.
2. `FILTERS`: provider, range, and project.
3. `SNAPSHOT`: today, 7d, 30d, and peak plus date from the loaded daily data.
4. `MIX · 30D`: two provider share bars.
5. `PROJECTS 30D`: top projects with session share and a micro bar.

At standard and wide widths `RW = 20`, including the divider. The page content
uses the fixed contract width `CW = W - 21` before the shell's outer frame
padding is applied. The implementation reduces `CW` by the shell gap so the
rail and page always exact-fill the padded inner frame.
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

## 5. Per-page contracts

### 5.1 Daily
- wide+tall (frame 1): three bands.
  - **B1 chart** rows: `clamp(14, BH*0.30, 18)` — full 30-day range, peak label in title, avg line.
  - **B2 analysis** rows: 17. Panes: `DAY DETAIL` (provider split w/ share bars, `MODELS BY COST`
    list, `DAY vs 30-DAY AVERAGE` 3 comparison rows) ≈ 70 cols · `PROJECTS` for cursor day +
    `30-DAY TRENDS` (4 sparkline rows w/ 7dΔ) ≈ 50 cols · `LAST 10 DAYS · RESCALED` mini chart,
    remainder.
  - **B3 sessions** rows: remainder (`BH - B1 - B2 - 2 rules`). Cursor-day session table, columns
    TIME PROV PROJECT SESSION MODEL TOKENS COST PR FIRST-PROMPT; row count = available rows (no pager
    when it fits, else `↓ N more`); attribution warning row last.
  - DATA: B3 needs the day session list already used by ledger drill-down (#116 loader). TRENDS
    sparklines derive from existing daily aggregates; `claude share` trend derivable.
- standard (frame 5): B1 chart 14 rows colW=2; B2 two panes; B3 top-3 by cost + `↓ N more` hint.
- floor (frame 6): chart 12 rows + DAY band (2 provider rows + top-model line + warning). No sessions.

### 5.2 Ledger
- wide+tall (frame 2), year/month level: 
  - **B1 periods** (rows: 17): master table pane (`PERIOD SESSIONS TOKENS CODEX CLAUDE TOTAL Δ ACTIVITY`)
    with **all periods of the zoom level rendered** (empty months keep zero-shade rows — the grid
    holds its shape) · right side pane 57 cols: `PERIOD DETAIL` (selected period summary), then
    `MODELS · ALL TIME`, `PRICING PROVENANCE`, `COST PER 1M`, `PROVIDER × MONTH`, `ZOOM STACK`
    (breadcrumb list w/ key hints) stacked to fill height.
  - **B2 period chart** rows 16: `SPEND BY MONTH` full-width-minus-side-pane, avg line, month totals
    caption row.
  - **B3 profiles** rows: remainder. Panes: `PROJECTS · <period>` table + `PROJECT × MONTH` intensity
    matrix · `WEEKDAY PROFILE` + `HOUR OF DAY` histogram.
  - Breadcrumb row above B1: `ALL YEARS › 2026 › month › day › sessions` + zoom key hints.
  - DATA: weekday profile, hour-of-day histogram, project×month matrix, per-period Δ — new store
    queries (all derivable from usage_daily + history catalog).
- day level (frame 3): master-detail. Left: day header (rank vs avg, warning) + full session table
  (fills height). Right side pane 57 cols: `SESSION` preview of cursor row — first prompt block,
  `OVERVIEW` key-values, `COST & TOKENS` — enter opens full detail. Cursor move repaints preview
  client-side (no reload).
- standard: B1 + B2 only; side pane drops; day level keeps preview if `CW ≥ 110`, else list only.
- floor: current v0.5 ledger behavior (list only, existing compact layout).

### 5.3 Models
- wide+tall (frame 4): three bands.
  - **B1 master table** rows: `models + 2`: PROV MODEL TOKENS TOK% share COST COST% share $/1M
    PRICING SESSIONS DAYS FIRST LAST + 10-cell 30-day sparkline; TOTAL row.
  - **B2 analysis** rows 24: panes: `TOKENS ↔ COST` butterfly + `COST CONCENTRATION` (cumulative
    top-N) · `PROVIDER ROLLUP` + `PRICING PROVENANCE` + `TOKENS PER SESSION` · `COST PER 1M` +
    `UNPRICED MODELS` + `RECENCY` (days since last use).
  - **B3 model×day matrix** rows: remainder: intensity cell per model per day + 30-day cost column.
  - DATA: recency, tokens/session, concentration derive from existing aggregates; model×day needs
    day×model matrix (exists: usage_daily).
- standard: B1 (sparkline column dropped if CW<150) + B2 as 2 panes; no B3.
- floor: current compact model list.

### 5.4 Heatmap (not mocked; extend in A's vocabulary)
- wide+tall: **B1 year grid** with 3-char intensity cells, weekday rows labeled, month header,
  right column `MONTH Σ` totals; **B2 panes**: `WEEKDAY PROFILE` · `STREAKS & RECORDS` (busiest day,
  longest streak, active/total) · `MONTH TABLE` (cost, tokens, active days). Grid height = 7 rows ×
  cellH(1) + headers; surplus height goes to B2.
- standard: 2-char cells, B2 two panes. floor: current dot grid + summary line.

### 5.5 Sessions
- wide+tall: master-detail. Left: session table (fills height; page size = available rows, cursor
  paging beyond); right side pane 57 cols: session preview (same component as ledger day preview).
- standard: table only (today's layout, but height-derived page size). floor: current compact.

### 5.6 Session detail (full page, from enter)
- wide: two-column: left `FIRST PROMPT` full block + `PROMPTS` list; right `OVERVIEW` + `PROVENANCE`
  + `COST & TOKENS` + `MODELS` table (per-model splits from #114 data). standard/floor: current
  single column.

### 5.7 Search
- wide+tall: input row + results list (fills height) + right preview pane 57 cols showing the
  selected hit: matched prompt in context (± surrounding prompts), session overview, `enter` opens
  detail, `e` export. standard: today's list (height-derived count). floor: current.
- DATA: preview needs prompt-context query (exists for detail view; reuse).

### 5.8 Vault
- wide: **B1 two panes**: `ARCHIVE HEALTH` key-values + verify action · `STORAGE` — raw vs stored
  bar, ratio, reclaimable, last archive/verify timeline. **B2**: `BUNDLES` table (recent bundles:
  date, files, raw→stored, status) filling remaining height.
- DATA: bundle listing query (vault manifest already has it).
- standard/floor: current layout.

### 5.9 System
- wide: **B1 two panes**: `DOCTOR` status key-values · `WARNINGS` (wrapped, warn color).
  **B2**: `EFFECTIVE PRICING` full-width table (as today but aligned columns, fills width).
  **B3** (tall only): `SCHEDULE & SOURCES` pane (schedule status, per-source file counts/sizes).
- standard/floor: current layout.

### 5.10 Overlays
- **Help**: at wide, two-column grouped cheat-sheet (groups: navigate / pages / actions / system),
  box width `min(110, W-8)`; dim = faint base view, **not** blank. esc/? close (quest #143 shipped).
- **Palette**: box width `min(90, W-8)`, list rows `min(items, BH-8)`; unchanged behavior.

## 6. Status bar addition

Right-aligned segment on row H-2: `last sync <rel> · <n> sources · <m> models` — rendered only when
it fits beside the left segments (left segments win). DATA: all three facts already in ambient cache.

(Shipped in quest 145; kept for reference.)

## 7. Test contract (every implementation quest)

1. Exact-fill assertions at 192×66, 120×40, 80×24: rendered frame is exactly W×H, every line exactly
   W cells, styled and unstyled.
2. **No-void assertion**: at wide+tall with populated fixtures, no band contains a run of
   > 3 consecutive all-blank content rows (the anti-regression for the audit's core finding).
3. Band arithmetic: `Σ band heights + rules == BH` asserted per tier.
4. Fill-derived list sizes: session/list row count changes when H changes (test two heights).
5. Existing behavior tests keep passing untouched (keys, zoom, loaders).
6. Evidence: committed frame snapshots per tier from a seeded snapshot test (repo convention:
   Source:/Command: headers) + a live capture at 192×66 in the PR.

## 8. Out of scope (explicitly)

Chart windowing changes beyond full-range rendering; new key bindings; new CLI surfaces; palette
changes; behavior of loaders (only *what* is loaded gains fields); anything touching store schema
(new queries read existing tables).

## 9. Appendix: page migration contract (quest 145 foundation)

Quest 145 supplies the shell and its bounded ambient facts. Existing pages remain their current
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
query. Quest 145 adds one bounded history-index project-population query for
the rail and no new persistence.
