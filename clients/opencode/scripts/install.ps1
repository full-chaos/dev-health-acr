[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$packageRoot = Split-Path -Parent $PSScriptRoot
$configRoot = if ($env:OPENCODE_CONFIG_DIR) { $env:OPENCODE_CONFIG_DIR } else { Join-Path $HOME ".config/context-fabric-opencode" }
$ownerFile = ".context-fabric-owner.v1"
$ownerValue = "context-fabric-opencode.v1"

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "OPENCODE_CONFIG_DIR must be an absolute path" }
if (Test-Path -LiteralPath $configRoot) {
  $entries = Get-ChildItem -Force -LiteralPath $configRoot
  if ($entries.Count -eq 0) { Remove-Item -LiteralPath $configRoot -Force } else { throw "refusing to install into a non-empty target" }
}

$parent = Split-Path -Parent $configRoot
New-Item -ItemType Directory -Force -Path $parent | Out-Null
$stage = Join-Path $parent (".context-fabric-opencode." + [Guid]::NewGuid().ToString("N"))
$installed = $false
try {
  New-Item -ItemType Directory -Path $stage | Out-Null
  Copy-Item -Recurse -Force -Path (Join-Path $packageRoot "config/*") -Destination $stage
  Set-Content -NoNewline -Path (Join-Path $stage $ownerFile) -Value $ownerValue
  New-Item -ItemType SymbolicLink -Path $configRoot -Target (Split-Path -Leaf $stage) | Out-Null
  $installed = $true
} finally {
  if (-not $installed -and (Test-Path -LiteralPath $stage)) { Remove-Item -Recurse -Force -LiteralPath $stage }
}
Write-Output "installed Context Fabric OpenCode config at $configRoot"
