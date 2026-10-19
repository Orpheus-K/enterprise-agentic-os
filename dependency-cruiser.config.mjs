export default {
  forbidden: [
    { name: "no-cycles", severity: "error", from: {}, to: { circular: true } },
    {
      name: "no-unresolved-imports",
      severity: "error",
      from: {},
      to: { couldNotResolve: true },
    },
    {
      name: "packages-do-not-import-apps",
      severity: "error",
      from: { path: "^packages/" },
      to: { path: "^apps/" },
    },
    {
      name: "ui-does-not-import-business",
      severity: "error",
      from: { path: "^packages/ui/" },
      to: { path: "^(packages/api-client|services|contracts)/" },
    },
    {
      name: "client-does-not-import-ui",
      severity: "error",
      from: { path: "^packages/api-client/" },
      to: { path: "^packages/ui/" },
    },
    {
      name: "no-source-deep-imports",
      severity: "error",
      from: { path: "^apps/" },
      to: { dependencyTypes: ["local"], path: "^packages/" },
      comment: "Applications must import published workspace exports.",
    },
  ],
  options: {
    doNotFollow: { path: "node_modules" },
    tsPreCompilationDeps: true,
    enhancedResolveOptions: {
      exportsFields: ["exports"],
      conditionNames: ["import", "default", "types"],
    },
  },
};
