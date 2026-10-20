import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import Ajv from "ajv";

// Pinned upstream schemas; generation and validation never access the network.
const ajv = new Ajv({ strict: false, validateFormats: false });
for (const name of ["spdx.schema.json", "jsf-0.82.schema.json"])
  ajv.addSchema(
    JSON.parse(
      readFileSync(new URL(`vendor/cyclonedx/${name}`, import.meta.url)),
    ),
  );
const validate = ajv.compile(
  JSON.parse(
    readFileSync(
      new URL("vendor/cyclonedx/bom-1.6.schema.json", import.meta.url),
    ),
  ),
);

export function createSBOM(dependencies, identities, binaries) {
  const components = dependencies.map((dependency) => {
    const { ecosystem, name, version, scope, notices } = dependency;
    if (!["go", "npm", "go-toolchain"].includes(ecosystem) || !name || !version)
      throw new Error("Invalid dependency identity");
    const type =
      ecosystem === "go" ? "golang" : ecosystem === "npm" ? "npm" : "generic";
    const purl = `pkg:${type}/${name.split("/").map(encodeURIComponent).join("/")}@${encodeURIComponent(version)}`;
    if (!notices?.length) throw new Error(`Missing attribution: ${purl}`);
    return {
      type: "library",
      "bom-ref": purl,
      name,
      version,
      purl,
      ...(dependency.declared_license
        ? { licenses: [{ license: { name: dependency.declared_license } }] }
        : {}),
      properties: [
        { name: "agentos:inventory-scope", value: scope },
        ...notices.map((notice) => ({
          name: "agentos:license-file",
          value: `${notice.file}#sha256=${notice.sha256}`,
        })),
        ...(dependency.checksum
          ? [{ name: "agentos:go-module-sum", value: dependency.checksum }]
          : []),
      ],
    };
  });
  for (const identity of identities) {
    const bytes = binaries.get(identity.component);
    if (!bytes) throw new Error("Missing binary bytes");
    components.push({
      type: "application",
      "bom-ref": `agentos:binary:${identity.component}`,
      name: identity.component,
      version: identity.source.revision,
      hashes: [
        {
          alg: "SHA-256",
          content: createHash("sha256").update(bytes).digest("hex"),
        },
      ],
      properties: [
        { name: "agentos:os", value: identity.os },
        { name: "agentos:architecture", value: identity.architecture },
      ],
    });
  }
  if (
    new Set(components.map((item) => item["bom-ref"])).size !==
    components.length
  )
    throw new Error("Duplicate SBOM component");
  const bom = {
    $schema: "http://cyclonedx.org/schema/bom-1.6.schema.json",
    bomFormat: "CycloneDX",
    specVersion: "1.6",
    version: 1,
    metadata: {
      component: {
        type: "application",
        "bom-ref": "agentos:offline-package",
        name: "agentos-sqlite-core",
        version: identities[0].source.revision,
      },
      properties: [
        {
          name: "agentos:source-modified",
          value: String(identities[0].source.modified),
        },
        {
          name: "agentos:inventory-scope",
          value:
            "Linked Go modules, Go runtime, and installed workspace production npm superset. No exact transitive dependency graph or tree-shaken npm attribution is asserted.",
        },
        { name: "agentos:release-status", value: "internal-validation-only" },
      ],
    },
    components,
  };
  if (!validate(bom))
    throw new Error(
      `Invalid CycloneDX SBOM: ${ajv.errorsText(validate.errors)}`,
    );
  return bom;
}
