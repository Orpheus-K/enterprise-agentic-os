param([string]$PackagePath = $PSScriptRoot)
$ErrorActionPreference = 'Stop'
$packageRoot = (Resolve-Path -LiteralPath $PackagePath).Path
$manifest = Get-Content -LiteralPath (Join-Path $packageRoot 'MANIFEST.json') -Raw | ConvertFrom-Json
if ($manifest.format -ne 1 -or $manifest.profile -ne 'windows-amd64-sqlite-core' -or $manifest.files.Count -eq 0) {
    throw 'Unsupported or empty package manifest'
}
$expected = @{}
foreach ($entry in $manifest.files) {
    if ($entry.path -notmatch '^[a-zA-Z0-9@._/-]+$' -or $entry.path -match '(^|/)\.\.(/|$)' -or [IO.Path]::IsPathRooted($entry.path) -or $entry.sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'Invalid manifest entry'
    }
    $target = [IO.Path]::GetFullPath((Join-Path $packageRoot $entry.path))
    if (-not $target.StartsWith($packageRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or $expected.ContainsKey($target)) {
        throw 'Escaped or duplicate manifest path'
    }
    $expected[$target] = $true
    $item = Get-Item -LiteralPath $target
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Nonregular package file' }
    if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant() -ne $entry.sha256) {
        throw "Checksum mismatch: $($entry.path)"
    }
}
foreach ($item in Get-ChildItem -LiteralPath $packageRoot -Recurse -Force) {
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Package contains a reparse point' }
    if (-not $item.PSIsContainer -and $item.FullName -ne (Join-Path $packageRoot 'MANIFEST.json') -and -not $expected.ContainsKey($item.FullName)) {
        throw "Unexpected package file: $($item.Name)"
    }
}
Write-Output "Verified $($expected.Count) files; profile=$($manifest.profile); status=$($manifest.release_status)"
