import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { offlineTarget } from "./offline-target.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const core = path.join(root, "services/core");
const compute = path.join(root, "services/compute");
const go = process.env.GO_BINARY ?? "go";

function run(command, args, cwd = root, capture = false, env = process.env) {
  const result = spawnSync(command, args, {
    cwd,
    stdio: capture ? "pipe" : "inherit",
    encoding: "utf8",
    env,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    if (capture) process.stderr.write(result.stderr);
    throw new Error(`${command} ${args.join(" ")} failed (${result.status})`);
  }
  return result.stdout ?? "";
}

function pnpm(...args) {
  const executable = process.env.npm_execpath;
  if (!executable) throw new Error("Run this task through pnpm scripts");
  run(process.execPath, [executable, ...args]);
}

function stageWeb() {
  pnpm("--filter", "@agentos/web", "build");
  const destination = path.join(core, "internal/webassets/site");
  // This fixed generated directory is never a source location.
  rmSync(destination, { recursive: true, force: true });
  cpSync(path.join(root, "apps/web/dist"), destination, { recursive: true });
}

function checkGo() {
  const files = readdirSync(core, { recursive: true })
    .filter((file) => file.endsWith(".go"))
    .map((file) => path.join(core, file));
  const gofmt = process.env.GO_BINARY
    ? path.join(
        path.dirname(go),
        process.platform === "win32" ? "gofmt.exe" : "gofmt",
      )
    : "gofmt";
  const unformatted = run(gofmt, ["-l", ...files], core, true).trim();
  if (unformatted) throw new Error(`Run gofmt on:\n${unformatted}`);
  run(go, ["vet", "./..."], core);
  run(go, ["test", "./..."], core);
}

function checkPython() {
  run("uv", ["sync", "--locked"], compute);
  for (const args of [
    [
      "ruff",
      "check",
      ".",
      "../../scripts/test-offline-linux.py",
      "../../deploy/offline/verify.py",
    ],
    [
      "ruff",
      "format",
      "--check",
      ".",
      "../../scripts/test-offline-linux.py",
      "../../deploy/offline/verify.py",
    ],
    ["mypy"],
    ["pytest"],
  ])
    run("uv", ["run", "--locked", ...args], compute);
}

function buildCore(target) {
  const output = path.join(root, "dist", ...(target ? [target.name] : []));
  mkdirSync(output, { recursive: true });
  for (const [binary, entrypoint] of [
    ["agentos", "agentos"],
    ["agentos-migrate", "migrate"],
  ]) {
    run(
      go,
      [
        "build",
        "-trimpath",
        "-o",
        path.join(
          output,
          target
            ? `${binary}${target.extension}`
            : process.platform === "win32"
              ? `${binary}.exe`
              : binary,
        ),
        `./cmd/${entrypoint}`,
      ],
      core,
      false,
      target
        ? { ...process.env, GOOS: target.os, GOARCH: "amd64", CGO_ENABLED: "0" }
        : process.env,
    );
  }
}

const task = process.argv[2];
switch (task) {
  case "check":
    pnpm("lint");
    pnpm("boundaries");
    pnpm("typecheck");
    pnpm("test");
    run(process.execPath, ["--test", "scripts/offline-sbom.test.mjs"]);
    run(process.execPath, ["--test", "scripts/diagnostics.test.mjs"]);
    if (process.platform === "win32")
      run("pwsh", ["-NoProfile", "-File", "deploy/windows/Service.Tests.ps1"]);
    stageWeb();
    checkGo();
    checkPython();
    buildCore();
    run(process.execPath, ["scripts/smoke.mjs"]);
    run("uv", ["lock", "--check"], compute);
    run("uv", ["build"], compute);
    break;
  case "build":
    stageWeb();
    buildCore();
    run("uv", ["lock", "--check"], compute);
    run("uv", ["build"], compute);
    break;
  case "offline": {
    if (
      ![4, 5].includes(process.argv.length) ||
      !path.isAbsolute(process.argv[3])
    )
      throw new Error(
        "pnpm package:offline <new absolute output directory> [windows-amd64|linux-amd64]",
      );
    const target = offlineTarget(process.argv[4]);
    stageWeb();
    buildCore(target);
    run(process.execPath, [
      "scripts/package-offline.mjs",
      process.argv[3],
      target.name,
    ]);
    break;
  }
  case "go":
    stageWeb();
    checkGo();
    break;
  case "python":
    checkPython();
    break;
  case "database":
    if (!process.env.AGENTOS_TEST_ADMIN_DATABASE_URL) {
      throw new Error(
        "AGENTOS_TEST_ADMIN_DATABASE_URL must point to a disposable test cluster",
      );
    }
    run(go, ["vet", "-tags=integration", "./internal/..."], core);
    run(
      go,
      ["test", "-tags=integration", "-count=1", "-v", "./internal/..."],
      core,
    );
    break;
  case "migrate":
    run(go, ["run", "./cmd/migrate"], core);
    break;
  case "start":
    run(
      path.join(
        root,
        "dist",
        process.platform === "win32" ? "agentos.exe" : "agentos",
      ),
      [],
    );
    break;
  default:
    throw new Error(`Unknown task: ${task}`);
}
