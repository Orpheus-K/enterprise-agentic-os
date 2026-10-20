import {
  closeSync,
  fstatSync,
  openSync,
  readSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const operations = new Set([
  "ingress.webhook",
  "ingress.websocket",
  "ingress.approval",
  "task.run",
  "model.complete",
  "model.route",
  "channel.reply",
  "identity.authorize",
]);

function object(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function parse(text) {
  try {
    return JSON.parse(text);
  } catch {
    throw new Error("diagnostics_invalid_json");
  }
}

// Project into fixed fields: arbitrary values, identifiers and error strings never leave input.
export function summarize(configText, logText) {
  if (
    Buffer.byteLength(configText) > 1024 * 1024 ||
    Buffer.byteLength(logText) > 16 * 1024 * 1024
  )
    throw new Error("diagnostics_input_limit");
  const config = parse(configText);
  if (!object(config)) throw new Error("diagnostics_invalid_config");
  const events = {};
  let total = 0;
  let omitted = 0;
  for (const line of logText.split(/\r?\n/)) {
    if (!line.trim()) continue;
    total++;
    if (Buffer.byteLength(line) > 64 * 1024 || total > 100000)
      throw new Error("diagnostics_log_limit");
    const event = parse(line);
    if (!object(event)) throw new Error("diagnostics_invalid_event");
    if (
      !operations.has(event.operation) ||
      !["Unset", "Ok", "Error"].includes(event.status)
    ) {
      omitted++;
      continue;
    }
    const key = `${event.operation}:${event.status}`;
    events[key] = (events[key] ?? 0) + 1;
  }
  return {
    format: "agentos.diagnostics.v1",
    configuration: {
      mode: ["websocket", "webhook"].includes(config.mode)
        ? config.mode
        : "unknown",
      modelConfigured: object(config.model),
      operationsConfigured: object(config.operations),
      approvalConfigured: object(config.approval),
    },
    logs: { total, omitted, events },
    scope:
      "local supplied files; no readiness, authorization or integrity attestation",
  };
}

function readBounded(file, limit) {
  if (!path.isAbsolute(file))
    throw new Error("diagnostics_absolute_path_required");
  let fd;
  try {
    fd = openSync(file, "r");
    if (!fstatSync(fd).isFile())
      throw new Error("diagnostics_regular_file_required");
    const buffer = Buffer.alloc(limit + 1);
    let length = 0;
    while (length < buffer.length) {
      const count = readSync(fd, buffer, length, buffer.length - length, null);
      if (!count) break;
      length += count;
    }
    if (length > limit) throw new Error("diagnostics_input_limit");
    return new TextDecoder("utf-8", { fatal: true }).decode(
      buffer.subarray(0, length),
    );
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

export function collect(configPath, logPath, outputPath) {
  if (!path.isAbsolute(outputPath))
    throw new Error("diagnostics_absolute_path_required");
  const result = summarize(
    readBounded(configPath, 1024 * 1024),
    readBounded(logPath, 16 * 1024 * 1024),
  );
  writeFileSync(outputPath, `${JSON.stringify(result, null, 2)}\n`, {
    flag: "wx",
    mode: 0o600,
  });
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  try {
    if (process.argv.length !== 5) throw new Error("diagnostics_arguments");
    collect(...process.argv.slice(2));
    process.stdout.write("diagnostics_written\n");
  } catch {
    // Native filesystem and JSON errors can contain local paths or input fragments.
    process.stderr.write(
      "diagnostics_failed: check arguments, UTF-8 JSON inputs, limits and new output path\n",
    );
    process.exitCode = 1;
  }
}
