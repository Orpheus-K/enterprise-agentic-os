Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-AgentOSFile {
    param([string]$Path)
    if (-not [IO.Path]::IsPathFullyQualified($Path) -or $Path -match '[\r\n%]') { throw 'service_absolute_literal_path_required' }
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'service_regular_file_required' }
    return $item.FullName
}

function New-AgentOSServicePlan {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string]$Configuration,
        [Parameter(Mandatory)][string]$Wrapper,
        [Parameter(Mandatory)][string]$WrapperSHA256,
        [Parameter(Mandatory)][string]$ServiceXML
    )
    if ($Name -cnotmatch '^[A-Za-z][A-Za-z0-9_-]{0,63}$') { throw 'service_invalid_name' }
    if ($WrapperSHA256 -cnotmatch '^[0-9a-f]{64}$') { throw 'service_invalid_wrapper_digest' }
    $exe = Get-AgentOSFile $Executable
    $config = Get-AgentOSFile $Configuration
    $wrapperPath = Get-AgentOSFile $Wrapper
    if ((Get-FileHash -LiteralPath $wrapperPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne $WrapperSHA256) { throw 'service_wrapper_digest_mismatch' }
    if (-not [IO.Path]::IsPathFullyQualified($ServiceXML) -or $ServiceXML -match '[\r\n%]' -or [IO.Path]::GetExtension($ServiceXML) -ine '.xml') { throw 'service_invalid_xml_path' }
    $xmlPath = [IO.Path]::GetFullPath($ServiceXML)
    if (Test-Path -LiteralPath $xmlPath) { throw 'service_xml_already_exists' }
    if (-not (Test-Path -LiteralPath ([IO.Path]::GetDirectoryName($xmlPath)) -PathType Container)) { throw 'service_xml_parent_missing' }
    $escapedExe = [Security.SecurityElement]::Escape($exe)
    $escapedConfig = [Security.SecurityElement]::Escape($config)
    $escapedWork = [Security.SecurityElement]::Escape([IO.Path]::GetDirectoryName($exe))
    $escapedLogs = [Security.SecurityElement]::Escape([IO.Path]::GetDirectoryName($xmlPath))
    $xml = @"
<service>
  <id>$Name</id>
  <name>$Name</name>
  <description>Enterprise Agentic OS core</description>
  <executable>$escapedExe</executable>
  <workingdirectory>$escapedWork</workingdirectory>
  <startmode>Manual</startmode>
  <serviceaccount><username>NT AUTHORITY\LocalService</username></serviceaccount>
  <env name="AGENTOS_CHANNEL_CONFIG" value="$escapedConfig" />
  <env name="AGENTOS_HTTP_ADDR" value="127.0.0.1:8080" />
  <logpath>$escapedLogs</logpath>
  <log mode="roll" />
</service>
"@
    return [pscustomobject]@{ Name=$Name; Wrapper=$wrapperPath; WrapperSHA256=$WrapperSHA256; ServiceXML=$xmlPath; XML=$xml }
}

function Install-AgentOSService {
    param(
        [Parameter(Mandatory)]$Plan,
        [scriptblock]$InvokeWrapper = { param($File, $Arguments) & $File @Arguments | Out-Null; return $LASTEXITCODE }
    )
    # The injectable command boundary lets tests exercise registration without touching SCM.
    if ((Get-FileHash -LiteralPath $Plan.Wrapper -Algorithm SHA256).Hash.ToLowerInvariant() -cne $Plan.WrapperSHA256) { throw 'service_wrapper_changed' }
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes($Plan.XML)
    $stream = [IO.File]::Open($Plan.ServiceXML, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
    $result = & $InvokeWrapper $Plan.Wrapper @('install', $Plan.ServiceXML, '--no-elevate')
    if ($result -isnot [int] -or $result -ne 0) { throw 'service_install_failed_or_unknown; retain XML and inspect SCM before retry' }
    return [pscustomobject]@{ Registered=$true; Started=$false; Name=$Plan.Name }
}

Export-ModuleMember -Function New-AgentOSServicePlan, Install-AgentOSService
