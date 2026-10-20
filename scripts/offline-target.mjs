export function offlineTarget(
  value = process.platform === "win32" ? "windows-amd64" : "linux-amd64",
) {
  if (!["windows-amd64", "linux-amd64"].includes(value))
    throw new Error("Offline target must be windows-amd64 or linux-amd64");
  const os = value.split("-")[0];
  if (
    process.arch !== "x64" ||
    !["win32", "linux"].includes(process.platform) ||
    (os === "windows" && process.platform !== "win32")
  )
    throw new Error("Unsupported offline build host/target combination");
  return {
    name: value,
    os,
    extension: os === "windows" ? ".exe" : "",
    profile: `${value}-sqlite-core`,
  };
}
