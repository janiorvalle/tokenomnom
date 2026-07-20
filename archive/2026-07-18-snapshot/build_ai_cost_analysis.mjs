import fs from "node:fs/promises";
import path from "node:path";

import { SpreadsheetFile, Workbook } from "@oai/artifact-tool";

const ROOT = "/Users/janiorvalle/Documents/Codex/2026-07-18/ar";
const OUTPUT_DIR = path.join(ROOT, "outputs");
const QA_DIR = path.join(ROOT, "work", "ai-token-cost-analysis-qa");
const CODEX_CSV = path.join(
  OUTPUT_DIR,
  "codex_daily_token_usage_by_model_2026-02-03_to_2026-07-18.csv",
);
const CLAUDE_PRICING_INPUT = path.join(
  ROOT,
  "work",
  "claude-token-usage-by-model-qa",
  "pricing-input.json",
);
const OUTPUT_PATH = path.join(
  OUTPUT_DIR,
  "ai_token_usage_cost_analysis_through_2026-07-18.xlsx",
);

const rateBasisTokens = 1_000_000;

const pricing = [
  {
    provider: "OpenAI",
    model: "gpt-5.2",
    status: "Published",
    base: 1.75,
    read: 0.175,
    write5m: null,
    write1h: null,
    output: 14,
    notes: "Standard API list price. Reasoning tokens are included in output tokens. No cache-write tokens were recorded.",
    source: "https://developers.openai.com/api/docs/models/gpt-5.2",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.2-codex",
    status: "Published",
    base: 1.75,
    read: 0.175,
    write5m: null,
    write1h: null,
    output: 14,
    notes: "Standard API list price. Reasoning tokens are included in output tokens. No cache-write tokens were recorded.",
    source: "https://developers.openai.com/api/docs/models/gpt-5.2-codex",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.3-codex",
    status: "Published",
    base: 1.75,
    read: 0.175,
    write5m: null,
    write1h: null,
    output: 14,
    notes: "Standard API list price. Reasoning tokens are included in output tokens. No cache-write tokens were recorded.",
    source: "https://developers.openai.com/api/docs/models/gpt-5.3-codex",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.3-codex-spark",
    status: "User-selected proxy",
    base: 1.75,
    read: 0.175,
    write5m: null,
    write1h: null,
    output: 14,
    notes: "Uses GPT-5.3-Codex standard API pricing as requested. Spark itself has no separately published API token price.",
    source: "https://developers.openai.com/api/docs/models/gpt-5.3-codex",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.4",
    status: "Published",
    base: 2.5,
    read: 0.25,
    write5m: null,
    write1h: null,
    output: 15,
    notes: "Standard API list price. Reasoning tokens are included in output tokens. No cache-write tokens were recorded.",
    source: "https://developers.openai.com/api/docs/models/gpt-5.4",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.5",
    status: "Published",
    base: 5,
    read: 0.5,
    write5m: null,
    write1h: null,
    output: 30,
    notes: "Standard API list price. Reasoning tokens are included in output tokens. No cache-write tokens were recorded.",
    source: "https://developers.openai.com/api/docs/models/gpt-5.5",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.6-luna",
    status: "Published",
    base: 1,
    read: 0.1,
    write5m: 1.25,
    write1h: 1.25,
    output: 6,
    notes: "Standard API list price. OpenAI publishes one cache-write rate (1.25x base), shown in both write columns. Recorded writes are zero.",
    source: "https://openai.com/index/gpt-5-6/",
  },
  {
    provider: "OpenAI",
    model: "gpt-5.6-sol",
    status: "Published",
    base: 5,
    read: 0.5,
    write5m: 6.25,
    write1h: 6.25,
    output: 30,
    notes: "Standard API list price. OpenAI publishes one cache-write rate (1.25x base), shown in both write columns. Recorded writes are zero.",
    source: "https://openai.com/index/gpt-5-6/",
  },
  {
    provider: "Anthropic",
    model: "claude-fable-5",
    status: "Published",
    base: 10,
    read: 1,
    write5m: 12.5,
    write1h: 20,
    output: 50,
    notes: "Standard first-party global API list price.",
    source: "https://platform.claude.com/docs/en/about-claude/pricing",
  },
  {
    provider: "Anthropic",
    model: "claude-haiku-4-5-20251001",
    status: "Published",
    base: 1,
    read: 0.1,
    write5m: 1.25,
    write1h: 2,
    output: 5,
    notes: "Standard first-party global API list price for Claude Haiku 4.5.",
    source: "https://platform.claude.com/docs/en/about-claude/pricing",
  },
  {
    provider: "Anthropic",
    model: "claude-opus-4-8",
    status: "Published",
    base: 5,
    read: 0.5,
    write5m: 6.25,
    write1h: 10,
    output: 25,
    notes: "Standard first-party global API list price.",
    source: "https://platform.claude.com/docs/en/about-claude/pricing",
  },
  {
    provider: "Anthropic",
    model: "claude-sonnet-5",
    status: "Published",
    base: 2,
    read: 0.2,
    write5m: 2.5,
    write1h: 4,
    output: 10,
    notes: "Introductory standard price through 2026-08-31, covering all usage dates in this workbook.",
    source: "https://platform.claude.com/docs/en/about-claude/pricing",
  },
];

