import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";

import { Workbook } from "@oai/artifact-tool";

const CODEX_HOME = "/Users/janiorvalle/.codex";
const TIME_ZONE = "America/New_York";
const START_DATE = "2026-02-03";
const END_DATE = "2026-07-18";
const OUTPUT_PATH =
  "/Users/janiorvalle/Documents/Codex/2026-07-18/ar/outputs/codex_daily_token_usage_by_model_2026-02-03_to_2026-07-18.csv";
const QA_DIR = "/Users/janiorvalle/Documents/Codex/2026-07-18/ar/work/token-usage-by-model-qa";

const tokenFields = [
  "input_tokens",
  "cached_input_tokens",
  "cache_write_input_tokens",
  "output_tokens",
  "reasoning_output_tokens",
  "total_tokens",
];

const monthFormatter = new Intl.DateTimeFormat("en-US", {
  month: "long",
  timeZone: "UTC",
});

const localDateFormatter = new Intl.DateTimeFormat("en-US", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  timeZone: TIME_ZONE,
});

function localDateKey(timestamp) {
  const parts = Object.fromEntries(
    localDateFormatter
      .formatToParts(new Date(timestamp))
      .filter((part) => part.type !== "literal")
      .map((part) => [part.type, part.value]),
  );
  return `${parts.year}-${parts.month}-${parts.day}`;
}

function newTotals() {
  return Object.fromEntries(tokenFields.map((field) => [field, 0]));
}

async function aggregateTokenEvents() {
  const totalsByDateModel = new Map();
  const currentModelByFile = new Map();
  const previousTotalsByFile = new Map();
  const pendingUsageByFile = new Map();
  let tokenEvents = 0;
  let countedTokenEvents = 0;
  let malformedMatches = 0;
  let counterResetEvents = 0;
  let duplicateSnapshotEvents = 0;
  let lastUsageMismatches = 0;
  let bufferedBeforeModelEvents = 0;
  let unknownModelEvents = 0;

  function addUsage(date, model, usage) {
    const key = `${date}\t${model}`;
    const totals = totalsByDateModel.get(key) ?? newTotals();
    for (const field of tokenFields) {
      totals[field] += Number(usage[field] ?? 0);
    }
    totalsByDateModel.set(key, totals);
  }

  const rg = spawn(
    "rg",
    [
      "--no-heading",
      "--with-filename",
      "--color=never",
      "--fixed-strings",
      "-e",
      '"type":"turn_context"',
      "-e",
      '"type":"token_count"',
      path.join(CODEX_HOME, "sessions"),
      path.join(CODEX_HOME, "archived_sessions"),
    ],
    { stdio: ["ignore", "pipe", "inherit"] },
  );

  const lines = readline.createInterface({ input: rg.stdout, crlfDelay: Infinity });

  for await (const line of lines) {
    const separator = line.indexOf(":{");
    if (separator === -1) {
      malformedMatches += 1;
      continue;
    }

    const filePath = line.slice(0, separator);
    let event;
    try {
      event = JSON.parse(line.slice(separator + 1));
    } catch {
      malformedMatches += 1;
      continue;
    }

    if (event?.type === "turn_context") {
      const model = event.payload?.model?.trim() || "unknown";
      currentModelByFile.set(filePath, model);
      const pending = pendingUsageByFile.get(filePath) ?? [];
      for (const entry of pending) addUsage(entry.date, model, entry.usage);
      pendingUsageByFile.delete(filePath);
      continue;
    }

    if (
      event?.payload?.type !== "token_count" ||
      !event.payload.info?.total_token_usage ||
      !event.payload.info?.last_token_usage
    ) {
      continue;
    }

    const current = event.payload.info.total_token_usage;
    const previous = previousTotalsByFile.get(filePath);
    const usage = Object.fromEntries(
      tokenFields.map((field) => [field, Number(event.payload.info.last_token_usage[field] ?? 0)]),
    );
    const resetDetected =
      previous &&
      tokenFields.some((field) => Number(current[field] ?? 0) < Number(previous[field] ?? 0));
    const duplicateSnapshot =
      previous &&
      tokenFields.every((field) => Number(current[field] ?? 0) === Number(previous[field] ?? 0));

    previousTotalsByFile.set(filePath, current);
    tokenEvents += 1;

    if (resetDetected) counterResetEvents += 1;
    if (duplicateSnapshot) duplicateSnapshotEvents += 1;
    if (usage.total_tokens !== usage.input_tokens + usage.output_tokens) {
      lastUsageMismatches += 1;
    }
    usage.total_tokens = usage.input_tokens + usage.output_tokens;
    if (usage.total_tokens <= 0) continue;

    const date = localDateKey(event.timestamp);
    if (date < START_DATE || date > END_DATE) continue;

    const model = currentModelByFile.get(filePath);
    if (model) {
      addUsage(date, model, usage);
    } else {
      const pending = pendingUsageByFile.get(filePath) ?? [];
      pending.push({ date, usage });
      pendingUsageByFile.set(filePath, pending);
      bufferedBeforeModelEvents += 1;
    }
    countedTokenEvents += 1;
  }

  for (const pending of pendingUsageByFile.values()) {
    for (const entry of pending) addUsage(entry.date, "unknown", entry.usage);
    unknownModelEvents += pending.length;
  }

  const exitCode = await new Promise((resolve, reject) => {
    rg.once("error", reject);
    rg.once("close", resolve);
  });
  if (exitCode !== 0) {
    throw new Error(`rg exited with code ${exitCode}`);
  }

  return {
    totalsByDateModel,
    tokenEvents,
    countedTokenEvents,
    malformedMatches,
    counterResetEvents,
    duplicateSnapshotEvents,
    lastUsageMismatches,
    bufferedBeforeModelEvents,
    unknownModelEvents,
  };
}

