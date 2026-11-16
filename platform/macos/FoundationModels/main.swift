import CoreFoundation
import Darwin
import Foundation
import FoundationModels

// One request per process. Prompts travel only on stdin and responses on stdout.
// Never print native errors: their descriptions can include request content.
private enum Limits {
    static let requestBytes = 256 * 1024
    static let inputBytes = 32 * 1024
    static let schemaBytes = 16 * 1024
    static let responseBytes = 12 * 1024
    static let outputTokens = 1536
    static let contextTokens = 8192
    static let templateReserve = 512
}

private struct Request {
    let operation: String
    let system: String
    let user: String
    let format: String
    let schema: Data?
    let maxOutputTokens: Int
    let sourceTransformation: Bool

    static func decode(_ data: Data) throws -> Request {
        guard data.count <= Limits.requestBytes,
              let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              Set(object.keys).isSubset(of: ["version", "operation", "system", "user", "format", "schema", "max_output_tokens", "source_transformation"]),
              object["source_transformation"] == nil || boolean(object["source_transformation"]) != nil,
              integer(object["version"]) == 1,
              let operation = object["operation"] as? String,
              ["status", "count", "generate"].contains(operation),
              let system = object["system"] as? String,
              let user = object["user"] as? String,
              system.utf8.count + user.utf8.count <= Limits.inputBytes,
              let format = object["format"] as? String,
              ["", "json"].contains(format),
              let output = integer(object["max_output_tokens"]),
              (operation == "generate" ? 1...Limits.outputTokens : 0...Limits.outputTokens).contains(output)
        else { throw BridgeError.invalidRequest }

        var schema: Data?
        if let value = object["schema"] {
            guard let schemaObject = value as? [String: Any], format == "json" else {
                throw BridgeError.invalidRequest
            }
            let encoded = try JSONSerialization.data(withJSONObject: schemaObject, options: [.sortedKeys, .withoutEscapingSlashes])
            guard encoded.count <= Limits.schemaBytes else { throw BridgeError.invalidRequest }
            schema = encoded
        }
        return Request(operation: operation, system: system, user: user, format: format, schema: schema, maxOutputTokens: output, sourceTransformation: boolean(object["source_transformation"]) ?? false)
    }

    private static func boolean(_ value: Any?) -> Bool? {
        guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
        return number.boolValue
    }

    fileprivate static func integer(_ value: Any?) -> Int? {
        guard let number = value as? NSNumber,
              CFGetTypeID(number) != CFBooleanGetTypeID(),
              number.doubleValue.isFinite,
              number.doubleValue.rounded() == number.doubleValue,
              number.doubleValue >= 0,
              number.doubleValue <= Double(Int32.max) else { return nil }
        return number.intValue
    }
}

private enum BridgeError: Error {
    case invalidRequest
}

private struct Response: Encodable {
    let version = 1
    var available = false
    var reason = ""
    var context_tokens: Int?
    var input_tokens: Int?
    var identity: String?
    var text: String?
    var error: String?
}

