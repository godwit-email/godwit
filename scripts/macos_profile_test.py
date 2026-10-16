import copy
import datetime
import hashlib
import unittest

from macos_profile import verify


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.entitlements = {
            "com.apple.application-identifier": "TEAM.email.godwit.app",
            "com.apple.developer.team-identifier": "TEAM",
            "com.apple.developer.usernotifications.communication": True,
        }
        self.identity = "Developer ID Application: Godwit (TEAM)"
        cert = b"certificate in the signing keychain"
        self.identities = f'1) {hashlib.sha1(cert).hexdigest().upper()} "{self.identity}"'
        self.profile = {
            "Platform": ["OSX"],
            "ExpirationDate": datetime.datetime(2044, 1, 1),
            "Entitlements": copy.copy(self.entitlements),
            "DeveloperCertificates": [cert],
        }

    def test_matching_profile(self):
        verify(self.profile, self.entitlements, self.identity, self.identities)

    def test_missing_or_wrong_entitlements(self):
        for key in self.entitlements:
            for value in (None, "wrong-team-or-app", False):
                with self.subTest(key=key, value=value):
                    p = copy.deepcopy(self.profile)
                    p["Entitlements"][key] = value
                    with self.assertRaisesRegex(ValueError, "does not authorize"):
                        verify(p, self.entitlements, self.identity, self.identities)

    def test_wrong_certificate_or_identity(self):
        for certificates, identity in (([], self.identity), ([b"another certificate"], self.identity), (self.profile["DeveloperCertificates"], "another identity")):
            with self.subTest(certificates=certificates, identity=identity):
                p = dict(self.profile, DeveloperCertificates=certificates)
                with self.assertRaisesRegex(ValueError, "signing certificate"):
                    verify(p, self.entitlements, identity, self.identities)

    def test_expired_or_missing_expiry(self):
        for expiry in (datetime.datetime(2000, 1, 1), datetime.datetime.min):
            with self.assertRaisesRegex(ValueError, "expired"):
                verify(dict(self.profile, ExpirationDate=expiry), self.entitlements, self.identity, self.identities)

    def test_wrong_platform(self):
        with self.assertRaisesRegex(ValueError, "macOS"):
            verify(dict(self.profile, Platform=["iOS"]), self.entitlements, self.identity, self.identities)