const detailHeaders = [
  "date",
  "month",
  "year",
  "provider",
  "model",
  "total_input_tokens",
  "base_input_tokens",
  "base_input_rate_per_1m",
  "base_input_cost_usd",
  "cache_read_tokens",
  "cache_read_rate_per_1m",
  "cache_read_cost_usd",
  "cache_write_5m_tokens",
  "cache_write_5m_rate_per_1m",
  "cache_write_5m_cost_usd",
  "cache_write_1h_tokens",
  "cache_write_1h_rate_per_1m",
  "cache_write_1h_cost_usd",
  "output_tokens",
  "output_rate_per_1m",
  "output_cost_usd",
  "reasoning_output_tokens_included_in_output",
  "total_tokens",
  "total_cost_usd",
  "pricing_status",
];

function monthName(date) {
  return new Intl.DateTimeFormat("en-US", { month: "long", timeZone: "UTC" }).format(
    new Date(`${date}T00:00:00Z`),
  );
}

function isoDate(date) {
  return date.toISOString().slice(0, 10);
}

function columnLetter(columnNumber) {
  let result = "";
  let value = columnNumber;
  while (value > 0) {
    value -= 1;
    result = String.fromCharCode(65 + (value % 26)) + result;
    value = Math.floor(value / 26);
  }
  return result;
}

function normalizeCodexRows(csvValues) {
  const [headers, ...rows] = csvValues;
  const ix = Object.fromEntries(headers.map((header, index) => [header, index]));
  return rows
    .filter((row) => row[ix.date])
    .map((row) => ({
      date: String(row[ix.date]),
      month: String(row[ix.month]),
      year: Number(row[ix.year]),
      provider: "OpenAI",
      model: String(row[ix.model]),
      totalInputTokens: Number(row[ix.input_tokens]),
      baseInputTokens:
        Number(row[ix.uncached_input_tokens]) - Number(row[ix.cache_write_input_tokens]),
      cacheReadTokens: Number(row[ix.cached_input_tokens]),
      cacheWrite5mTokens: 0,
      cacheWrite1hTokens: 0,
      outputTokens: Number(row[ix.output_tokens]),
      reasoningOutputTokens: Number(row[ix.reasoning_output_tokens]),
      totalTokens: Number(row[ix.total_tokens]),
    }));
}

function normalizeClaudeRows(rows) {
  return rows.map((row) => ({
    date: row.date,
    month: monthName(row.date),
    year: Number(row.date.slice(0, 4)),
    provider: "Anthropic",
    model: row.model,
    totalInputTokens: Number(row.inputTokens),
    baseInputTokens: Number(row.baseInputTokens),
    cacheReadTokens: Number(row.cachedInputTokens),
    cacheWrite5mTokens: Number(row.cacheWrite5mInputTokens),
    cacheWrite1hTokens: Number(row.cacheWrite1hInputTokens),
    outputTokens: Number(row.outputTokens),
    reasoningOutputTokens: Number(row.reasoningOutputTokens),
    totalTokens: Number(row.totalTokens),
  }));
}

function costParts(row, rate) {
  if (!rate || rate.status === "No public API price") return null;
  const base = (row.baseInputTokens / rateBasisTokens) * rate.base;
  const read = (row.cacheReadTokens / rateBasisTokens) * rate.read;
  const write5m = (row.cacheWrite5mTokens / rateBasisTokens) * (rate.write5m ?? 0);
  const write1h = (row.cacheWrite1hTokens / rateBasisTokens) * (rate.write1h ?? 0);
  const output = (row.outputTokens / rateBasisTokens) * rate.output;
  return { base, read, write5m, write1h, output, total: base + read + write5m + write1h + output };
}

function detailValues(rows) {
  return rows.map((row) => [
    row.date,
    row.month,
    row.year,
    row.provider,
    row.model,
    row.totalInputTokens,
    row.baseInputTokens,
    null,
    null,
    row.cacheReadTokens,
    null,
    null,
    row.cacheWrite5mTokens,
    null,
    null,
    row.cacheWrite1hTokens,
    null,
    null,
    row.outputTokens,
    null,
    null,
    row.reasoningOutputTokens,
    row.totalTokens,
    null,
    null,
  ]);
}

function styleHeader(range, fill) {
  range.format = {
    fill,
    font: { bold: true, color: "#FFFFFF" },
    horizontalAlignment: "center",
    verticalAlignment: "center",
    wrapText: true,
  };
}

