"""Check that a notification profile authorizes this app and signer."""

import datetime
import hashlib
import plistlib
import subprocess
import sys


def verify(profile, entitlements, identity, identities):
    if profile.get("ExpirationDate", datetime.datetime.min) <= datetime.datetime.now(datetime.timezone.utc).replace(tzinfo=None):
        raise ValueError("Provisioning profile has expired")
    if "OSX" not in profile.get("Platform", []):
        raise ValueError("Provisioning profile must support macOS")
    for key in ("com.apple.application-identifier", "com.apple.developer.team-identifier", "com.apple.developer.usernotifications.communication"):
        value = entitlements[key]
        if profile.get("Entitlements", {}).get(key) != value:
            raise ValueError(f"Provisioning profile does not authorize {key}")
    if not any(
        f'{hashlib.sha1(cert).hexdigest().upper()} "{identity}"' in identities
        for cert in profile.get("DeveloperCertificates", [])
    ):
        raise ValueError("Provisioning profile does not include the signing certificate")


if __name__ == "__main__":
    profile_path, entitlements_path, identity, keychain = sys.argv[1:]
    profile = plistlib.loads(subprocess.check_output(["security", "cms", "-D", "-i", profile_path]))
    with open(entitlements_path, "rb") as file:
        entitlements = plistlib.load(file)
    identities = subprocess.check_output(["security", "find-identity", "-v", "-p", "codesigning", keychain], text=True)
    verify(profile, entitlements, identity, identities)
    print("Communication notification profile matches the app and signing certificate.")
