import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { createInterface } from "node:readline";
import Ajv from "ajv";

const binary = path.resolve(
  "dist",
  process.platform === "win32" ? "agentos.exe" : "agentos",
);
// Identity inspection must stay offline even with unusable site configuration.
const identities = [];
for (const name of ["agentos", "agentos-migrate"]) {
  const executable = path.resolve(
    "dist",
    process.platform === "win32" ? `${name}.exe` : name,
  );
  const env = {
    ...process.env,
    AGENTOS_CHANNEL_CONFIG: "missing-site-config.json",
    AGENTOS_MIGRATION_DATABASE_URL: "invalid",
    AGENTOS_HTTP_ADDR: "invalid",
  };
  const version = spawnSync(executable, ["--version"], {
    env,
    encoding: "utf8",
    timeout: 5000,
  });
  assert.equal(version.error, undefined);
  assert.equal(version.status, 0, version.stderr);
  const metadata = JSON.parse(version.stdout);
  assert.equal(metadata.component, name);
  assert.ok(
    Number.isInteger(metadata.schema_version) && metadata.schema_version > 0,
  );
  assert.match(metadata.quotation_wasm_sha256, /^[0-9a-f]{64}$/);
  assert.match(metadata.go_version, /^go[0-9]/);
  assert.ok(metadata.os && metadata.architecture);
  if (metadata.source !== null) {
    assert.match(metadata.source.revision, /^[0-9a-f]{40,64}$/);
    assert.equal(typeof metadata.source.modified, "boolean");
  }
  const invalid = spawnSync(executable, ["--version", "unexpected"], {
    env,
    encoding: "utf8",
    timeout: 5000,
  });
  assert.equal(invalid.status, 1);
  identities.push(metadata);
}
assert.deepEqual(
  { ...identities[0], component: "same" },
  { ...identities[1], component: "same" },
);

const server = spawn(binary, [], {
  env: { ...process.env, AGENTOS_HTTP_ADDR: "127.0.0.1:0" },
  stdio: ["ignore", "pipe", "inherit"],
});
const lines = createInterface({ input: server.stdout });
const timeout = setTimeout(() => server.kill(), 15000);

try {
  const address = await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.once("exit", (code) =>
      reject(new Error(`Core exited before readiness: ${code}`)),
    );
    lines.on("line", (line) => {
      try {
        const log = JSON.parse(line);
        if (log.msg === "core listening" && typeof log.address === "string")
          resolve(log.address);
      } catch (error) {
        reject(error);
      }
    });
  });
  const base = `http://${address}`;
  const health = await fetch(`${base}/api/health`, {
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(health.status, 200);
  const schema = JSON.parse(await readFile("contracts/health.json", "utf8"));
  assert.equal(new Ajv().compile(schema)(await health.json()), true);
  const ready = await fetch(`${base}/api/ready`);
  assert.equal(ready.status, 503);
  assert.deepEqual(await ready.json(), { ready: false });
  assert.equal(ready.headers.get("cache-control"), "no-store");
  assert.equal((await fetch(`${base}/api/ops/audit`)).status, 404);
  const home = await fetch(base);
  const html = await home.text();
  assert.equal(home.status, 200);
  assert.match(html, /AgentOS/);
  const assets = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+)"/g)];
  assert.ok(assets.length >= 2, "Built JS and CSS must be embedded");
  for (const [, asset] of assets)
    assert.equal((await fetch(`${base}${asset}`)).status, 200);
  assert.equal(
    (
      await fetch(`${base}/skills/example`, {
        headers: { Accept: "text/html" },
      })
    ).status,
    200,
  );
  assert.equal(
    (await fetch(`${base}/api/missing`, { headers: { Accept: "text/html" } }))
      .status,
    404,
  );
  assert.equal(
    (
      await fetch(`${base}/assets/missing.js`, {
        headers: { Accept: "text/html" },
      })
    ).status,
    404,
  );
  console.log(
    "Built binary: contract, embedded assets, SPA route and 404 boundaries passed.",
  );
} finally {
  clearTimeout(timeout);
  lines.close();
  const exited = once(server, "exit");
  if (server.exitCode === null && server.signalCode === null) {
    server.kill();
    await exited;
  }
}
