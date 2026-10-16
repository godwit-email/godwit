$ErrorActionPreference = 'Stop'
if ($env:RELEASE_TAG -notmatch '^v\d+\.\d+\.\d+$') { throw 'Invalid release tag' }
$version = $env:RELEASE_TAG.Substring(1)
$arch = $env:RELEASE_ARCH
if ($arch -notin @('amd64', 'arm64')) { throw 'Invalid architecture' }
$expectedArch = @{ amd64 = 'X64'; arm64 = 'Arm64' }[$arch]
if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() -ne $expectedArch) { throw 'Runner architecture mismatch' }

function Assert-Signed($path) {
    $signature = Get-AuthenticodeSignature -LiteralPath $path
    if ($signature.Status -ne 'Valid') { throw "Invalid signature on ${path}: $($signature.StatusMessage)" }
    if ($signature.SignerCertificate.Subject -ne 'CN=BARANWANG LTD, O=BARANWANG LTD, L=London, C=GB') { throw "Unexpected publisher: $($signature.SignerCertificate.Subject)" }
    if (-not $signature.TimeStamperCertificate) { throw "Missing timestamp on $path" }
    Write-Host "PASS signature: $path; publisher=$($signature.SignerCertificate.Subject); timestamp=$($signature.TimeStamperCertificate.Subject)"
    Get-FileHash -LiteralPath $path -Algorithm SHA256 | Format-List
}

$work = Join-Path $env:RUNNER_TEMP 'published-release-verification'
New-Item -ItemType Directory -Path $work | Out-Null
$manifest = Invoke-RestMethod "https://dl.godwit.email/update-windows-$arch.json"
if ($manifest.version -ne $version) { throw "Unexpected update version: $($manifest.version)" }
$archiveUrl = "https://dl.godwit.email/godwit-$version-windows-$arch.tar.gz"
if ($manifest.url -ne $archiveUrl) { throw 'Unexpected update archive URL' }
$archive = Join-Path $work 'update.tar.gz'
Invoke-WebRequest $archiveUrl -OutFile $archive
if ((Get-Item $archive).Length -ne $manifest.size) { throw 'Update archive size mismatch' }
$payload = Join-Path $work 'payload'
New-Item -ItemType Directory -Path $payload | Out-Null
tar -xzf $archive -C $payload
if ($LASTEXITCODE -ne 0) { throw 'Cannot extract update archive' }
foreach ($name in @('Godwit.exe', 'mygo-sqlite3.dll')) { Assert-Signed (Join-Path $payload $name) }

$installer = Join-Path $work 'GodwitSetup.exe'
$installerUrl = "https://dl.godwit.email/" + [Uri]::EscapeDataString("Godwit Setup $version $arch.exe")
Invoke-WebRequest $installerUrl -OutFile $installer
Assert-Signed $installer
$install = Join-Path $env:LOCALAPPDATA 'Programs\Godwit'
$registry = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\email.godwit.app'
if ((Test-Path $install) -or (Test-Path $registry)) { throw 'Runner already has Godwit installed' }
$setup = Start-Process $installer -ArgumentList '/S' -PassThru
if (-not $setup.WaitForExit(120000)) { $setup.Kill(); throw 'Installation timed out' }
if ($setup.ExitCode -ne 0) { throw "Installation failed: $($setup.ExitCode)" }
if ((Get-ItemProperty $registry).DisplayVersion -ne $version) { throw 'Installed version mismatch' }
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'Godwit.lnk'
if (-not (Test-Path $shortcut)) { throw 'Start menu shortcut missing' }
foreach ($name in @('Godwit.exe', 'mygo-sqlite3.dll', 'Uninstall.exe')) { Assert-Signed (Join-Path $install $name) }
foreach ($name in @('Godwit.exe', 'mygo-sqlite3.dll')) {
    if ((Get-FileHash (Join-Path $install $name)).Hash -ne (Get-FileHash (Join-Path $payload $name)).Hash) { throw "Installed file differs from update archive: $name" }
}
Write-Host "PASS installation: version=$version; architecture=$arch; files match published update"
$exe = Join-Path $install 'Godwit.exe'
$reportedVersion = & $exe --version
if ($LASTEXITCODE -ne 0 -or "$reportedVersion" -notmatch [regex]::Escape($version)) { throw "Executable version check failed: $reportedVersion" }
$app = Start-Process $exe -PassThru
try {
    Start-Sleep -Seconds 15
    if ($app.HasExited) { throw "Application exited during startup: $($app.ExitCode)" }
    Write-Host "PASS application startup: process $($app.Id) remained running for 15 seconds"
} finally {
    if (-not $app.HasExited) { Stop-Process -Id $app.Id -Force; $app.WaitForExit() }
}
$uninstaller = Start-Process (Join-Path $install 'Uninstall.exe') -ArgumentList '/S' -PassThru
if (-not $uninstaller.WaitForExit(60000)) { $uninstaller.Kill(); throw 'Uninstaller launch timed out' }
if ($uninstaller.ExitCode -ne 0) { throw "Uninstaller failed: $($uninstaller.ExitCode)" }
$deadline = (Get-Date).AddSeconds(45)
while ((Test-Path $install) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 250 }
if ((Test-Path $install) -or (Test-Path $registry) -or (Test-Path $shortcut)) { throw 'Uninstall left app files, registry or shortcut behind' }
Write-Host 'PASS uninstall: app directory, uninstall registration and shortcut removed'