function formatDetailSheet(sheet, rowCount, pricingRowByModel, tableName) {
  sheet.showGridLines = false;
  sheet.freezePanes.freezeRows(1);
  sheet.freezePanes.freezeColumns(5);
  sheet.getRange("A1:Y1").values = [detailHeaders];
  styleHeader(sheet.getRange("A1:Y1"), "#204E5A");
  sheet.getRange("A1:Y1").format.rowHeight = 42;

  const formulas = [];
  for (let offset = 0; offset < rowCount; offset += 1) {
    const row = offset + 2;
    const model = sheet.getRange(`E${row}`).values?.[0]?.[0];
    const pricingRow = pricingRowByModel.get(model);
    if (!pricingRow) throw new Error(`Missing pricing row for ${model}`);
    formulas.push([
      `='Pricing'!$D$${pricingRow}`,
      `=IF($Y${row}="No public API price","",$G${row}/'Pricing'!$M$2*$H${row})`,
      `='Pricing'!$E$${pricingRow}`,
      `=IF($Y${row}="No public API price","",$J${row}/'Pricing'!$M$2*$K${row})`,
      `='Pricing'!$F$${pricingRow}`,
      `=IF($Y${row}="No public API price","",$M${row}/'Pricing'!$M$2*$N${row})`,
      `='Pricing'!$G$${pricingRow}`,
      `=IF($Y${row}="No public API price","",$P${row}/'Pricing'!$M$2*$Q${row})`,
      `='Pricing'!$H$${pricingRow}`,
      `=IF($Y${row}="No public API price","",$S${row}/'Pricing'!$M$2*$T${row})`,
      `=IF($Y${row}="No public API price","",SUM($I${row},$L${row},$O${row},$R${row},$U${row}))`,
      `='Pricing'!$C$${pricingRow}`,
    ]);
  }

  for (let offset = 0; offset < rowCount; offset += 1) {
    const row = offset + 2;
    const f = formulas[offset];
    sheet.getRange(`H${row}:I${row}`).formulas = [[f[0], f[1]]];
    sheet.getRange(`K${row}:L${row}`).formulas = [[f[2], f[3]]];
    sheet.getRange(`N${row}:O${row}`).formulas = [[f[4], f[5]]];
    sheet.getRange(`Q${row}:R${row}`).formulas = [[f[6], f[7]]];
    sheet.getRange(`T${row}:U${row}`).formulas = [[f[8], f[9]]];
    sheet.getRange(`X${row}:Y${row}`).formulas = [[f[10], f[11]]];
  }

  const lastRow = rowCount + 1;
  sheet.getRange(`A2:A${lastRow}`).format.numberFormat = "yyyy-mm-dd";
  sheet.getRange(`C2:C${lastRow}`).format.numberFormat = "0";
  for (const columns of ["F:G", "J:J", "M:M", "P:P", "S:S", "V:W"]) {
    sheet.getRange(`${columns.split(":")[0]}2:${columns.split(":")[1]}${lastRow}`).format.numberFormat = "#,##0";
  }
  for (const columns of ["H:H", "K:K", "N:N", "Q:Q", "T:T"]) {
    sheet.getRange(`${columns.split(":")[0]}2:${columns.split(":")[1]}${lastRow}`).format.numberFormat = '"$"0.000';
  }
  for (const columns of ["I:I", "L:L", "O:O", "R:R", "U:U", "X:X"]) {
    sheet.getRange(`${columns.split(":")[0]}2:${columns.split(":")[1]}${lastRow}`).format.numberFormat = '"$"#,##0.00';
  }

  sheet.getRange(`A1:Y${lastRow}`).format.verticalAlignment = "center";
  sheet.getRange("A:A").format.columnWidth = 12;
  sheet.getRange("B:B").format.columnWidth = 12;
  sheet.getRange("C:C").format.columnWidth = 9;
  sheet.getRange("D:D").format.columnWidth = 12;
  sheet.getRange("E:E").format.columnWidth = 29;
  sheet.getRange("F:Y").format.columnWidth = 18;
  sheet.getRange("Y:Y").format.columnWidth = 22;
  sheet.getRange(`A2:Y${lastRow}`).format.rowHeight = 20;
  sheet.tables.add(`A1:Y${lastRow}`, true, tableName);
}

function aggregateByModel(rows, rateByModel) {
  const summary = new Map();
  for (const row of rows) {
    const current = summary.get(row.model) ?? {
      provider: row.provider,
      model: row.model,
      totalTokens: 0,
      baseInputTokens: 0,
      cacheReadTokens: 0,
      cacheWrite5mTokens: 0,
      cacheWrite1hTokens: 0,
      outputTokens: 0,
      base: 0,
      read: 0,
      write5m: 0,
      write1h: 0,
      output: 0,
      total: 0,
    };
    current.totalTokens += row.totalTokens;
    current.baseInputTokens += row.baseInputTokens;
    current.cacheReadTokens += row.cacheReadTokens;
    current.cacheWrite5mTokens += row.cacheWrite5mTokens;
    current.cacheWrite1hTokens += row.cacheWrite1hTokens;
    current.outputTokens += row.outputTokens;
    const parts = costParts(row, rateByModel.get(row.model));
    if (parts) {
      for (const key of ["base", "read", "write5m", "write1h", "output", "total"]) {
        current[key] += parts[key];
      }
    }
    summary.set(row.model, current);
  }
  return [...summary.values()].sort(
    (a, b) => a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model),
  );
}

