import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";
import { createSBOM } from "./offline-sbom.mjs";

const dependencies = [
  {
    ecosystem: "npm",
    name: "@test/ui",
    version: "1.0.0",
    scope: "workspace-production-superset",
    declared_license: "MIT",
    notices: [{ file: "licenses/test/LICENSE", sha256: "a".repeat(64) }],
  },
  {
    ecosystem: "go",
    name: "example.org/mod",
    version: "v1.0.0",
    scope: "linked-binaries",
    checksum: "h1:synthetic",
    notices: [{ file: "licenses/go/LICENSE", sha256: "b".repeat(64) }],
  },
];
const identities = [
  {
    component: "agentos",
    os: "linux",
    architecture: "amd64",
    source: { revision: "c".repeat(40), modified: true },
  },
];
const binaries = new Map([["agentos", Buffer.from("synthetic binary")]]);

test("SBOM preserves actual inventory, binary hashes, module sums and uncertainty", () => {
  const bom = createSBOM(dependencies, identities, binaries);
  assert.equal(bom.components.length, 3);
  assert.equal(bom.components[0].purl, "pkg:npm/%40test/ui@1.0.0");
  assert.ok(
    bom.components[1].properties.some(
      (property) =>
        property.name === "agentos:go-module-sum" &&
        property.value === "h1:synthetic",
    ),
  );
  assert.equal(bom.components[1].hashes, undefined); // Go h1 is not an archive SHA-256.
  assert.equal(
    bom.components[2].hashes[0].content,
    createHash("sha256").update(binaries.get("agentos")).digest("hex"),
  );
  assert.equal(bom.dependencies, undefined); // Do not invent direct/transitive edges.
  assert.match(
    bom.metadata.properties.find(
      (property) => property.name === "agentos:inventory-scope",
    ).value,
    /superset/,
  );
});

test("SBOM rejects duplicate, missing attribution, malformed and incomplete evidence", () => {
  assert.throws(
    () => createSBOM([...dependencies, dependencies[0]], identities, binaries),
    /Duplicate/,
  );
  assert.throws(
    () =>
      createSBOM([{ ...dependencies[0], notices: [] }], identities, binaries),
    /attribution/,
  );
  assert.throws(
    () =>
      createSBOM([{ ...dependencies[0], scope: 123 }], identities, binaries),
    /Invalid CycloneDX/,
  );
  assert.throws(
    () => createSBOM(dependencies, identities, new Map()),
    /Missing binary/,
  );
});
