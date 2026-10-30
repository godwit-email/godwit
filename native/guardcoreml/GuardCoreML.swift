// CoreML behind a C interface, for AstrLink Guard's encoder (core/ai/mask/guard
// loads it with purego, so Go stays without cgo). Built by
// scripts/build-guard-coreml.sh into resources/darwin-<arch>/.
//
// It runs a compiled program with inputs x [1, s, h] and mask [1, 1, 1, s]
// and output logits [1, s, k]; the program and its weights are guard's.
// Every string it returns is malloc'd, freed with godwit_ml_free; a NULL
// one means it went well.

import CoreML
import Foundation

private func fail(_ e: Error) -> UnsafeMutablePointer<CChar> { strdup(e.localizedDescription)! }

@_cdecl("godwit_ml_free")
public func free_(_ p: UnsafeMutableRawPointer?) { free(p) }

/// Compiles the package at pkg into out (a .mlmodelc).
@_cdecl("godwit_ml_compile")
public func compile(_ pkg: UnsafePointer<CChar>, _ out: UnsafePointer<CChar>) -> UnsafeMutablePointer<CChar>? {
    let o = URL(fileURLWithPath: String(cString: out))
    do {
        let c = try MLModel.compileModel(at: URL(fileURLWithPath: String(cString: pkg)))
        let fm = FileManager.default
        try? fm.removeItem(at: o)
        do {
            try fm.moveItem(at: c, to: o)
        } catch {
            try? fm.removeItem(at: c)
            throw error
        }
        return nil
    } catch {
        return fail(error)
    }
}

/// Opens the compiled program at path for every compute unit: a retained
/// handle, or NULL and *err.
@_cdecl("godwit_ml_open")
public func open(_ path: UnsafePointer<CChar>, _ err: UnsafeMutablePointer<UnsafeMutablePointer<CChar>?>) -> UnsafeMutableRawPointer? {
    let cfg = MLModelConfiguration()
    cfg.computeUnits = .all
    do {
        let m = try MLModel(contentsOf: URL(fileURLWithPath: String(cString: path)), configuration: cfg)
        return Unmanaged.passRetained(m).toOpaque()
    } catch {
        err.pointee = fail(error)
        return nil
    }
}

@_cdecl("godwit_ml_close")
public func close(_ h: UnsafeMutableRawPointer) { Unmanaged<MLModel>.fromOpaque(h).release() }

/// The device CoreML's compute plan puts most of the program at path on, by
/// the operations' estimated cost: "Neural Engine", "GPU" or "CPU", or an
/// error (before macOS 14.4 there is no plan).
@_cdecl("godwit_ml_device")
public func device(_ path: UnsafePointer<CChar>) -> UnsafeMutablePointer<CChar> {
    guard #available(macOS 14.4, *) else { return strdup("error: the compute plan needs macOS 14.4")! }
    let url = URL(fileURLWithPath: String(cString: path))
    var answer = "error: no compute plan"
    let done = DispatchSemaphore(value: 0)
    Task.detached {
        defer { done.signal() }
        let cfg = MLModelConfiguration()
        cfg.computeUnits = .all
        guard let plan = try? await MLComputePlan.load(contentsOf: url, configuration: cfg),
              case let .program(prog) = plan.modelStructure else { return }
        var cost = [String: Double]()
        func walk(_ b: MLModelStructure.Program.Block) {
            for op in b.operations {
                if let d = plan.deviceUsage(for: op)?.preferred {
                    let name: String
                    switch d {
                    case .neuralEngine: name = "Neural Engine"
                    case .gpu: name = "GPU"
                    default: name = "CPU"
                    }
                    cost[name, default: 0] += plan.estimatedCost(of: op)?.weight ?? 0
                }
                op.blocks.forEach(walk)
            }
        }
        prog.functions.values.forEach { walk($0.block) }
        if let best = cost.max(by: { $0.value < $1.value }) { answer = best.key }
    }
    done.wait()
    return strdup(answer)!
}

private func array(_ src: UnsafePointer<Float>, _ shape: [Int], _ n: Int) throws -> MLMultiArray {
    let b = UnsafeMutablePointer<Float>.allocate(capacity: n)
    b.initialize(from: src, count: n)
    var strides = [Int](repeating: 1, count: shape.count)
    for i in stride(from: shape.count - 2, through: 0, by: -1) { strides[i] = strides[i + 1] * shape[i + 1] }
    return try MLMultiArray(dataPointer: b, shape: shape.map { NSNumber(value: $0) }, dataType: .float32,
                            strides: strides.map { NSNumber(value: $0) }, deallocator: { $0.deallocate() })
}

/// half's float, by its bits: Swift's Float16 isn't on Intel Macs.
private func float(_ h: UInt16) -> Float {
    let sign = UInt32(h & 0x8000) << 16, exp = Int((h >> 10) & 0x1f), man = UInt32(h & 0x3ff)
    if exp == 0 { return (sign != 0 ? -1 : 1) * Float(man) * 0x1p-24 }
    if exp == 31 { return Float(bitPattern: sign | 0x7f80_0000 | man << 13) }
    return Float(bitPattern: sign | UInt32(exp + 112) << 23 | man << 13)
}

/// Reads x [1, s, h] and mask [1, 1, 1, s] into out [s, k]. One call at a
/// time per handle.
@_cdecl("godwit_ml_run")
public func run(_ hp: UnsafeMutableRawPointer, _ x: UnsafePointer<Float>, _ mask: UnsafePointer<Float>,
                _ s: Int, _ h: Int, _ out: UnsafeMutablePointer<Float>, _ k: Int) -> UnsafeMutablePointer<CChar>? {
    let m = Unmanaged<MLModel>.fromOpaque(hp).takeUnretainedValue()
    return autoreleasepool {
        do {
            let input = try MLDictionaryFeatureProvider(dictionary: [
                "x": MLFeatureValue(multiArray: try array(x, [1, s, h], s * h)),
                "mask": MLFeatureValue(multiArray: try array(mask, [1, 1, 1, s], s)),
            ])
            guard let lg = try m.prediction(from: input).featureValue(for: "logits")?.multiArrayValue,
                  lg.shape.count == 3, lg.shape[1].intValue == s, lg.shape[2].intValue == k
            else { return strdup("logits have another shape")! }
            let s1 = lg.strides[1].intValue, s2 = lg.strides[2].intValue
            switch lg.dataType {
            case .float32:
                let p = lg.dataPointer.assumingMemoryBound(to: Float.self)
                for t in 0..<s { for c in 0..<k { out[t * k + c] = p[t * s1 + c * s2] } }
            case .float16:
                let p = lg.dataPointer.assumingMemoryBound(to: UInt16.self)
                for t in 0..<s { for c in 0..<k { out[t * k + c] = float(p[t * s1 + c * s2]) } }
            default:
                return strdup("logits are of another type")!
            }
            return nil
        } catch {
            return fail(error)
        }
    }
}
