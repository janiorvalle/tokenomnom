# Dense Desk audit

Quest 145 audits the current TUI before page-specific densification. The
decision is to change the shell and leave page-owned content alone.

## Current shell

| Surface | Current behavior | Foundation decision |
| --- | --- | --- |
| Width and height | One cockpit layout with a fixed rail minimum | Independent width and height tiers |
| Global rows | Top bar, summary, body, status, footer | Add an explicit size badge row outside the floor tier |
| Page body | One content pane padded to the cockpit body | Render the existing page in one exact-fill band |
| Sidebar | Navigation plus filters | Rename the role to ambient rail and add snapshot, mix, projects |
| Status | Left-aligned facts | Right-align the segment and retain warning priority |
| Floor width | Rail still consumed columns | Remove the rail and show the `1-8 pages` hint |
| Data | Dashboard loader owns all store access | No new queries in the foundation |

## Page audit

| Page | Existing content | Quest 145 treatment | Follow-up |
| --- | --- | --- | --- |
| Daily | Snapshot chart and day detail | One compatibility band | Quest 146 |
| Ledger | Zoomable ledger renderer | One compatibility band | Quest 147 |
| Models | Snapshot model table | One compatibility band | Quest 148 |
| Heatmap | Snapshot year grid | One compatibility band | Quest 149 |
| Sessions | Bounded list and detail | One compatibility band | Quest 150 |
| Search | Search input, results, detail | One compatibility band | Quest 150 |
| Vault | Archive health and action | One compatibility band | Quest 151 |
| System | Doctor and pricing rows | One compatibility band | Quest 151 |

No page changes its key handler, zoom state, loader token, or async model in
this quest. The only existing test expectation adjusted is the search-input
tail assertion: the floor tier now gives the input the full inner width, so the
assertion checks that the cursor and tail remain visible rather than requiring
the old rail width's exact cut point.

## Layout risks carried forward

- Sparse compatibility bands still have blank rows at wide+tall. This is
  intentional for the foundation and is the first thing the page quests must
  replace with B1/B2/B3 content; the final no-void sweep belongs to quest 152.
- The rail's MIX block is a view of the current request and sync state until a
  later page supplies richer provider totals. It does not invent a query or a
  percentage.
- The floor top bar carries the size badge inline so the compact page retains
  the previous body height. Standard and wide tiers get a dedicated row.

## Verification source

```text
Source: internal/tui/layout_test.go::TestCockpitLayoutExactArithmeticAtReferenceSizes
Command: go test ./internal/tui -run 'TestCockpitLayoutExactArithmeticAtReferenceSizes|TestRenderBandFillsEveryCellAndKeepsPaneArithmetic' -count=1 -v
```
