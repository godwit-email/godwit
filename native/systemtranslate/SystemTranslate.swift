// The macOS Translation framework behind a C interface, for Godwit's
// built-in system-translation plugin (cmd/godwit/systemtranslate_darwin.go
// loads it with purego). Built by scripts/build-system-translate.sh into
// resources/darwin-<arch>/, which the app ships in Contents/Resources.
//
// The headless TranslationSession needs macOS 26; the library loads on older
// systems, where godwit_st_available says no. Translation runs on this Mac;
// a language pair not downloaded fails with not_installed: the headless
// session cannot ask to download it. The framework finishes its work on the
// main dispatch queue, which the app's AppKit loop drains: a process
// without one (godwit's headless mode) would wait forever.

import Foundation
import NaturalLanguage
import Translation

private struct Request: Decodable {
    let texts: [String]
    let source: String
    let target: String
}

private struct Answer: Encodable {
    var texts: [String]? = nil
    var source: String? = nil
    var error: Failure? = nil
}

private struct Failure: Encodable {
    let code: String // not_installed, unsupported_language, invalid_request, internal
    let message: String
}

/// 1 when this macOS translates without a window (macOS 26 or later).
@_cdecl("godwit_st_available")
public func available() -> Int32 {
    if #available(macOS 26, *) { return 1 }
    return 0
}

/// Translates {"texts","source","target"} (source "" detects each text's
/// language) into a malloc'd NUL-terminated JSON answer, {"texts","source"}
/// with the mail's main language, or {"error": {"code","message"}}, which the
/// caller frees with godwit_st_free. It blocks until done: call it off the
/// main thread, while the main queue runs.
@_cdecl("godwit_st_translate")
public func translate(_ req: UnsafePointer<CChar>) -> UnsafeMutablePointer<CChar> {
    let json = String(cString: req)
    var answer = Answer()
    let done = DispatchSemaphore(value: 0)
    Task.detached {
        answer = await run(json)
        done.signal()
    }
    done.wait()
    let data = (try? JSONEncoder().encode(answer)) ?? Data(#"{"error":{"code":"internal","message":"encoding"}}"#.utf8)
    return strdup(String(decoding: data, as: UTF8.self))!
}

@_cdecl("godwit_st_free")
public func free(_ p: UnsafeMutablePointer<CChar>?) {
    Foundation.free(p)
}

/// Returns a malloc'd NUL-terminated JSON array of installed language identifiers (e.g. ["de", "en-US", "en-GB", "fr", "tr"]),
/// which the caller frees with godwit_st_free.
@_cdecl("godwit_st_installed_languages")
public func installedLanguages() -> UnsafeMutablePointer<CChar> {
    var tags: [String] = []
    if #available(macOS 26, *) {
        var installed: [Locale.Language] = []
        let done = DispatchSemaphore(value: 0)
        Task.detached {
            let availability = LanguageAvailability()
            let supported = await availability.supportedLanguages
            for s in supported {
                if await availability.status(from: s, to: nil) == .installed {
                    installed.append(s)
                }
            }
            // Apple's availability.status(from: s, to: nil) only evaluates whether s is installed
            // as an offline source language. For languages that serve as translation targets
            // (such as English in downloaded pairs), from: s, to: nil returns .supported rather than .installed.
            // Check remaining supported languages as targets from any installed source:
            for s in supported where !installed.contains(s) {
                for other in installed {
                    if await availability.status(from: other, to: s) == .installed {
                        installed.append(s)
                        break
                    }
                }
            }
            done.signal()
        }
        done.wait()
        var codeCounts: [String: Int] = [:]
        for s in installed {
            if let code = s.languageCode?.identifier {
                codeCounts[code, default: 0] += 1
            }
        }
        for s in installed {
            guard let code = s.languageCode?.identifier else { continue }
            if code == "zh", let script = s.script?.identifier {
                tags.append("zh-\(script)")
            } else if (codeCounts[code] ?? 0) > 1, let region = s.region?.identifier {
                tags.append("\(code)-\(region)")
            } else {
                tags.append(code)
            }
        }
    }
    let unique = Array(NSOrderedSet(array: tags)).compactMap { $0 as? String }
    let data = (try? JSONEncoder().encode(unique)) ?? Data("[]".utf8)
    return strdup(String(decoding: data, as: UTF8.self))!
}