function csvEscape(value) {
  const text = String(value);
  if (!/[",\r\n]/.test(text)) return text;
  return `"${text.replaceAll('"', '""')}"`;
}

async function main() {
  const {
    totalsByDateModel,
    tokenEvents,
    countedTokenEvents,
    malformedMatches,
    counterResetEvents,
    duplicateSnapshotEvents,
    lastUsageMismatches,
    bufferedBeforeModelEvents,
    unknownModelEvents,
  } = await aggregateTokenEvents();

  const headers = [
    "date",
    "month",
    "year",
    "model",
    "input_tokens",
    "cached_input_tokens",
    "cache_write_input_tokens",
    "uncached_input_tokens",
    "output_tokens",
    "reasoning_output_tokens",
    "total_tokens",
  ];

  const rows = [...totalsByDateModel.entries()]
    .map(([key, totals]) => {
      const [date, model] = key.split("\t");
      const [year, month] = date.split("-");
      const monthName = monthFormatter.format(new Date(`${year}-${month}-01T00:00:00Z`));
      return [
        date,
        monthName,
        Number(year),
        model,
        totals.input_tokens,
        totals.cached_input_tokens,
        totals.cache_write_input_tokens,
        totals.input_tokens - totals.cached_input_tokens,
        totals.output_tokens,
        totals.reasoning_output_tokens,
        totals.total_tokens,
      ];
    })
    .sort((a, b) => a[0].localeCompare(b[0]) || a[3].localeCompare(b[3]));

  const csv = [headers, ...rows]
    .map((row) => row.map(csvEscape).join(","))
    .join("\n")
    .concat("\n");

  await fs.mkdir(path.dirname(OUTPUT_PATH), { recursive: true });
  await fs.mkdir(QA_DIR, { recursive: true });
  await fs.writeFile(OUTPUT_PATH, csv, "utf8");

  const workbook = await Workbook.fromCSV(csv, { sheetName: "Daily Token Usage" });
  const sheet = workbook.worksheets.getItem("Daily Token Usage");
  sheet.freezePanes.freezeRows(1);
  sheet.showGridLines = false;
  sheet.getRange("A1:K1").format = {
    fill: "#1F4E78",
    font: { bold: true, color: "#FFFFFF" },
    horizontalAlignment: "center",
  };
  sheet.getRange(`A2:A${rows.length + 1}`).format.numberFormat = "yyyy-mm-dd";
  sheet.getRange(`C2:C${rows.length + 1}`).format.numberFormat = "0";
  sheet.getRange(`E2:K${rows.length + 1}`).format.numberFormat = "#,##0";
  sheet.getRange(`A1:K${rows.length + 1}`).format.autofitColumns();
  sheet.getRange("A:A").format.columnWidth = 13;
  sheet.getRange("B:B").format.columnWidth = 12;
  sheet.getRange("C:C").format.columnWidth = 10;
  sheet.getRange("D:D").format.columnWidth = 24;
  sheet.getRange("E:K").format.columnWidth = 22;

  const inspection = await workbook.inspect({
    kind: "table",
    range: `Daily Token Usage!A1:K${rows.length + 1}`,
    include: "values,formulas",
    tableMaxRows: 8,
    tableMaxCols: 11,
    maxChars: 6000,
  });
  await fs.writeFile(path.join(QA_DIR, "inspection.ndjson"), inspection.ndjson, "utf8");

  const preview = await workbook.render({
    sheetName: "Daily Token Usage",
    range: `A1:K${rows.length + 1}`,
    scale: 0.8,
    format: "png",
  });
  await fs.writeFile(
    path.join(QA_DIR, "daily-token-usage.png"),
    new Uint8Array(await preview.arrayBuffer()),
  );

  const columnTotals = Object.fromEntries(
    headers.slice(4).map((header, index) => [
      header,
      rows.reduce((sum, row) => sum + Number(row[index + 4]), 0),
    ]),
  );

  const summary = {
    outputPath: OUTPUT_PATH,
    startDate: START_DATE,
    endDate: END_DATE,
    dataRows: rows.length,
    dateModelRows: rows.length,
    uniqueDates: new Set(rows.map((row) => row[0])).size,
    models: [...new Set(rows.map((row) => row[3]))].sort(),
    tokenEvents,
    countedTokenEvents,
    malformedMatches,
    counterResetEvents,
    duplicateSnapshotEvents,
    lastUsageMismatches,
    bufferedBeforeModelEvents,
    unknownModelEvents,
    columnTotals,
  };
  await fs.writeFile(
    path.join(QA_DIR, "summary.json"),
    `${JSON.stringify(summary, null, 2)}\n`,
    "utf8",
  );
  process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
}

await main();
