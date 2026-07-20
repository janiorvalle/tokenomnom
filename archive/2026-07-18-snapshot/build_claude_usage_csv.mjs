import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import readline from "node:readline";

import { Workbook } from "@oai/artifact-tool";

const CLAUDE_PROJECTS = "/Users/janiorvalle/.claude/projects";
const TIME_ZONE = "America/New_York";
const END_DATE = "2026-07-18";
const OUTPUT_DIR = "/Users/janiorvalle/Documents/Codex/2026-07-18/ar/outputs";
const QA_DIR = "/Users/janiorvalle/Documents/Codex/2026-07-18/ar/work/claude-token-usage-by-model-qa";

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

function normalizedIterations(message) {
  const usage = message.usage ?? {};
  const iterations = Array.isArray(usage.iterations) && usage.iterations.length > 0
    ? usage.iterations
    : [usage];

  return iterations.map((iteration) => ({
    model: iteration.model || message.model || "unknown",
    rawInputTokens: Number(iteration.input_tokens ?? 0),
    cacheReadInputTokens: Number(iteration.cache_read_input_tokens ?? 0),
    cacheCreationInputTokens: Number(iteration.cache_creation_input_tokens ?? 0),
    cacheWrite5mInputTokens: Number(
      iteration.cache_creation?.ephemeral_5m_input_tokens ?? 0,
    ),
    cacheWrite1hInputTokens: Number(
      iteration.cache_creation?.ephemeral_1h_input_tokens ?? 0,
    ),
    outputTokens: Number(iteration.output_tokens ?? 0),
  }));
}

function usageScore(message) {
  return normalizedIterations(message).reduce(
    (sum, item) =>
      sum +
      item.rawInputTokens +
      item.cacheReadInputTokens +
      item.cacheCreationInputTokens +
      item.outputTokens,
    0,
  );
}