private func run(_ json: String) async -> Answer {
    guard let req = try? JSONDecoder().decode(Request.self, from: Data(json.utf8)) else {
        return Answer(error: Failure(code: "invalid_request", message: "not {texts, source, target}"))
    }
    guard #available(macOS 26, *) else {
        return Answer(error: Failure(code: "unsupported_language", message: "macOS 26 or later translates on this Mac"))
    }
    // Each text's language, a text too short to tell in the mail's main
    // one: the language most of its text is in, by UTF-8 bytes, which weigh
    // a CJK character as about the words it holds. A quoted reply or a
    // signature may be in another.
    var detected: [String?] = req.texts.map { req.source.isEmpty ? detect($0) : req.source }
    var weight: [String: Int] = [:]
    for (i, lang) in detected.enumerated() {
        if let lang { weight[lang, default: 0] += req.texts[i].utf8.count }
    }
    guard let main = weight.max(by: { $0.value < $1.value })?.key else {
        return Answer(texts: req.texts, source: "")
    }
    var groups: [String: [Int]] = [:]
    for i in detected.indices {
        detected[i] = detected[i] ?? main
        groups[detected[i]!, default: []].append(i)
    }
    let tgt = full(Locale.Language(identifier: req.target))
    var out = req.texts
    var translated: [String: Int] = [:]
    for (source, indices) in groups {
        let src = full(Locale.Language(identifier: source))
        // One language in one script needs no translation, and the framework has no such pair.
        if src.languageCode == tgt.languageCode && src.script == tgt.script {
            continue
        }
        // Nor does it convert between Chinese scripts: ICU does.
        if src.languageCode == "zh" && tgt.languageCode == "zh", let s = src.script?.identifier, let t = tgt.script?.identifier {
            for i in indices {
                out[i] = req.texts[i].applyingTransform(StringTransform("\(s)-\(t)"), reverse: false) ?? req.texts[i]
            }
            translated[source] = weight[source, default: 0]
            continue
        }
        do {
            let session = TranslationSession(installedSource: src, target: tgt)
            let requests = indices.map { TranslationSession.Request(sourceText: req.texts[$0], clientIdentifier: String($0)) }
            for r in try await session.translations(from: requests) {
                if let id = r.clientIdentifier, let i = Int(id), out.indices.contains(i) {
                    out[i] = r.targetText
                }
            }
            translated[source] = weight[source, default: 0]
        } catch {
            // Another language's texts stay as they are; the main one's failure is the answer.
            if source != main {
                continue
            }
            switch error {
            case TranslationError.notInstalled:
                return Answer(error: Failure(code: "not_installed", message: "\(source) → \(req.target) is not downloaded"))
            case TranslationError.unsupportedLanguagePairing, TranslationError.unsupportedSourceLanguage,
                TranslationError.unsupportedTargetLanguage, TranslationError.unableToIdentifyLanguage:
                return Answer(error: Failure(code: "unsupported_language", message: "\(source) → \(req.target) is not supported"))
            default:
                return Answer(error: Failure(code: "internal", message: String(describing: error)))
            }
        }
    }
    // The source is the main language unless the mail is mostly in the
    // target already: then it is the main one of the texts translated, so
    // a Chinese mail with an English paragraph is not "needs no translation".
    let source = translated[main] != nil ? main : translated.max(by: { $0.value < $1.value })?.key ?? main
    return Answer(texts: out, source: source)
}

/// The language of text, nil when it cannot tell (BCP 47: "en", "zh-Hant").
private func detect(_ text: String) -> String? {
    let rec = NLLanguageRecognizer()
    rec.processString(text)
    guard let lang = rec.dominantLanguage, lang != .undetermined else { return nil }
    return lang.rawValue
}

/// language with its likely script and region: zh-CN is zh-Hans-CN, en is en-Latn-US.
@available(macOS 26, *)
private func full(_ l: Locale.Language) -> Locale.Language {
    Locale.Language(identifier: l.maximalIdentifier)
}
