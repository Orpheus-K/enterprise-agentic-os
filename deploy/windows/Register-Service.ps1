[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Name,
    [Parameter(Mandatory)][string]$Executable,
    [Parameter(Mandatory)][string]$Configuration,
    [Parameter(Mandatory)][string]$Wrapper,
    [Parameter(Mandatory)][string]$WrapperSHA256,
    [Parameter(Mandatory)][string]$ServiceXML,
    [switch]$Register
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'service_windows_required' }
Import-Module (Join-Path $PSScriptRoot 'Service.psm1') -Force
$parameters = @{} + $PSBoundParameters
[void]$parameters.Remove('Register')
$plan = New-AgentOSServicePlan @parameters
if (-not $Register) { $plan; return }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
try {
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'service_administrator_required' }
} finally { $identity.Dispose() }
Install-AgentOSService -Plan $plan
