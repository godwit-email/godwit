$ErrorActionPreference = 'Stop'

# Only the Azure call and Windows certificate API are replaced; run the real signing script.
function Invoke-ArtifactSigning {
    param($Endpoint, $CodeSigningAccountName, $CertificateProfileName, $Files,
        $FileDigest, $TimestampRfc3161, $TimestampDigest,
        [switch]$ExcludeEnvironmentCredential, [switch]$ExcludeManagedIdentityCredential,
        [switch]$ExcludeInteractiveBrowserCredential)
    if ($signingFails) { throw 'Azure signing failed' }
    if ($Endpoint -ne 'https://eus.codesigning.azure.net/' -or
        $CodeSigningAccountName -ne 'BARANWANG' -or $CertificateProfileName -ne 'Godwit' -or
        $Files -ne $file -or $FileDigest -ne 'SHA256' -or
        $TimestampRfc3161 -ne 'http://timestamp.acs.microsoft.com' -or $TimestampDigest -ne 'SHA256' -or
        -not $ExcludeEnvironmentCredential -or -not $ExcludeManagedIdentityCredential -or
        -not $ExcludeInteractiveBrowserCredential) { throw "Unexpected signing request: $($PSBoundParameters | ConvertTo-Json -Compress)" }
}

function Get-AuthenticodeSignature {
    param($LiteralPath)
    if ($LiteralPath -ne $file) { throw 'Verifying the wrong file' }
    [pscustomobject]@{ Status = $status; TimeStamperCertificate = $timestamp }
}

$file = Join-Path ([IO.Path]::GetTempPath()) ('Godwit signing ' + [guid]::NewGuid() + '.exe')
try {
    Set-Content -LiteralPath $file -Value 'test'
    $status = 'Valid'
    $timestamp = [pscustomobject]@{ Subject = 'timestamp authority' }
    $signingFails = $false
    & "$PSScriptRoot/sign-windows.ps1" -Path $file

    foreach ($failure in 'Azure', 'InvalidSignature', 'MissingTimestamp') {
        $signingFails = $failure -eq 'Azure'
        $status = if ($failure -eq 'InvalidSignature') { 'HashMismatch' } else { 'Valid' }
        $timestamp = if ($failure -eq 'MissingTimestamp') { $null } else { [pscustomobject]@{ Subject = 'timestamp authority' } }
        $caught = $false
        try { & "$PSScriptRoot/sign-windows.ps1" -Path $file } catch { $caught = $true }
        if (-not $caught) { throw "Signing must stop for $failure" }
    }
    Write-Host 'Windows signing checks passed (success, Azure failure, invalid signature, missing timestamp).'
} finally {
    Remove-Item -LiteralPath $file
}
