import Foundation

@main
struct ProfileTests {
    static func main() throws {
        let entitlements: [String: Any] = [
            "com.apple.application-identifier": "TEAM.email.godwit.app",
            "com.apple.developer.team-identifier": "TEAM",
            "com.apple.developer.usernotifications.communication": true,
        ]
        let identity = "Developer ID Application: Godwit (TEAM)"
        let certificate = Data("abc".utf8)
        let identities = "1) A9993E364706816ABA3E25717850C26C9CD0D89D \"\(identity)\""
        let profile: [String: Any] = [
            "Platform": ["OSX"],
            "ExpirationDate": Date(timeIntervalSince1970: 4102444800),
            "Entitlements": entitlements,
            "DeveloperCertificates": [certificate],
        ]
        try verifyProfile(profile, entitlements: entitlements, identity: identity, identities: identities)

        func rejects(_ candidate: [String: Any], identity signer: String? = nil, reason: String) throws {
            do {
                try verifyProfile(candidate, entitlements: entitlements, identity: signer ?? identity, identities: identities)
            } catch let error as ProfileError {
                guard error.description.contains(reason) else {
                    throw ProfileError(description: "Expected \(reason), got \(error)")
                }
                return
            }
            throw ProfileError(description: "Accepted invalid profile: \(reason)")
        }

        for key in entitlements.keys {
            for value: Any in [NSNull(), "wrong-team-or-app", false] {
                var grants = entitlements
                grants[key] = value
                var candidate = profile
                candidate["Entitlements"] = grants
                try rejects(candidate, reason: "does not authorize")
            }
            var expected = entitlements
            expected.removeValue(forKey: key)
            do {
                try verifyProfile(profile, entitlements: expected, identity: identity, identities: identities)
                throw ProfileError(description: "Accepted missing expected entitlement")
            } catch let error as ProfileError where error.description.contains("does not authorize") {}
        }
        for certificates in [[], [Data("another certificate".utf8)]] {
            var candidate = profile
            candidate["DeveloperCertificates"] = certificates
            try rejects(candidate, reason: "signing certificate")
        }
        try rejects(profile, identity: "another identity", reason: "signing certificate")
        for expiry: Any in [Date(timeIntervalSince1970: 0), NSNull()] {
            var candidate = profile
            candidate["ExpirationDate"] = expiry
            try rejects(candidate, reason: "expired")
        }
        var candidate = profile
        candidate["Platform"] = ["iOS"]
        try rejects(candidate, reason: "macOS")
        print("Profile checks passed: matching profile, entitlements, certificate, expiry and platform.")
    }
}
