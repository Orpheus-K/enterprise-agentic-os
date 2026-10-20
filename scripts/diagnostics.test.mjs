import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";
import { collect, summarize } from "./diagnostics.mjs";

test("diagnostic projection excludes nested secrets, identifiers and unknown labels", () => {
  const secret = "SYNTHETIC_PRIVATE_CANARY";
  const result = summarize(
    JSON.stringify({
      mode: "websocket",
      model: { api_key: secret },
      binding: { tenant_id: secret },
      password: secret,
    }),
    [
      {
        operation: "task.run",
        status: "Error",
        error: secret,
        task_id: secret,
        trace_id: secret,
        nested: { authorization: secret },
      },
      { operation: "task.run", status: "Error" },
      { operation: secret, status: "Ok" },
      { operation: "task.run", status: secret },
    ]
      .map(JSON.stringify)
      .join("\n"),
  );
  assert.equal(JSON.stringify(result).includes(secret), false);
  assert.deepEqual(result.logs, {
    total: 4,
    omitted: 2,
    events: { "task.run:Error": 2 },
  });
  assert.equal(result.configuration.modelConfigured, true);
});

test("malformed and oversized evidence fails without partial result", () => {
  for (const [config, log] of [
    ["[]", ""],
    ["{}", "[]"],
    ["{}", "{broken-secret"],
    ["{}", JSON.stringify({ message: "x".repeat(65536) })],
    [" ".repeat(1048577), ""],
  ])
    assert.throws(() => summarize(config, log), /diagnostics_/);
});

test("collector writes only projection, refuses overwrite and keeps CLI errors private", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "agentos-diagnostics-"));
  try {
    const config = path.join(dir, "config.json"),
      log = path.join(dir, "log.jsonl"),
      out = path.join(dir, "report.json");
    writeFileSync(config, '{"mode":"websocket","secret":"CANARY"}');
    writeFileSync(log, '{"error":"CANARY"}\n');
    collect(config, log, out);
    assert.equal(readFileSync(out, "utf8").includes("CANARY"), false);
    assert.throws(() => collect(config, log, out), { code: "EEXIST" });
    writeFileSync(config, '{"secret":"CANARY"');
    const failed = spawnSync(
      process.execPath,
      ["scripts/diagnostics.mjs", config, log, path.join(dir, "new.json")],
      { encoding: "utf8" },
    );
    assert.equal(failed.status, 1);
    assert.equal((failed.stdout + failed.stderr).includes("CANARY"), false);
    assert.equal((failed.stdout + failed.stderr).includes(dir), false);
    assert.equal(existsSync(path.join(dir, "new.json")), false);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
