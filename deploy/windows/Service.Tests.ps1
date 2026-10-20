Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'Service.psm1') -Force
function Assert-True($Condition, $Message) { if (-not $Condition) { throw $Message } }
function Assert-Fails([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "expected failure: $Pattern"
}
$root = Join-Path ([IO.Path]::GetTempPath()) ('agentos-service-' + [guid]::NewGuid())
[IO.Directory]::CreateDirectory($root) | Out-Null
try {
    $exe = Join-Path $root 'core & app.exe'
    $wrapper = Join-Path $root 'synthetic-wrapper.exe'
    $config = Join-Path $root 'private & config.json'
    [IO.File]::WriteAllText($exe, 'synthetic binary; never executed')
    [IO.File]::WriteAllText($wrapper, 'synthetic wrapper; never executed')
    [IO.File]::WriteAllText($config, '{"secret":"DO_NOT_EXPORT"}')
    $serviceArgs = @{Name='AgentOSTest'; Executable=$exe; Configuration=$config; Wrapper=$wrapper; WrapperSHA256=(Get-FileHash -LiteralPath $wrapper).Hash.ToLowerInvariant(); ServiceXML=(Join-Path $root 'service.xml')}
    $plan = New-AgentOSServicePlan @serviceArgs
    $preview = & (Join-Path $PSScriptRoot 'Register-Service.ps1') @serviceArgs
    Assert-True ($preview.XML -ceq $plan.XML) 'entrypoint preview mismatch'
    Assert-True (-not (Test-Path -LiteralPath $plan.ServiceXML)) 'plan mutated disk'
    $xml = [xml]$plan.XML
    Assert-True ($xml.service.executable -ceq $exe) 'XML escaping failed'
    Assert-True ($xml.service.serviceaccount.username -ceq 'NT AUTHORITY\LocalService') 'account mismatch'
    Assert-True ($xml.service.startmode -ceq 'Manual') 'unexpected automatic start'
    Assert-True (-not $plan.XML.Contains('DO_NOT_EXPORT')) 'configuration content leaked'
    $probe = @{Calls=0; File=''; Arguments=@()}
    $invoke = { param($File,$Arguments) $probe.Calls++; $probe.File=$File; $probe.Arguments=$Arguments; return 0 }.GetNewClosure()
    $result = Install-AgentOSService -Plan $plan -InvokeWrapper $invoke
    Assert-True ($result.Registered -and -not $result.Started) 'wrong registration result'
    Assert-True ($probe.Calls -eq 1 -and $probe.File -ceq $wrapper) 'wrong invocation count/path'
    Assert-True (($probe.Arguments -join '|') -ceq "install|$($plan.ServiceXML)|--no-elevate") 'unsafe command arguments'
    Assert-Fails { Install-AgentOSService -Plan $plan -InvokeWrapper $invoke } 'already exists|存在'
    Assert-True ($probe.Calls -eq 1) 'replayed registration'
    $serviceArgs.ServiceXML = Join-Path $root 'failure.xml'
    $failed = New-AgentOSServicePlan @serviceArgs
    Assert-Fails { Install-AgentOSService -Plan $failed -InvokeWrapper { return 5 } } 'failed_or_unknown'
    Assert-True (Test-Path -LiteralPath $failed.ServiceXML) 'removed unknown-outcome evidence'
    $serviceArgs.Name = 'bad"<name'
    Assert-Fails { New-AgentOSServicePlan @serviceArgs } 'invalid_name'
    $serviceArgs.Name = 'AgentOSTest'
    $serviceArgs.ServiceXML = Join-Path $root 'changed.xml'
    $changed = New-AgentOSServicePlan @serviceArgs
    [IO.File]::WriteAllText($wrapper, 'tampered')
    Assert-Fails { Install-AgentOSService -Plan $changed -InvokeWrapper $invoke } 'wrapper_changed'
    Assert-Fails { New-AgentOSServicePlan @serviceArgs } 'digest_mismatch'
    Assert-True (-not (Test-Path -LiteralPath $changed.ServiceXML)) 'changed wrapper wrote XML'
    $serviceArgs.Executable = 'relative.exe'
    Assert-Fails { New-AgentOSServicePlan @serviceArgs } 'absolute_literal_path_required'
    'Service script tests passed; SCM and executables were not invoked.'
} finally {
    $resolved = [IO.Path]::GetFullPath($root)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if (-not $resolved.StartsWith($temp, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFileName($resolved) -notlike 'agentos-service-*') { throw 'unsafe_test_cleanup' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
