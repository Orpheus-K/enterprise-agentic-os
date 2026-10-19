import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { createInterface } from "node:readline";
import Ajv from "ajv";

const binary = path.resolve(
  "dist",
  process.platform === "win32" ? "agentos.exe" : "agentos",
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
