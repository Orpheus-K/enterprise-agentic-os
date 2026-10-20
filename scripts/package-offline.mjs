import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  cpSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const go = process.env.GO_BINARY ?? "go";
function run(command, args, cwd = root, env = process.env) {
  const result = spawnSync(command, args, {
    cwd,
    env,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
  if (result.error || result.status !== 0)
    throw new Error(
      `Failed: ${command}: ${result.error?.message ?? result.stderr}`,
    );
  return result.stdout;
}
function hash(file) {
  return createHash("sha256").update(readFileSync(file)).digest("hex");
}
function files(directory, prefix = "") {
  return readdirSync(directory, { withFileTypes: true })
    .flatMap((entry) => {
      const name = path.posix.join(prefix, entry.name);
      if (entry.isSymbolicLink())
        throw new Error(`Symlink in package: ${name}`);
      return entry.isDirectory()
        ? files(path.join(directory, entry.name), name)
        : [name];
    })
    .sort();
}
if (process.platform !== "win32" || process.arch !== "x64")
  throw new Error(
    "This first offline profile is certified only on Windows/amd64",
  );
const destination = process.argv[2];
if (!destination || process.argv.length !== 3 || !path.isAbsolute(destination))
  throw new Error("Usage: package-offline.mjs <new absolute output directory>");
// Refuse overwrite: no recursive deletion of existing packages or customer data.
mkdirSync(destination);
const write = (name, value) => {
  const file = path.join(destination, name);
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, value);
};
const copy = (source, name) => {
  mkdirSync(path.dirname(path.join(destination, name)), { recursive: true });
  cpSync(source, path.join(destination, name));
};
const identities = [];
const modules = new Map();
const revision = run("git", ["rev-parse", "HEAD"]).trim();
for (const component of ["agentos", "agentos-migrate"]) {
  const source = path.join(root, "dist", `${component}.exe`);
  const identity = JSON.parse(run(source, ["--version"]));
  if (
    identity.component !== component ||
    identity.os !== "windows" ||
    identity.architecture !== "amd64" ||
    identity.source?.revision !== revision
  )
    throw new Error(`Stale or incompatible binary: ${component}`);
  identities.push(identity);
  const info = JSON.parse(run(go, ["version", "-m", "-json", source]));
  for (const mod of info.Deps) {
    if (mod.Replace || !mod.Version || !mod.Sum)
      throw new Error(`Unpinned module ${mod.Path}`);
    const previous = modules.get(mod.Path);
    if (
      previous &&
      (previous.Version !== mod.Version || previous.Sum !== mod.Sum)
    )
      throw new Error(`Conflicting module ${mod.Path}`);
    modules.set(mod.Path, mod);
  }
  copy(source, `bin/${component}.exe`);
}
if (
  JSON.stringify({ ...identities[0], component: "" }) !==
  JSON.stringify({ ...identities[1], component: "" })
)
  throw new Error("Binary identities do not match");

// Keep original attribution text, including nested notices and license directories.
const legalName =
  /^(licen[cs]es?|copying|notice|copyright|patents|authors)([._-].*)?$/i;
function legalFiles(directory, prefix = "", inLegalDirectory = false) {
  return readdirSync(directory, { withFileTypes: true })
    .flatMap((entry) => {
      if (
        entry.isSymbolicLink() ||
        entry.name === ".git" ||
        entry.name === "node_modules"
      )
        return [];
      const name = path.posix.join(prefix, entry.name);
      const legal = inLegalDirectory || legalName.test(entry.name);
      return entry.isDirectory()
        ? legalFiles(path.join(directory, entry.name), name, legal)
        : legal
          ? [name]
          : [];
    })
    .sort();
}
const dependencies = [];
function collect(ecosystem, name, version, directory, extra = {}) {
  const originals = legalFiles(directory);
  if (!originals.some((name) => /licen[cs]e|copying|copyright/i.test(name)))
    throw new Error(`Missing license text: ${name}@${version}`);
  const id = createHash("sha256")
    .update(`${ecosystem}:${name}@${version}`)
    .digest("hex")
    .slice(0, 20);
  const notices = originals.map((relative) => {
    const target = `licenses/${id}/${relative}`;
    copy(path.join(directory, relative), target);
    return {
      source: relative,
      file: target,
      sha256: hash(path.join(destination, target)),
    };
  });
  dependencies.push({ ecosystem, name, version, ...extra, notices });
}
const offlineEnv = {
  ...process.env,
  GOPROXY: "off",
  GOSUMDB: "off",
  GOTOOLCHAIN: "local",
};
for (const mod of [...modules.values()].sort((a, b) =>
  a.Path.localeCompare(b.Path),
)) {
  const cached = JSON.parse(
    run(
      go,
      ["mod", "download", "-json", `${mod.Path}@${mod.Version}`],
      path.join(root, "services/core"),
      offlineEnv,
    ),
  );
  if (cached.Error || cached.Sum !== mod.Sum || !cached.Dir)
    throw new Error(`Module cache mismatch: ${mod.Path}`);
  collect("go", mod.Path, mod.Version, cached.Dir, {
    checksum: mod.Sum,
    scope: "linked-binaries",
  });
}
const goroot = run(go, ["env", "GOROOT"]).trim();
// Runtime and vendored standard-library attribution; omit compiler/test source.
for (const item of ["LICENSE", "PATENTS"])
  copy(path.join(goroot, item), `licenses/go/${item}`);
for (const relative of legalFiles(path.join(goroot, "src")))
  copy(path.join(goroot, "src", relative), `licenses/go/src/${relative}`);
dependencies.push({
  ecosystem: "go-toolchain",
  name: "Go",
  version: identities[0].go_version,
  scope: "runtime",
  notices: files(path.join(destination, "licenses/go")).map((file) => ({
    file: `licenses/go/${file}`,
    sha256: hash(path.join(destination, "licenses/go", file)),
  })),
});
if (!process.env.npm_execpath) throw new Error("Run packaging through pnpm");
const npmLicenses = JSON.parse(
  run(process.execPath, [
    process.env.npm_execpath,
    "licenses",
    "list",
    "--prod",
    "--json",
  ]),
);
const seen = new Set();
for (const group of Object.values(npmLicenses)) {
  for (const entry of group) {
    if (!entry.paths?.length)
      throw new Error(`No installed source for ${entry.name}`);
    for (const directory of entry.paths) {
      const pkg = JSON.parse(
        readFileSync(path.join(directory, "package.json"), "utf8"),
      );
      const id = `${pkg.name}@${pkg.version}`;
      if (seen.has(id)) continue;
      seen.add(id);
      if (!pkg.license || typeof pkg.license !== "string")
        throw new Error(`Unknown declared license: ${id}`);
      collect("npm", pkg.name, pkg.version, directory, {
        declared_license: pkg.license,
        scope: "workspace-production-superset",
      });
    }
  }
}
write(
  "DEPENDENCIES.json",
  `${JSON.stringify({ format: 1, description: "Original license texts, not a legal clearance or SPDX SBOM. npm is a conservative installed production dependency superset.", dependencies }, null, 2)}\n`,
);
for (const [source, target] of [
  ["deploy/offline/INSTALL.md", "INSTALL.md"],
  ["deploy/offline/Verify.ps1", "Verify.ps1"],
  ["deploy/sqlite/README.md", "docs/deploy/sqlite/README.md"],
  ["deploy/feishu.example.json", "docs/deploy/feishu.example.json"],
  ["contracts/identity.md", "docs/contracts/identity.md"],
  ["contracts/task-runtime.md", "docs/contracts/task-runtime.md"],
  ["contracts/operations.md", "docs/contracts/operations.md"],
])
  copy(path.join(root, source), target);
const contents = files(destination).map((name) => ({
  path: name,
  sha256: hash(path.join(destination, name)),
}));
write(
  "MANIFEST.json",
  `${JSON.stringify(
    {
      format: 1,
      profile: "windows-amd64-sqlite-core",
      release_status: "internal-validation-only",
      source: identities[0].source,
      binaries: identities,
      excluded: [
        "Python runtime and wheels",
        "PostgreSQL server and extensions",
        "models",
        "site credentials",
        "customer databases",
      ],
      files: contents,
    },
    null,
    2,
  )}\n`,
);
console.log(
  `Offline package: ${destination}; files=${contents.length}; dependencies=${dependencies.length}; modified=${identities[0].source.modified}`,
);