async function main() {
  await fs.mkdir(OUTPUT_DIR, { recursive: true });
  await fs.mkdir(QA_DIR, { recursive: true });

  const codexCsv = await fs.readFile(CODEX_CSV, "utf8");
  const imported = await Workbook.fromCSV(codexCsv, { sheetName: "Imported" });
  const csvValues = imported.worksheets.getItem("Imported").getUsedRange(true).values;
  if (csvValues.length === 0) throw new Error("CSV import returned no rows");
  const codexRows = normalizeCodexRows(csvValues);
  const claudeRows = normalizeClaudeRows(
    JSON.parse(await fs.readFile(CLAUDE_PRICING_INPUT, "utf8")),
  );
  const allRows = [...codexRows, ...claudeRows];

  const workbook = await Workbook.create();
  const summarySheet = workbook.worksheets.add("Summary");
  const pricingSheet = workbook.worksheets.add("Pricing");
  const codexSheet = workbook.worksheets.add("Codex Costs");
  const claudeSheet = workbook.worksheets.add("Claude Costs");
  const chartDataSheet = workbook.worksheets.add("Chart Data");

  const pricingHeaders = [
    "provider",
    "model",
    "pricing_status",
    "base_input_$/1m",
    "cache_read_$/1m",
    "cache_write_5m_$/1m",
    "cache_write_1h_$/1m",
    "output_$/1m",
    "notes",
    "source_url",
  ];
  pricingSheet.showGridLines = false;
  pricingSheet.freezePanes.freezeRows(1);
  pricingSheet.getRange("A1:J1").values = [pricingHeaders];
  styleHeader(pricingSheet.getRange("A1:J1"), "#253746");
  pricingSheet.getRange(`A2:J${pricing.length + 1}`).values = pricing.map((rate) => [
    rate.provider,
    rate.model,
    rate.status,
    rate.base,
    rate.read,
    rate.write5m,
    rate.write1h,
    rate.output,
    rate.notes,
    rate.source,
  ]);
  pricingSheet.getRange("L1:M2").values = [
    ["assumption", "value"],
    ["rate_basis_tokens", rateBasisTokens],
  ];
  styleHeader(pricingSheet.getRange("L1:M1"), "#B76E22");
  pricingSheet.getRange(`D2:H${pricing.length + 1}`).format.numberFormat = '"$"0.000';
  pricingSheet.getRange("M2").format.numberFormat = "#,##0";
  pricingSheet.getRange(`A1:J${pricing.length + 1}`).format.verticalAlignment = "top";
  pricingSheet.getRange(`I2:J${pricing.length + 1}`).format.wrapText = true;
  pricingSheet.getRange("A:A").format.columnWidth = 13;
  pricingSheet.getRange("B:B").format.columnWidth = 31;
  pricingSheet.getRange("C:C").format.columnWidth = 22;
  pricingSheet.getRange("D:H").format.columnWidth = 18;
  pricingSheet.getRange("I:I").format.columnWidth = 54;
  pricingSheet.getRange("J:J").format.columnWidth = 48;
  pricingSheet.getRange("L:L").format.columnWidth = 23;
  pricingSheet.getRange("M:M").format.columnWidth = 16;
  pricingSheet.getRange(`A2:J${pricing.length + 1}`).format.rowHeight = 48;
  pricingSheet.tables.add(`A1:J${pricing.length + 1}`, true, "PricingTable");

  const pricingRowByModel = new Map(pricing.map((rate, index) => [rate.model, index + 2]));
  codexSheet.getRange(`A2:Y${codexRows.length + 1}`).values = detailValues(codexRows);
  claudeSheet.getRange(`A2:Y${claudeRows.length + 1}`).values = detailValues(claudeRows);
  formatDetailSheet(codexSheet, codexRows.length, pricingRowByModel, "CodexCostsTable");
  formatDetailSheet(claudeSheet, claudeRows.length, pricingRowByModel, "ClaudeCostsTable");

  const modelRows = pricing.map((rate) => ({ provider: rate.provider, model: rate.model }));
  summarySheet.showGridLines = false;
  summarySheet.getRange("A1:I1").merge();
  summarySheet.getRange("A1").values = [["AI Token Usage Cost Analysis"]];
  summarySheet.getRange("A1:I1").format = {
    fill: "#253746",
    font: { bold: true, color: "#FFFFFF", size: 18 },
    verticalAlignment: "center",
  };
  summarySheet.getRange("A1:I1").format.rowHeight = 34;
  summarySheet.getRange("A2:I2").merge();
  summarySheet.getRange("A2").values = [[
    "Standard API list-price equivalent only, not actual ChatGPT or Claude subscription charges. Excludes credits, plan fees, long-context, priority, batch, regional, fast-mode, and tool-call modifiers.",
  ]];
  summarySheet.getRange("A2:I2").format = {
    fill: "#E8EFF1",
    font: { color: "#253746", italic: true },
    wrapText: true,
    verticalAlignment: "center",
  };
  summarySheet.getRange("A2:I2").format.rowHeight = 42;

  for (const range of ["A4:B4", "D4:E4", "G4:H4", "A5:B5", "D5:E5", "G5:H5"]) {
    summarySheet.getRange(range).merge();
  }
  summarySheet.getRange("A4").values = [["Calculated list-price equivalent"]];
  summarySheet.getRange("D4").values = [["OpenAI subtotal"]];
  summarySheet.getRange("G4").values = [["Anthropic subtotal"]];
  summarySheet.getRange("A5").values = [["Proxy-priced tokens"]];
  summarySheet.getRange("D5").values = [["Published-priced tokens"]];
  summarySheet.getRange("G5").values = [["Rate basis"]];
  for (const range of ["A4:B4", "D4:E4", "G4:H4", "A5:B5", "D5:E5", "G5:H5"]) {
    summarySheet.getRange(range).format = {
      fill: range.endsWith("4") ? "#DCEAEC" : "#F3E8D9",
      font: { bold: true, color: "#253746" },
      verticalAlignment: "center",
    };
  }

  const summaryHeaderRow = 8;
  const firstModelRow = summaryHeaderRow + 1;
  const lastModelRow = firstModelRow + modelRows.length - 1;
  summarySheet.getRange(`A${summaryHeaderRow}:I${summaryHeaderRow}`).values = [[
    "provider",
    "model",
    "total_tokens",
    "base_input_cost_usd",
    "cache_read_cost_usd",
    "cache_write_cost_usd",
    "output_cost_usd",
    "total_cost_usd",
    "pricing_status",
  ]];
  styleHeader(summarySheet.getRange(`A${summaryHeaderRow}:I${summaryHeaderRow}`), "#204E5A");
  summarySheet.getRange(`A${firstModelRow}:B${lastModelRow}`).values = modelRows.map((row) => [
    row.provider,
    row.model,
  ]);

  for (let index = 0; index < modelRows.length; index += 1) {
    const row = firstModelRow + index;
    const sourceSheet = modelRows[index].provider === "OpenAI" ? "Codex Costs" : "Claude Costs";
    const sourceLastRow = modelRows[index].provider === "OpenAI" ? codexRows.length + 1 : claudeRows.length + 1;
    const priceRow = pricingRowByModel.get(modelRows[index].model);
    summarySheet.getRange(`C${row}:I${row}`).formulas = [[
      `=SUMIF('${sourceSheet}'!$E$2:$E$${sourceLastRow},$B${row},'${sourceSheet}'!$W$2:$W$${sourceLastRow})`,
      `=IF($I${row}="No public API price","",SUMIF('${sourceSheet}'!$E$2:$E$${sourceLastRow},$B${row},'${sourceSheet}'!$I$2:$I$${sourceLastRow}))`,
      `=IF($I${row}="No public API price","",SUMIF('${sourceSheet}'!$E$2:$E$${sourceLastRow},$B${row},'${sourceSheet}'!$L$2:$L$${sourceLastRow}))`,
      `=IF($I${row}="No public API price","",SUMIF('${sourceSheet}'!$E$2:$E$${sourceLastRow},$B${row},'${sourceSheet}'!$O$2:$O$${sourceLastRow})+SUMIF('${sourceSheet}'!$E$2:$E$${sourceLastRow},$B${row},'${sourceSheet}'!$R$2:$R$${sourceLastRow}))`,
      `=IF($I${row}="No public API price","",SUMIF('${sourceSheet}'!$E$2:$E$${sourceLastRow},$B${row},'${sourceSheet}'!$U$2:$U$${sourceLastRow}))`,
      `=IF($I${row}="No public API price","",SUM($D${row}:$G${row}))`,
      `='Pricing'!$C$${priceRow}`,
    ]];
  }

  summarySheet.getRange("C4").formulas = [[`=SUM($H$${firstModelRow}:$H$${lastModelRow})`]];
  summarySheet.getRange("F4").formulas = [[`=SUMIF($A$${firstModelRow}:$A$${lastModelRow},"OpenAI",$H$${firstModelRow}:$H$${lastModelRow})`]];
  summarySheet.getRange("I4").formulas = [[`=SUMIF($A$${firstModelRow}:$A$${lastModelRow},"Anthropic",$H$${firstModelRow}:$H$${lastModelRow})`]];
  summarySheet.getRange("C5").formulas = [[`=SUMIF($I$${firstModelRow}:$I$${lastModelRow},"User-selected proxy",$C$${firstModelRow}:$C$${lastModelRow})`]];
  summarySheet.getRange("F5").formulas = [[`=SUMIF($I$${firstModelRow}:$I$${lastModelRow},"Published",$C$${firstModelRow}:$C$${lastModelRow})`]];
  summarySheet.getRange("I5").formulas = [["='Pricing'!$M$2"]];
  summarySheet.getRange("C4:I4").format.numberFormat = '"$"#,##0.00';
  summarySheet.getRange("C5:I5").format.numberFormat = "#,##0";
  summarySheet.getRange(`C${firstModelRow}:C${lastModelRow}`).format.numberFormat = "#,##0";
  summarySheet.getRange(`D${firstModelRow}:H${lastModelRow}`).format.numberFormat = '"$"#,##0.00';
  summarySheet.getRange(`A${firstModelRow}:I${lastModelRow}`).format.rowHeight = 22;
  summarySheet.freezePanes.freezeRows(summaryHeaderRow);
  summarySheet.getRange("A:A").format.columnWidth = 13;
  summarySheet.getRange("B:B").format.columnWidth = 31;
  summarySheet.getRange("C:C").format.columnWidth = 19;
  summarySheet.getRange("D:H").format.columnWidth = 21;
  summarySheet.getRange("I:I").format.columnWidth = 23;
  summarySheet.tables.add(`A${summaryHeaderRow}:I${lastModelRow}`, true, "ModelCostSummaryTable");

  const latestDate = allRows.reduce(
    (latest, row) => (row.date > latest ? row.date : latest),
    allRows[0].date,
  );
  const trailingStart = new Date(`${latestDate}T00:00:00Z`);
  trailingStart.setUTCDate(trailingStart.getUTCDate() - 29);
  const dailyDates = Array.from({ length: 30 }, (_, index) => {
    const date = new Date(trailingStart);
    date.setUTCDate(date.getUTCDate() + index);
    return isoDate(date);
  });

  const earliestDate = allRows.reduce(
    (earliest, row) => (row.date < earliest ? row.date : earliest),
    allRows[0].date,
  );
  const monthlyPeriods = [];
  const monthCursor = new Date(`${earliestDate.slice(0, 7)}-01T00:00:00Z`);
  const lastMonth = new Date(`${latestDate.slice(0, 7)}-01T00:00:00Z`);
  while (monthCursor <= lastMonth) {
    monthlyPeriods.push({
      label: monthCursor.toISOString().slice(0, 7),
      year: monthCursor.getUTCFullYear(),
      month: new Intl.DateTimeFormat("en-US", { month: "long", timeZone: "UTC" }).format(monthCursor),
    });
    monthCursor.setUTCMonth(monthCursor.getUTCMonth() + 1);
  }

  chartDataSheet.showGridLines = false;
  chartDataSheet.freezePanes.freezeRows(3);
  const modelNames = modelRows.map((row) => row.model);
  const dailyTitle = `Daily cost by model: trailing 30 days (${dailyDates[0]} to ${dailyDates.at(-1)})`;
  const dailyHeaderRow = 3;
  const dailyFirstRow = 4;
  const dailyLastRow = dailyFirstRow + dailyDates.length - 1;
  const dailyLastColumn = columnLetter(modelNames.length + 1);
  chartDataSheet.getRange(`A1:${dailyLastColumn}1`).merge();
  chartDataSheet.getRange("A1").values = [[dailyTitle]];
  chartDataSheet.getRange(`A1:${dailyLastColumn}1`).format = {
    fill: "#253746",
    font: { bold: true, color: "#FFFFFF", size: 14 },
    verticalAlignment: "center",
  };
  chartDataSheet.getRange(`A${dailyHeaderRow}:${dailyLastColumn}${dailyHeaderRow}`).values = [[
    "date",
    ...modelNames,
  ]];
  styleHeader(
    chartDataSheet.getRange(`A${dailyHeaderRow}:${dailyLastColumn}${dailyHeaderRow}`),
    "#204E5A",
  );
  chartDataSheet.getRange(`A${dailyFirstRow}:A${dailyLastRow}`).values = dailyDates.map((date) => [date]);

  for (let modelIndex = 0; modelIndex < modelRows.length; modelIndex += 1) {
    const column = columnLetter(modelIndex + 2);
    const sourceSheet = modelRows[modelIndex].provider === "OpenAI" ? "Codex Costs" : "Claude Costs";
    const sourceLastRow = modelRows[modelIndex].provider === "OpenAI" ? codexRows.length + 1 : claudeRows.length + 1;
    chartDataSheet.getRange(`${column}${dailyFirstRow}:${column}${dailyLastRow}`).formulas = dailyDates.map(
      (_, dateIndex) => {
        const row = dailyFirstRow + dateIndex;
        return [
          `=SUMIFS('${sourceSheet}'!$X$2:$X$${sourceLastRow},'${sourceSheet}'!$A$2:$A$${sourceLastRow},$A${row},'${sourceSheet}'!$E$2:$E$${sourceLastRow},${column}$${dailyHeaderRow})`,
        ];
      },
    );
  }

  const monthlyTitleRow = dailyLastRow + 3;
  const monthlyHeaderRow = monthlyTitleRow + 2;
  const monthlyFirstRow = monthlyHeaderRow + 1;
  const monthlyLastRow = monthlyFirstRow + monthlyPeriods.length - 1;
  const monthlyYearColumn = columnLetter(modelNames.length + 2);
  const monthlyNameColumn = columnLetter(modelNames.length + 3);
  chartDataSheet.getRange(`A${monthlyTitleRow}:${dailyLastColumn}${monthlyTitleRow}`).merge();
  chartDataSheet.getRange(`A${monthlyTitleRow}`).values = [["Monthly cost by model since detailed tracking began"]];
  chartDataSheet.getRange(`A${monthlyTitleRow}:${dailyLastColumn}${monthlyTitleRow}`).format = {
    fill: "#253746",
    font: { bold: true, color: "#FFFFFF", size: 14 },
    verticalAlignment: "center",
  };
  chartDataSheet.getRange(`A${monthlyHeaderRow}:${monthlyNameColumn}${monthlyHeaderRow}`).values = [[
    "month",
    ...modelNames,
    "year_helper",
    "month_name_helper",
  ]];
  styleHeader(
    chartDataSheet.getRange(`A${monthlyHeaderRow}:${monthlyNameColumn}${monthlyHeaderRow}`),
    "#204E5A",
  );
  chartDataSheet.getRange(`A${monthlyFirstRow}:A${monthlyLastRow}`).values = monthlyPeriods.map((period) => [
    period.label,
  ]);
  chartDataSheet.getRange(`${monthlyYearColumn}${monthlyFirstRow}:${monthlyYearColumn}${monthlyLastRow}`).values = monthlyPeriods.map(
    (period) => [period.year],
  );
  chartDataSheet.getRange(`${monthlyNameColumn}${monthlyFirstRow}:${monthlyNameColumn}${monthlyLastRow}`).values = monthlyPeriods.map(
    (period) => [period.month],
  );

  for (let modelIndex = 0; modelIndex < modelRows.length; modelIndex += 1) {
    const column = columnLetter(modelIndex + 2);
    const sourceSheet = modelRows[modelIndex].provider === "OpenAI" ? "Codex Costs" : "Claude Costs";
    const sourceLastRow = modelRows[modelIndex].provider === "OpenAI" ? codexRows.length + 1 : claudeRows.length + 1;
    chartDataSheet.getRange(`${column}${monthlyFirstRow}:${column}${monthlyLastRow}`).formulas = monthlyPeriods.map(
      (_, periodIndex) => {
        const row = monthlyFirstRow + periodIndex;
        return [
          `=SUMIFS('${sourceSheet}'!$X$2:$X$${sourceLastRow},'${sourceSheet}'!$E$2:$E$${sourceLastRow},${column}$${monthlyHeaderRow},'${sourceSheet}'!$C$2:$C$${sourceLastRow},$${monthlyYearColumn}${row},'${sourceSheet}'!$B$2:$B$${sourceLastRow},$${monthlyNameColumn}${row})`,
        ];
      },
    );
  }

  chartDataSheet.getRange(`B${dailyFirstRow}:${dailyLastColumn}${dailyLastRow}`).format.numberFormat = '"$"#,##0.00';
  chartDataSheet.getRange(`B${monthlyFirstRow}:${dailyLastColumn}${monthlyLastRow}`).format.numberFormat = '"$"#,##0.00';
  chartDataSheet.getRange("A:A").format.columnWidth = 14;
  chartDataSheet.getRange(`B:${dailyLastColumn}`).format.columnWidth = 26;
  chartDataSheet.getRange(`${monthlyYearColumn}:${monthlyNameColumn}`).format.columnWidth = 18;

  const chartColors = [
    "#2E6F9E",
    "#4B9B8A",
    "#D18B2C",
    "#A8555A",
    "#7557A8",
    "#6A7A35",
    "#3A8DA8",
    "#BC6B3B",
    "#3E5D78",
    "#C24F83",
    "#7A6A58",
    "#2F7D5C",
  ];
  const addModelChart = ({ title, firstRow, lastRow, headerRow, startCell, endCell }) => {
    const chart = summarySheet.charts.add("line", {
      chartType: "line",
      title,
      hasLegend: true,
    });
    for (let modelIndex = 0; modelIndex < modelRows.length; modelIndex += 1) {
      const column = columnLetter(modelIndex + 2);
      const series = chart.series.add(modelRows[modelIndex].model);
      series.categoryFormula = `'Chart Data'!$A$${firstRow}:$A$${lastRow}`;
      series.formula = `'Chart Data'!$${column}$${firstRow}:$${column}$${lastRow}`;
      series.fill = chartColors[modelIndex % chartColors.length];
    }
    chart.setPosition(startCell, endCell);
    chart.title = title;
    chart.titleTextStyle.fontSize = 13;
    chart.hasLegend = true;
    chart.xAxis = { axisType: "textAxis", textStyle: { fontSize: 9 } };
    chart.yAxis = { numberFormatCode: '"$"#,##0', textStyle: { fontSize: 9 } };
    return chart;
  };

  addModelChart({
    title: "Daily cost by model ($): trailing 30 days",
    firstRow: dailyFirstRow,
    lastRow: dailyLastRow,
    headerRow: dailyHeaderRow,
    startCell: "A23",
    endCell: "I45",
  });
  addModelChart({
    title: "Monthly cost by model ($): since tracking began",
    firstRow: monthlyFirstRow,
    lastRow: monthlyLastRow,
    headerRow: monthlyHeaderRow,
    startCell: "A47",
    endCell: "I69",
  });

  const rateByModel = new Map(pricing.map((rate) => [rate.model, rate]));
  const independent = aggregateByModel(allRows, rateByModel);
  const independentTotals = {
    publishedPriceEquivalent: independent.reduce((sum, row) => sum + row.total, 0),
    openAIPriceEquivalent: independent
      .filter((row) => row.provider === "OpenAI")
      .reduce((sum, row) => sum + row.total, 0),
    anthropicPriceEquivalent: independent
      .filter((row) => row.provider === "Anthropic")
      .reduce((sum, row) => sum + row.total, 0),
    unpricedTokens: independent
      .filter((row) => rateByModel.get(row.model)?.status === "No public API price")
      .reduce((sum, row) => sum + row.totalTokens, 0),
    proxyPricedTokens: independent
      .filter((row) => rateByModel.get(row.model)?.status === "User-selected proxy")
      .reduce((sum, row) => sum + row.totalTokens, 0),
    pricedTokens: independent
      .filter((row) => rateByModel.get(row.model)?.status === "Published")
      .reduce((sum, row) => sum + row.totalTokens, 0),
  };

  const summaryInspection = await workbook.inspect({
    kind: "table",
    range: `Summary!A1:I${lastModelRow}`,
    include: "values,formulas",
    tableMaxRows: 30,
    tableMaxCols: 9,
    maxChars: 30000,
  });
  await fs.writeFile(path.join(QA_DIR, "summary-inspection.ndjson"), summaryInspection.ndjson, "utf8");

  const detailInspection = await workbook.inspect({
    kind: "table",
    range: "Claude Costs!A1:Y8",
    include: "values,formulas",
    tableMaxRows: 8,
    tableMaxCols: 25,
    maxChars: 30000,
  });
  await fs.writeFile(path.join(QA_DIR, "detail-inspection.ndjson"), detailInspection.ndjson, "utf8");

  const chartDataInspection = await workbook.inspect({
    kind: "table",
    range: `Chart Data!A1:${monthlyNameColumn}${monthlyLastRow}`,
    include: "values,formulas",
    tableMaxRows: 50,
    tableMaxCols: 15,
    maxChars: 50000,
  });
  await fs.writeFile(path.join(QA_DIR, "chart-data-inspection.ndjson"), chartDataInspection.ndjson, "utf8");

  const errors = await workbook.inspect({
    kind: "match",
    searchTerm: "#REF!|#DIV/0!|#VALUE!|#NAME\\?|#N/A",
    options: { useRegex: true, maxResults: 300 },
    summary: "final formula error scan",
  });
  await fs.writeFile(path.join(QA_DIR, "formula-errors.ndjson"), errors.ndjson, "utf8");

  const renders = [
    ["Summary", `A1:I${lastModelRow}`, "summary.png", 1.3],
    ["Summary", "A21:I70", "summary-charts.png", 1.0],
    ["Pricing", `A1:M${pricing.length + 1}`, "pricing.png", 0.9],
    ["Codex Costs", "A1:Y18", "codex-costs.png", 0.65],
    ["Claude Costs", "A1:Y18", "claude-costs.png", 0.65],
    ["Chart Data", `A1:${monthlyNameColumn}${monthlyLastRow}`, "chart-data.png", 0.65],
  ];
  for (const [sheetName, range, fileName, scale] of renders) {
    const preview = await workbook.render({ sheetName, range, scale, format: "png" });
    await fs.writeFile(
      path.join(QA_DIR, fileName),
      new Uint8Array(await preview.arrayBuffer()),
    );
  }

  const output = await SpreadsheetFile.exportXlsx(workbook);
  await output.save(OUTPUT_PATH);

  const result = {
    outputPath: OUTPUT_PATH,
    codexRows: codexRows.length,
    claudeRows: claudeRows.length,
    pricingRows: pricing.length,
    models: independent,
    totals: independentTotals,
  };
  await fs.writeFile(path.join(QA_DIR, "result.json"), `${JSON.stringify(result, null, 2)}\n`, "utf8");
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

await main();