@main
private struct Bridge {
    static func main() async {
        let response: Response
        do {
            guard CommandLine.arguments.count == 1 else { throw BridgeError.invalidRequest }
            let request = try Request.decode(readInput())
            if #available(macOS 26.0, *) {
                response = await handle(request)
            } else {
                response = Response(reason: "unsupported_os", error: "unsupported_os")
            }
        } catch {
            response = Response(error: "invalid_request")
        }
        // Encoding this bounded value cannot expose a prompt or native error.
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        if var data = try? encoder.encode(response) {
            data.append(0x0a)
            try? FileHandle.standardOutput.write(contentsOf: data)
        }
    }

    private static func readInput() throws -> Data {
        var data = Data()
        while data.count <= Limits.requestBytes {
            let remaining = Limits.requestBytes + 1 - data.count
            guard let chunk = try FileHandle.standardInput.read(upToCount: min(16 * 1024, remaining)), !chunk.isEmpty else {
                return data
            }
            data.append(chunk)
        }
        throw BridgeError.invalidRequest
    }

    @available(macOS 26.0, *)
    private static func handle(_ request: Request) async -> Response {
        // This concrete model is Apple's on-device system model. There are no
        // tools, custom executors, remote providers, or cloud compute models.
        // Apple's supported transformation mode accepts existing source text
        // while retaining output guardrails. It applies only to string output;
        // general questions continue to use the default guided-generation path.
        let model = SystemLanguageModel(useCase: .general, guardrails: request.sourceTransformation ? .permissiveContentTransformations : .default)
        let context = contextSize(model)
        var response = Response(context_tokens: context, identity: identity(context: context, sourceTransformation: request.sourceTransformation))
        switch model.availability {
        case .available:
            response.available = true
            response.reason = "available"
        case .unavailable(let reason):
            switch reason {
            case .deviceNotEligible: response.reason = "device_not_eligible"
            case .appleIntelligenceNotEnabled: response.reason = "apple_intelligence_not_enabled"
            case .modelNotReady: response.reason = "model_not_ready"
            @unknown default: response.reason = "unavailable"
            }
            response.error = "unavailable"
            return response
        }
        guard request.operation != "status" else { return response }

        do {
            let plan = try GenerationPlan(request)
            let input = try await tokenCount(model, plan: plan)
            response.input_tokens = input
            guard request.operation == "generate" else { return response }
            guard input + Limits.templateReserve + request.maxOutputTokens <= context else {
                response.error = "context_budget"
                return response
            }

            let session = LanguageModelSession(model: model, tools: [], instructions: Instructions(plan.instructions))
            let options = GenerationOptions(samplingMode: .greedy, maximumResponseTokens: request.maxOutputTokens)
            let text: String
            if let schema = plan.schema {
                let result = try await session.respond(to: Prompt(request.user), schema: schema, options: options)
                text = result.content.jsonString
            } else {
                let result = try await session.respond(to: Prompt(request.user), options: options)
                text = result.content
            }
            guard !text.isEmpty, text.utf8.count <= Limits.responseBytes else {
                response.error = "generation_failed"
                return response
            }
            if request.format == "json" {
                guard let bytes = text.data(using: .utf8),
                      (try? JSONSerialization.jsonObject(with: bytes)) is [String: Any] else {
                    response.error = "invalid_json"
                    return response
                }
            }
            response.text = text
            return response
        } catch {
            response.error = safeError(error)
            if response.error == "unavailable" {
                response.available = false
                response.reason = "model_not_ready"
            }
            return response
        }
    }

    @available(macOS 26.0, *)
    private static func contextSize(_ model: SystemLanguageModel) -> Int {
        if #available(macOS 26.4, *) {
            let runtime = model.contextSize
            if runtime > Limits.templateReserve { return min(runtime, Limits.contextTokens) }
        }
        return 4096
    }

    @available(macOS 26.0, *)
    private static func tokenCount(_ model: SystemLanguageModel, plan: GenerationPlan) async throws -> Int {
        if #available(macOS 26.4, *) {
            let instructions = try await model.tokenCount(for: Instructions(plan.instructions))
            let prompt = try await model.tokenCount(for: Prompt(plan.user))
            let schema: Int
            if let generationSchema = plan.schema {
                schema = try await model.tokenCount(for: generationSchema)
            } else {
                schema = 0
            }
            guard instructions >= 0, prompt >= 0, schema >= 0,
                  instructions <= 1_000_000, prompt <= 1_000_000, schema <= 1_000_000 else {
                throw BridgeError.invalidRequest
            }
            return instructions + prompt + schema
        }
        // Before 26.4, UTF-8 byte count is a conservative tokenizer bound.
        // The encoded schema is included even when generation uses guidance.
        return plan.instructions.utf8.count + plan.user.utf8.count + plan.schemaBytes
    }

    @available(macOS 26.0, *)
    private static func safeError(_ error: Error) -> String {
        if #available(macOS 27.0, *) {
            if let native = error as? LanguageModelError {
                switch native {
                case .contextSizeExceeded: return "context_budget"
                case .guardrailViolation, .refusal: return "refused"
                default: return "generation_failed"
                }
            }
            if error is SystemLanguageModel.Error { return "unavailable" }
        }
        if let native = error as? LanguageModelSession.GenerationError {
            switch native {
            case .exceededContextWindowSize: return "context_budget"
            case .guardrailViolation, .refusal: return "refused"
            case .assetsUnavailable: return "unavailable"
            default: return "generation_failed"
            }
        }
        return "generation_failed"
    }

    private static func identity(context: Int, sourceTransformation: Bool) -> String {
        let version = ProcessInfo.processInfo.operatingSystemVersion
        var size = 0
        var build = "unknown"
        if sysctlbyname("kern.osversion", nil, &size, nil, 0) == 0, size > 0, size <= 128 {
            var buffer = [UInt8](repeating: 0, count: size)
            if sysctlbyname("kern.osversion", &buffer, &size, nil, 0) == 0 {
                let candidate = String(decoding: buffer.prefix(while: { $0 != 0 }), as: UTF8.self)
                if candidate.utf8.allSatisfy({ (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) }) {
                    build = candidate
                }
            }
        }
        // Apple exposes no immutable model-weights digest. This identifies the
        // system provider and OS build, not the exact installed model assets.
        let mode = sourceTransformation ? ":source-transform-v1" : ""
        return "apple-system-default:macos-\(version.majorVersion).\(version.minorVersion).\(version.patchVersion):build-\(build):context-\(context)\(mode)"
    }
}