function csvEscape(value) {
  const text = String(value);
  if (!/[",\r\n]/.test(text)) return text;
  return `"${text.replaceAll('"', '""')}"`;
}

async function readUniqueMessages() {
  const messagesById = new Map();
  let matchedLines = 0;
  let malformedMatches = 0;
  let duplicateRecords = 0;
  let differingDuplicateRecords = 0;
  let missingMessageIds = 0;

  const rg = spawn(
    "rg",
    [
      "--no-heading",
      "--with-filename",
      "--color=never",
      "--fixed-strings",
      "-g",
      "*.jsonl",
      '"usage":{',
      CLAUDE_PROJECTS,
    ],
    { stdio: ["ignore", "pipe", "inherit"] },
  );

  const lines = readline.createInterface({ input: rg.stdout, crlfDelay: Infinity });
  for await (const line of lines) {
    matchedLines += 1;
    const separator = line.indexOf(":{");
    if (separator === -1) {
      malformedMatches += 1;
      continue;
    }

    let event;
    try {
      event = JSON.parse(line.slice(separator + 1));
    } catch {
      malformedMatches += 1;
      continue;
    }

    if (event?.type !== "assistant" || !event.message?.usage || !event.timestamp) continue;

    const messageId = event.message.id;
    if (!messageId) {
      missingMessageIds += 1;
      continue;
    }

    const candidate = {
      timestamp: event.timestamp,
      message: event.message,
      score: usageScore(event.message),
    };
    const existing = messagesById.get(messageId);
    if (!existing) {
      messagesById.set(messageId, candidate);
      continue;
    }

    duplicateRecords += 1;
    if (candidate.score !== existing.score) differingDuplicateRecords += 1;
    if (candidate.score > existing.score) {
      candidate.timestamp = existing.timestamp < candidate.timestamp
        ? existing.timestamp
        : candidate.timestamp;
      messagesById.set(messageId, candidate);
    }
  }

  const exitCode = await new Promise((resolve, reject) => {
    rg.once("error", reject);
    rg.once("close", resolve);
  });
  if (exitCode !== 0) throw new Error(`rg exited with code ${exitCode}`);

  return {
    messagesById,
    matchedLines,
    malformedMatches,
    duplicateRecords,
    differingDuplicateRecords,
    missingMessageIds,
  };
}

async function main() {
  const source = await readUniqueMessages();
  const totalsByDateModel = new Map();
  let earliestDate = END_DATE;
  let iterationCount = 0;
  let fallbackMessageCount = 0;

  for (const record of source.messagesById.values()) {
    const date = localDateKey(record.timestamp);
    if (date > END_DATE) continue;
    if (date < earliestDate) earliestDate = date;

    const iterations = normalizedIterations(record.message);
    if (iterations.length > 1) fallbackMessageCount += 1;

    for (const item of iterations) {
      const totalInputTokens =
        item.rawInputTokens + item.cacheReadInputTokens + item.cacheCreationInputTokens;
      const totalTokens = totalInputTokens + item.outputTokens;
      if (totalTokens <= 0) continue;

      const key = `${date}\t${item.model}`;
      const totals = totalsByDateModel.get(key) ?? {
        inputTokens: 0,
        cachedInputTokens: 0,
        cacheWriteInputTokens: 0,
        cacheWrite5mInputTokens: 0,
        cacheWrite1hInputTokens: 0,
        cacheWriteUnclassifiedInputTokens: 0,
        uncachedInputTokens: 0,
        outputTokens: 0,
        reasoningOutputTokens: 0,
        totalTokens: 0,
      };

      totals.inputTokens += totalInputTokens;
      totals.cachedInputTokens += item.cacheReadInputTokens;
      totals.cacheWriteInputTokens += item.cacheCreationInputTokens;
      totals.cacheWrite5mInputTokens += item.cacheWrite5mInputTokens;
      totals.cacheWrite1hInputTokens += item.cacheWrite1hInputTokens;
      totals.cacheWriteUnclassifiedInputTokens += Math.max(
        0,
        item.cacheCreationInputTokens -
          item.cacheWrite5mInputTokens -
          item.cacheWrite1hInputTokens,
      );
      totals.uncachedInputTokens += item.rawInputTokens + item.cacheCreationInputTokens;
      totals.outputTokens += item.outputTokens;
      totals.totalTokens += totalTokens;
      totalsByDateModel.set(key, totals);
      iterationCount += 1;
    }
  }

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
        totals.inputTokens,
        totals.cachedInputTokens,
        totals.cacheWriteInputTokens,
        totals.uncachedInputTokens,
        totals.outputTokens,
        totals.reasoningOutputTokens,
        totals.totalTokens,
      ];
    })
    .sort((a, b) => a[0].localeCompare(b[0]) || a[3].localeCompare(b[3]));

  const csv = [headers, ...rows]
    .map((row) => row.map(csvEscape).join(","))
    .join("\n")
    .concat("\n");

  const outputPath = path.join(
    OUTPUT_DIR,
    `claude_daily_token_usage_by_model_${earliestDate}_to_${END_DATE}.csv`,
  );
  await fs.mkdir(OUTPUT_DIR, { recursive: true });
  await fs.mkdir(QA_DIR, { recursive: true });
  await fs.writeFile(outputPath, csv, "utf8");

  const pricingInput = [...totalsByDateModel.entries()]
    .map(([key, totals]) => {
      const [date, model] = key.split("\t");
      return {
        date,
        model,
        inputTokens: totals.inputTokens,
        cachedInputTokens: totals.cachedInputTokens,
        cacheWriteInputTokens: totals.cacheWriteInputTokens,
        cacheWrite5mInputTokens: totals.cacheWrite5mInputTokens,
        cacheWrite1hInputTokens: totals.cacheWrite1hInputTokens,
        cacheWriteUnclassifiedInputTokens: totals.cacheWriteUnclassifiedInputTokens,
        baseInputTokens: totals.uncachedInputTokens - totals.cacheWriteInputTokens,
        outputTokens: totals.outputTokens,
        reasoningOutputTokens: totals.reasoningOutputTokens,
        totalTokens: totals.totalTokens,
      };
    })
    .sort((a, b) => a.date.localeCompare(b.date) || a.model.localeCompare(b.model));
  await fs.writeFile(
    path.join(QA_DIR, "pricing-input.json"),
    `${JSON.stringify(pricingInput, null, 2)}\n`,
    "utf8",
  );

  const workbook = await Workbook.fromCSV(csv, { sheetName: "Daily Token Usage" });
  const sheet = workbook.worksheets.getItem("Daily Token Usage");
  sheet.freezePanes.freezeRows(1);
  sheet.showGridLines = false;
  sheet.getRange("A1:K1").format = {
    fill: "#6B3FA0",
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
  sheet.getRange("D:D").format.columnWidth = 32;
  sheet.getRange("E:K").format.columnWidth = 22;

  const inspection = await workbook.inspect({
    kind: "table",
    range: `Daily Token Usage!A1:K${rows.length + 1}`,
    include: "values,formulas",
    tableMaxRows: 10,
    tableMaxCols: 11,
    maxChars: 7000,
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
    outputPath,
    startDate: earliestDate,
    endDate: END_DATE,
    dataRows: rows.length,
    uniqueDates: new Set(rows.map((row) => row[0])).size,
    models: [...new Set(rows.map((row) => row[3]))].sort(),
    uniqueMessages: source.messagesById.size,
    iterationCount,
    fallbackMessageCount,
    matchedLines: source.matchedLines,
    duplicateRecords: source.duplicateRecords,
    differingDuplicateRecords: source.differingDuplicateRecords,
    malformedMatches: source.malformedMatches,
    missingMessageIds: source.missingMessageIds,
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
