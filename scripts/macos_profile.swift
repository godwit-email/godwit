import CryptoKit
import Foundation

struct ProfileError: Error, CustomStringConvertible {
    let description: String
}

func verifyProfile(_ profile: [String: Any], entitlements: [String: Any], identity: String, identities: String) throws {
    guard let expiry = profile["ExpirationDate"] as? Date, expiry > Date() else {
        throw ProfileError(description: "Provisioning profile has expired")
    }
    guard (profile["Platform"] as? [String])?.contains("OSX") == true else {
        throw ProfileError(description: "Provisioning profile must support macOS")
    }
    let grants = profile["Entitlements"] as? [String: Any] ?? [:]
    for key in ["com.apple.application-identifier", "com.apple.developer.team-identifier", "com.apple.developer.usernotifications.communication"] {
        guard let expected = entitlements[key] as? NSObject,
              let granted = grants[key] as? NSObject, granted.isEqual(expected) else {
            throw ProfileError(description: "Provisioning profile does not authorize \(key)")
        }
    }
    guard (profile["DeveloperCertificates"] as? [Data] ?? []).contains(where: { certificate in
        let fingerprint = Insecure.SHA1.hash(data: certificate).map { String(format: "%02X", $0) }.joined()
        return identities.contains("\(fingerprint) \"\(identity)\"")
    }) else {
        throw ProfileError(description: "Provisioning profile does not include the signing certificate")
    }
}

#if !PROFILE_TEST
@main
struct ProfileCheck {
    static func security(_ arguments: [String]) throws -> Data {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/security")
        process.arguments = arguments
        let output = Pipe()
        process.standardOutput = output
        try process.run()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else {
            throw ProfileError(description: "security \(arguments[0]) failed (\(process.terminationStatus))")
        }
        return data
    }

    static func main() {
        do {
            let args = Array(CommandLine.arguments.dropFirst())
            guard args.count == 4 else {
                throw ProfileError(description: "Usage: macos-profile <profile> <entitlements> <identity> <keychain>")
            }
            let decoded = try security(["cms", "-D", "-i", args[0]])
            let entitlementData = try Data(contentsOf: URL(fileURLWithPath: args[1]))
            guard let profile = try PropertyListSerialization.propertyList(from: decoded, format: nil) as? [String: Any],
                  let entitlements = try PropertyListSerialization.propertyList(from: entitlementData, format: nil) as? [String: Any] else {
                throw ProfileError(description: "Profile and entitlements must be plist dictionaries")
            }
            let identities = try security(["find-identity", "-v", "-p", "codesigning", args[3]])
            try verifyProfile(profile, entitlements: entitlements, identity: args[2], identities: String(decoding: identities, as: UTF8.self))
            print("Communication notification profile matches the app and signing certificate.")
        } catch {
            FileHandle.standardError.write(Data("\(error)\n".utf8))
            exit(1)
        }
    }
}
#endif
