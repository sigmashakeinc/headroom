<#
.SYNOPSIS
  headroom installer for Windows x64 (amd64).

.DESCRIPTION
  powershell -ExecutionPolicy ByPass -c "irm https://raw.githubusercontent.com/sigmashakeinc/headroom/main/install.ps1 | iex"

  Environment:
    VERSION                Release tag to install (for example v0.1.0). Default: latest.
    INSTALL_DIR            Install directory. Default: $env:LOCALAPPDATA\Programs\headroom.
    HEADROOM_NO_MODIFY_PATH=1   Do not change the user PATH.
    HEADROOM_FROM_FILE     Same as -FromFile (works with "irm | iex").
    HEADROOM_CHECKSUMS_FILE  Same as -ChecksumsFile (works with "irm | iex").

.PARAMETER FromFile
  Install from a local .zip archive. No download.

.PARAMETER ChecksumsFile
  Path to a local checksums.txt for -FromFile. The script verifies the archive
  sha256 against the entry for its file name. It fails on mismatch or missing
  entry. Without it, verification is skipped and a warning is printed.
#>
param(
  [string]$FromFile = $env:HEADROOM_FROM_FILE,
  [string]$ChecksumsFile = $env:HEADROOM_CHECKSUMS_FILE
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch { }

$Repo = 'sigmashakeinc/headroom'
$Base = "https://github.com/$Repo"

function Fail([string]$Message) {
  throw "install.ps1: $Message"
}

$InstallDir = $env:INSTALL_DIR
if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\headroom' }

$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("headroom-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $Tmp | Out-Null

try {
  if ($FromFile) {
    if (-not (Test-Path -LiteralPath $FromFile -PathType Leaf)) { Fail "file not found: $FromFile" }
    $Archive = (Resolve-Path -LiteralPath $FromFile).Path
    Write-Host "Installing from local archive: $Archive"
    if ($ChecksumsFile) {
      if (-not (Test-Path -LiteralPath $ChecksumsFile -PathType Leaf)) { Fail "checksums file not found: $ChecksumsFile" }
      $LocalName = Split-Path -Leaf $Archive
      $want = $null
      foreach ($line in Get-Content -LiteralPath $ChecksumsFile) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2 -and ($parts[1].TrimStart('*') -eq $LocalName)) { $want = $parts[0]; break }
      }
      if (-not $want) { Fail "no checksum listed for $LocalName. Nothing was installed." }
      $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash
      if ($want.ToLower() -ne $got.ToLower()) {
        Fail "checksum mismatch for $LocalName (expected $want, got $got). Nothing was installed."
      }
      Write-Host 'Checksum OK.'
    } else {
      Write-Warning 'No checksums file given. Verification of the local archive is skipped.'
    }
  } else {
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    if ($arch -eq 'AMD64') {
      $Arch = 'amd64'
    } else {
      $archLower = "$arch".ToLower()
      [Console]::Error.WriteLine("headroom: unsupported platform windows/${archLower}; supported: linux x64, macOS arm64, Windows x64")
      exit 1
    }

    $Tag = $env:VERSION
    if (-not $Tag) {
      $req = [Net.WebRequest]::Create("$Base/releases/latest")
      $req.AllowAutoRedirect = $false
      $resp = $req.GetResponse()
      try { $loc = $resp.Headers['Location'] } finally { $resp.Close() }
      if (-not $loc) { Fail 'cannot resolve the latest release' }
      $Tag = ($loc -split '/tag/')[-1]
    }

    $Name = "headroom_${Tag}_windows_${Arch}.zip"
    Write-Host "Installing headroom $Tag (windows/$Arch)"
    $Archive = Join-Path $Tmp $Name
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/releases/download/$Tag/$Name" -OutFile $Archive
    $Sums = Join-Path $Tmp 'checksums.txt'
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/releases/download/$Tag/checksums.txt" -OutFile $Sums

    $want = $null
    foreach ($line in Get-Content -LiteralPath $Sums) {
      $parts = $line -split '\s+', 2
      if ($parts.Count -eq 2 -and ($parts[1].TrimStart('*') -eq $Name)) { $want = $parts[0]; break }
    }
    if (-not $want) { Fail "no checksum listed for $Name" }
    $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash
    if ($want.ToLower() -ne $got.ToLower()) {
      Fail "checksum mismatch for $Name (expected $want, got $got). Nothing was installed."
    }
    Write-Host 'Checksum OK.'
  }

  $Extract = Join-Path $Tmp 'x'
  Expand-Archive -LiteralPath $Archive -DestinationPath $Extract -Force
  $Exe = Join-Path $Extract 'headroom.exe'
  if (-not (Test-Path -LiteralPath $Exe)) { Fail 'archive does not contain headroom.exe' }

  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  Copy-Item -LiteralPath $Exe -Destination (Join-Path $InstallDir 'headroom.exe') -Force
  Write-Host "Installed: $(Join-Path $InstallDir 'headroom.exe')"

  if ($env:HEADROOM_NO_MODIFY_PATH -ne '1') {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @()
    if ($userPath) { $entries = $userPath -split ';' | Where-Object { $_ } }
    if ($entries -notcontains $InstallDir) {
      $new = (($entries + $InstallDir) -join ';')
      [Environment]::SetEnvironmentVariable('Path', $new, 'User')
      Write-Host "Added $InstallDir to your user PATH. Open a new terminal to use headroom."
    }
  }
} finally {
  Remove-Item -Recurse -Force -LiteralPath $Tmp -ErrorAction SilentlyContinue
}
