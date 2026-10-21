import Foundation
import FoundationModels
import NaturalLanguage

struct RequestPayload: Decodable {
    var system: String?
    var text: String
    var detectLanguage: Bool?
    var temperature: Double?
}

struct ResponsePayload: Encodable {
    var success: Bool
    var text: String?
    var error: String?
}

@main
struct AppleIntelligenceCLI {
    static func main() async {
        let args = CommandLine.arguments

        if args.contains("--check") {
            let model = SystemLanguageModel(guardrails: .permissiveContentTransformations)
            let isAvail = (model.availability == .available)
            let out = ["available": isAvail]
            if let data = try? JSONSerialization.data(withJSONObject: out),
               let str = String(data: data, encoding: .utf8) {
                print(str)
            }
            return
        }

        var inputData: Data?

        if let b64Idx = args.firstIndex(of: "--b64"), b64Idx + 1 < args.count {
            let b64Str = args[b64Idx + 1]
            inputData = Data(base64Encoded: b64Str)
        } else if let jsonIdx = args.firstIndex(of: "--json"), jsonIdx + 1 < args.count {
            inputData = args[jsonIdx + 1].data(using: .utf8)
        } else {
            // Read from stdin
            inputData = FileHandle.standardInput.readDataToEndOfFile()
        }

        guard let data = inputData, !data.isEmpty else {
            outputError("No input data provided")
            return
        }

        guard let payload = try? JSONDecoder().decode(RequestPayload.self, from: data) else {
            outputError("Invalid JSON input payload")
            return
        }

        // Fast path for language detection via Apple's NaturalLanguage framework
        if payload.detectLanguage == true {
            let recognizer = NLLanguageRecognizer()
            recognizer.processString(payload.text)
            if let lang = recognizer.dominantLanguage, lang != .undetermined {
                outputSuccess(lang.rawValue)
            } else {
                outputSuccess("und")
            }
            return
        }

        let model = SystemLanguageModel(guardrails: .permissiveContentTransformations)
        guard model.availability == .available else {
            outputError("Apple Intelligence is not available on this Mac")
            return
        }

        do {
            let instructions = Instructions(payload.system ?? "Führe die gewünschte Textverarbeitung präzise aus.")
            let session = LanguageModelSession(model: model, instructions: instructions)
            let temp = payload.temperature ?? 0.1
            let options = GenerationOptions(temperature: temp)
            let response = try await session.respond(to: Prompt(payload.text), options: options)
            let trimmed = response.content.trimmingCharacters(in: .whitespacesAndNewlines)
            outputSuccess(trimmed)
        } catch {
            outputError(error.localizedDescription)
        }
    }

    static func outputSuccess(_ text: String) {
        let res = ResponsePayload(success: true, text: text, error: nil)
        if let data = try? JSONEncoder().encode(res),
           let str = String(data: data, encoding: .utf8) {
            print(str)
        }
    }

    static func outputError(_ msg: String) {
        let res = ResponsePayload(success: false, text: nil, error: msg)
        if let data = try? JSONEncoder().encode(res),
           let str = String(data: data, encoding: .utf8) {
            print(str)
        }
        exit(1)
    }
}
