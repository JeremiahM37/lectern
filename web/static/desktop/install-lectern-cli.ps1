# Windows terminal client: run the console on the server over OpenSSH.
param([string]$Server = 'lectern', [string]$Api = 'http://127.0.0.1:9110')
$ErrorActionPreference = 'Stop'
if ($Server -notmatch '^[a-zA-Z0-9_@.:-]+$' -or $Server.StartsWith('-')) { throw 'Invalid SSH alias' }
if ($Api -notmatch '^https?://[^\s]+$') { throw 'API must be an http(s) URL' }
Get-Command ssh -ErrorAction Stop | Out-Null
$dir = Join-Path $env:LOCALAPPDATA 'Lectern\cli'
New-Item -ItemType Directory -Force $dir | Out-Null
$launcher = @'
$server = '__SERVER__'
$api = '__API__'
function Quote-Sh([string]$value) { $q = [char]39; $d = [char]34; return "$q" + $value.Replace("$q", "$q$d$q$d$q") + "$q" }
function Remote-Command([string[]]$parts) {
  return 'LECTERN_API=' + (Quote-Sh $api) + ' /usr/local/bin/lectern ' + (($parts | ForEach-Object { Quote-Sh $_ }) -join ' ')
}
if ($args.Count -eq 0) { & ssh -tt $server (Remote-Command @('console')); exit $LASTEXITCODE }
# Transfer local context files before invoking the server's upload command.
if ($args[0] -eq 'upload') {
  if ($args.Count -ne 4) { throw 'Usage: lectern upload KIND ID FILE' }
  if ($args[1] -notin @('session','attempt','project','task') -or $args[2] -notmatch '^[1-9][0-9]*$') { throw 'Invalid attachment' }
  $file = Get-Item -LiteralPath $args[3]
  $extension = $file.Extension
  if ($extension -notmatch '^\.[a-zA-Z0-9]+$') { $extension = '' }
  $remote = (& ssh $server 'mktemp -d /tmp/lectern-upload-XXXXXXXX').Trim()
  if ($LASTEXITCODE -ne 0 -or $remote -notmatch '^/tmp/lectern-upload-[a-zA-Z0-9]+$') { throw 'Cannot stage upload' }
  try {
    & scp -- $file.FullName "${server}:$remote/context$extension"
    if ($LASTEXITCODE -ne 0) { throw 'Transfer failed' }
    & ssh $server (Remote-Command @('upload', $args[1], $args[2], "$remote/context$extension"))
    $result = $LASTEXITCODE
  } finally { & ssh $server "rm -rf -- $remote" | Out-Null }
  exit $result
}
# Download to a server-side staging file, then copy to the requested local path.
if ($args[0] -eq 'download') {
  if ($args.Count -ne 5) { throw 'Usage: lectern download KIND ID REMOTE LOCAL' }
  if ($args[1] -notin @('session','attempt','project') -or $args[2] -notmatch '^[1-9][0-9]*$') { throw 'Invalid attachment' }
  if (Test-Path -LiteralPath $args[4]) { throw 'Destination already exists' }
  $remote = (& ssh $server 'mktemp -d /tmp/lectern-download-XXXXXXXX').Trim()
  if ($LASTEXITCODE -ne 0 -or $remote -notmatch '^/tmp/lectern-download-[a-zA-Z0-9]+$') { throw 'Cannot stage download' }
  try {
    & ssh $server (Remote-Command @('download', $args[1], $args[2], $args[3], "$remote/artifact"))
    if ($LASTEXITCODE -ne 0) { throw 'Download failed' }
    & scp "${server}:$remote/artifact" $args[4]
    $result = $LASTEXITCODE
  } finally { & ssh $server "rm -rf -- $remote" | Out-Null }
  exit $result
}
$command = Remote-Command $args
if ($args[0] -in @('console','tui','attach')) { & ssh -tt $server $command }
else { & ssh $server $command }
exit $LASTEXITCODE
'@
$apiLiteral = $Api.Replace("'", "''")
$launcher.Replace('__SERVER__', $Server).Replace('__API__', $apiLiteral) | Set-Content (Join-Path $dir 'lectern.ps1')
'@powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0lectern.ps1" %*' | Set-Content (Join-Path $dir 'lectern.cmd')
$userPath = [Environment]::GetEnvironmentVariable('Path','User')
if (($userPath -split ';') -notcontains $dir) { [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User') }
$env:Path += ";$dir"
Write-Host "Installed. Run lectern from a new terminal. Remote commands use $Api and your existing SSH keys/host verification."
