# Install the latest Lectern release on Windows.
#
#   irm https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.ps1 | iex
#
# Downloads the zip for this CPU from the latest GitHub release, verifies it
# against checksums.txt, installs lectern.exe under
# $env:LOCALAPPDATA\Programs\lectern and adds that directory to the user PATH.
# On Windows the binary is the client (mcp, post, sessions, tasks…) against a
# Lectern server elsewhere; the control plane itself needs tmux (Linux, macOS
# or WSL).
$ErrorActionPreference = "Stop"
$repo = "JeremiahM37/lectern"
$version = if ($env:LECTERN_VERSION) { $env:LECTERN_VERSION } else { "latest" }
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$base = if ($version -eq "latest") { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/$version" }
$archive = "lectern_windows_$arch.zip"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("lectern-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $archive ($version)…"
  Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt")
  $want = (Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -match " $archive$" }) -split " " | Select-Object -First 1
  $got = (Get-FileHash (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLower()
  if (-not $want -or $want -ne $got) { throw "checksum mismatch for $archive" }
  $dir = if ($env:LECTERN_INSTALL_DIR) { $env:LECTERN_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\lectern" }
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force
  Copy-Item (Join-Path $tmp "lectern.exe") (Join-Path $dir "lectern.exe") -Force
  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  if (($userPath -split ";") -notcontains $dir) {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$dir", "User")
    $env:Path = "$env:Path;$dir"
    Write-Host "Added $dir to your user PATH (open a new terminal to pick it up)."
  }
  Write-Host ("Installed " + (& (Join-Path $dir "lectern.exe") version) + " to $dir")
  Write-Host "Set LECTERN_API to your Lectern server, e.g. `$env:LECTERN_API = 'http://aiserver:9110'`."
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
