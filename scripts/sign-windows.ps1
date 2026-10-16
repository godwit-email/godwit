param([Parameter(Mandatory)][string]$Path)

$ErrorActionPreference = 'Stop'
$Path = (Resolve-Path -LiteralPath $Path).Path

Invoke-ArtifactSigning -Endpoint 'https://eus.codesigning.azure.net/' `
    -CodeSigningAccountName 'BARANWANG' -CertificateProfileName 'Godwit' -Files $Path `
    -FileDigest SHA256 -TimestampRfc3161 'http://timestamp.acs.microsoft.com' -TimestampDigest SHA256 `
    -ExcludeEnvironmentCredential -ExcludeManagedIdentityCredential -ExcludeInteractiveBrowserCredential

# Azure certificates expire after 72 hours, so a successful signature also needs a timestamp.
$signature = Get-AuthenticodeSignature -LiteralPath $Path
if ($signature.Status -ne 'Valid' -or -not $signature.TimeStamperCertificate) {
    throw "Invalid or missing timestamped signature: $Path ($($signature.Status))"
}