@available(macOS 26.0, *)
private struct GenerationPlan {
    let instructions: String
    let user: String
    let schema: GenerationSchema?
    let schemaBytes: Int

    init(_ request: Request) throws {
        user = request.user
        var instructions = request.system
        var schema: GenerationSchema?
        if request.format == "json" {
            instructions += "\nReturn only one valid JSON object, without Markdown fences or commentary."
            if let data = request.schema,
               let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] {
                if !request.sourceTransformation {
                    schema = try? SchemaCompiler.compile(object)
                }
                if schema == nil {
                    instructions += "\nMatch this JSON schema exactly:\n" + String(decoding: data, as: UTF8.self)
                }
            }
        }
        self.instructions = instructions
        self.schema = schema
        if let schema {
            schemaBytes = try JSONEncoder().encode(schema).count
        } else {
            schemaBytes = 0
        }
    }
}

// Compile only constraints we can preserve. More complex JSON Schema (notably
// oneOf intent branches) uses the complete schema in the prompt and the caller's
// strict decoder. No constraint is silently discarded by guided generation.
@available(macOS 26.0, *)
private struct SchemaCompiler {
    private var nodes = 0

    static func compile(_ object: [String: Any]) throws -> GenerationSchema {
        var compiler = SchemaCompiler()
        let root = try compiler.node(object, name: "Response", depth: 0)
        return try GenerationSchema(root: root, dependencies: [])
    }

    private mutating func node(_ object: [String: Any], name: String, depth: Int) throws -> DynamicGenerationSchema {
        nodes += 1
        guard depth <= 12, nodes <= 256, let type = object["type"] as? String else {
            throw BridgeError.invalidRequest
        }
        guard object["description"] == nil || object["description"] is String else {
            throw BridgeError.invalidRequest
        }
        let description = object["description"] as? String
        switch type {
        case "object":
            try keys(object, allowed: ["type", "description", "properties", "required", "additionalProperties"])
            guard let properties = object["properties"] as? [String: [String: Any]],
                  let required = object["required"] as? [String],
                  Set(required).isSubset(of: Set(properties.keys)),
                  let additional = object["additionalProperties"] as? NSNumber,
                  CFGetTypeID(additional) == CFBooleanGetTypeID(), !additional.boolValue else { throw BridgeError.invalidRequest }
            var fields: [DynamicGenerationSchema.Property] = []
            for key in properties.keys.sorted() {
                let child = try node(properties[key]!, name: name + "_" + key, depth: depth + 1)
                fields.append(.init(name: key, description: properties[key]?["description"] as? String, schema: child, isOptional: !required.contains(key)))
            }
            return DynamicGenerationSchema(name: name, description: description, properties: fields)
        case "array":
            try keys(object, allowed: ["type", "description", "items", "minItems", "maxItems"])
            guard let items = object["items"] as? [String: Any] else { throw BridgeError.invalidRequest }
            let minimum = Request.integer(object["minItems"])
            let maximum = Request.integer(object["maxItems"])
            guard (object["minItems"] == nil || minimum != nil),
                  (object["maxItems"] == nil || maximum != nil),
                  minimum.map({ (0...64).contains($0) }) ?? true,
                  maximum.map({ (0...64).contains($0) }) ?? true,
                  minimum ?? 0 <= maximum ?? 64 else { throw BridgeError.invalidRequest }
            return DynamicGenerationSchema(arrayOf: try node(items, name: name + "_Item", depth: depth + 1), minimumElements: minimum, maximumElements: maximum)
        case "string":
            try keys(object, allowed: ["type", "description", "enum"])
            if let choices = object["enum"] {
                guard let values = choices as? [String], !values.isEmpty, values.count <= 64 else { throw BridgeError.invalidRequest }
                return DynamicGenerationSchema(name: name, description: description, anyOf: values)
            }
            return DynamicGenerationSchema(type: String.self)
        case "integer":
            try keys(object, allowed: ["type", "description"])
            return DynamicGenerationSchema(type: Int.self)
        case "number":
            try keys(object, allowed: ["type", "description"])
            return DynamicGenerationSchema(type: Double.self)
        case "boolean":
            try keys(object, allowed: ["type", "description"])
            return DynamicGenerationSchema(type: Bool.self)
        default:
            throw BridgeError.invalidRequest
        }
    }

    private func keys(_ object: [String: Any], allowed: Set<String>) throws {
        guard Set(object.keys).isSubset(of: allowed) else { throw BridgeError.invalidRequest }
    }
}
