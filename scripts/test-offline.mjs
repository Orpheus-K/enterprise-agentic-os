import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import {
  chmodSync,
  cpSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { createInterface } from "node:readline";
import { createSBOM } from "./offline-sbom.mjs";

const source = process.argv[2];
if (
  process.platform !== "win32" ||
  !source ||
  !path.isAbsolute(source) ||
  process.argv.length !== 3
)
  throw new Error(
    "Usage: node scripts/test-offline.mjs <absolute Windows package directory>",
  );
const temporary = mkdtempSync(
  path.join(path.dirname(source), "offline-install-"),
);
const installed = path.join(temporary, "installed");
cpSync(source, installed, { recursive: true });
const env = {
  SystemRoot: process.env.SystemRoot,
  WINDIR: process.env.WINDIR,
  TEMP: temporary,
  TMP: temporary,
  PATH: "",
  AGENTOS_HTTP_ADDR: "127.0.0.1:0",
};
const binary = path.join(installed, "bin/agentos.exe");
const migrate = path.join(installed, "bin/agentos-migrate.exe");
const powershell = path.join(
  process.env.SystemRoot,
  "System32/WindowsPowerShell/v1.0/powershell.exe",
);
function run(command, args, expected = 0, variables = env, timeout = 20000) {
  const result = spawnSync(command, args, {
    cwd: temporary,
    env: variables,
    encoding: "utf8",
    timeout,
  });
  assert.equal(result.error, undefined);
  assert.equal(result.status, expected, result.stderr);
  return result.stdout;
}
function verify(expected = 0) {
  return run(
    powershell,
    [
      "-NoProfile",
      "-NonInteractive",
      "-ExecutionPolicy",
      "Bypass",
      "-File",
      path.join(installed, "Verify.ps1"),
    ],
    expected,
    env,
    // Cold Windows PowerShell startup plus hashing the entire package exceeded
    // the binary smoke deadline on hosted Windows. Keep verification bounded.
    120000,
  );
}
try {
  verify();
  const manifest = JSON.parse(
    readFileSync(path.join(installed, "MANIFEST.json"), "utf8"),
  );
  const inventory = JSON.parse(
    readFileSync(path.join(installed, "DEPENDENCIES.json"), "utf8"),
  );
  assert.deepEqual(
    JSON.parse(readFileSync(path.join(installed, "SBOM.cdx.json"), "utf8")),
    createSBOM(
      inventory.dependencies,
      manifest.binaries,
      new Map(
        manifest.binaries.map((identity) => [
          identity.component,
          readFileSync(
            path.join(installed, "bin", `${identity.component}.exe`),
          ),
        ]),
      ),
    ),
  );
  for (const identity of manifest.binaries) {
    const executable = path.join(installed, "bin", `${identity.component}.exe`);
    assert.deepEqual(
      JSON.parse(
        run(executable, ["--version"], 0, {
          ...env,
          AGENTOS_CHANNEL_CONFIG: "missing",
          AGENTOS_MIGRATION_DATABASE_URL: "invalid",
        }),
      ),
      identity,
    );
  }
  const notice = manifest.files.find((file) =>
    file.path.startsWith("licenses/"),
  );
  assert.ok(notice);
  const noticePath = path.join(installed, notice.path);
  const original = readFileSync(noticePath);
  // Go module cache attribution files retain read-only mode when copied.
  chmodSync(noticePath, 0o600);
  writeFileSync(noticePath, "tampered");
  verify(1);
  writeFileSync(noticePath, original);
  renameSync(noticePath, `${noticePath}.missing`);
  verify(1);
  renameSync(`${noticePath}.missing`, noticePath);
  const extra = path.join(installed, "unexpected.txt");
  writeFileSync(extra, "unexpected");
  verify(1);
  rmSync(extra);
  verify();
  const database = path.join(temporary, "runtime.db");
  assert.match(
    run(migrate, ["--sqlite", database]),
    /SQLite schema [0-9]+ ready/,
  );
  assert.match(
    run(migrate, ["--sqlite", database]),
    /SQLite schema [0-9]+ ready/,
  );
  run(migrate, ["--sqlite", database], 1, {
    ...env,
    AGENTOS_MIGRATION_DATABASE_URL: "invalid",
  });
  const backup = path.join(temporary, "backup");
  const restoredDatabase = path.join(temporary, "restored.db");
  assert.match(run(migrate, ["--sqlite-backup", database, backup]), /verified/);
  assert.match(
    run(migrate, ["--sqlite-restore", backup, restoredDatabase]),
    /verified/,
  );
  run(migrate, ["--sqlite-restore", backup, restoredDatabase], 1);
  const config = {
    storage_driver: "sqlite",
    sqlite_path: restoredDatabase,
    binding: {
      tenant_id: "10000000-0000-0000-0000-000000000001",
      app_id: "cli_test",
      tenant_key: "test",
    },
    mode: "webhook",
    app_secret_env: "APP",
    verification_token_env: "TOKEN",
    encrypt_key_env: "KEY",
    model: {
      endpoint: "http://127.0.0.1:1/chat/completions",
      name: "synthetic",
      max_tokens: 64,
      allow_http: true,
    },
  };
  const configuration = path.join(temporary, "channel.json");
  writeFileSync(configuration, JSON.stringify(config));
  const siteEnv = {
    ...env,
    AGENTOS_CHANNEL_CONFIG: configuration,
    APP: "synthetic",
    TOKEN: "synthetic",
    KEY: "synthetic",
  };
  // No tenant is provisioned: core and embedded UI run; readiness must fail closed.
  const server = spawn(binary, [], {
    cwd: temporary,
    env: siteEnv,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const lines = createInterface({ input: server.stdout });
  const timeout = setTimeout(() => server.kill(), 15000);
  try {
    const address = await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.once("exit", (code) =>
        reject(new Error(`Core exited before listening: ${code}`)),
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
    const get = (url) => fetch(url, { signal: AbortSignal.timeout(5000) });
    assert.equal((await get(`${base}/api/health`)).status, 200);
    assert.equal((await get(`${base}/api/ready`)).status, 503);
    const home = await get(base);
    assert.equal(home.status, 200);
    const html = await home.text();
    const assets = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+)"/g)];
    assert.ok(assets.length >= 2);
    for (const asset of assets)
      assert.equal((await get(base + asset[1])).status, 200);
    assert.equal((await get(`${base}/api/missing`)).status, 404);
  } finally {
    clearTimeout(timeout);
    lines.close();
    const exited = once(server, "exit");
    if (server.exitCode === null && server.signalCode === null) {
      server.kill();
      await exited;
    }
  }
  writeFileSync(
    configuration,
    JSON.stringify({
      ...config,
      sqlite_path: path.join(temporary, "missing.db"),
    }),
  );
  run(binary, [], 1, siteEnv);
  writeFileSync(configuration, JSON.stringify({ ...config, tools: {} }));
  run(binary, [], 1, siteEnv);
  console.log(
    "Offline install PASS: checksums, tamper/missing/extra rejection, identities, SQLite migration/repeat/conflict, embedded UI, missing-tenant readiness, invalid startup. Binary PATH was empty; no developer tools invoked by target processes.",
  );
} finally {
  // Only delete the exact new directory created above, never an existing package.
  const expectedParent = path.resolve(path.dirname(source));
  if (
    path.dirname(path.resolve(temporary)) !== expectedParent ||
    !path.basename(temporary).startsWith("offline-install-")
  )
    throw new Error("Refusing cleanup outside installation test directory");
  rmSync(temporary, { recursive: true });
}
