import Foundation

struct MotionSample {
    let x: Double
    let y: Double
    let z: Double
    var norm: Double { sqrt(x * x + y * y + z * z) }
}

struct TouchModel: Codable {
    let mean: [Double]
    let scale: [Double]
    let weights: [Double]
    let intercept: Double

    var isValid: Bool {
        mean.count == 8 && scale.count == 8 && weights.count == 8
            && (mean + scale + weights + [intercept]).allSatisfy(\.isFinite)
            && scale.allSatisfy { $0 > 0 }
    }

    static func features(_ samples: [MotionSample]) -> [Double] {
        func average(_ xs: [Double]) -> Double { xs.reduce(0, +) / Double(xs.count) }
        func deviation(_ xs: [Double]) -> Double {
            let m = average(xs)
            return sqrt(average(xs.map { ($0 - m) * ($0 - m) }))
        }
        let x = samples.map(\.x), y = samples.map(\.y), z = samples.map(\.z)
        let changes = zip(samples.dropFirst(), samples).map { a, b in
            MotionSample(x: a.x - b.x, y: a.y - b.y, z: a.z - b.z).norm
        }
        return [average(x), average(y), average(z), deviation(x), deviation(y),
                deviation(z), deviation(samples.map(\.norm)), average(changes)]
    }

    func probability(_ samples: [MotionSample]) -> Double {
        guard samples.count >= 2, isValid else { return 0 }
        let features = Self.features(samples)
        let score = zip(features.indices, features).reduce(intercept) { sum, item in
            let (i, value) = item
            return sum + weights[i] * (value - mean[i]) / scale[i]
        }
        return 1 / (1 + exp(-max(-50, min(50, score))))
    }
}

enum TouchDetectionPolicy {
    // Favor surfacing candidates for review while collecting labels. Legacy setups
    // used 0.8-0.9, which suppressed many movements before the user could label them.
    static func threshold(configured: Double?) -> Double {
        min(configured ?? 0.55, 0.55)
    }
}

struct EpisodeGate {
    private var highWindows = 0
    private var lowWindows = 0
    private var latched = false
    private var lastAlert = -Double.infinity

    mutating func observe(probability: Double, threshold: Double, now: Double) -> Bool {
        if probability >= threshold {
            highWindows += 1
            lowWindows = 0
            if highWindows >= 2 && !latched && now - lastAlert >= 10 {
                latched = true
                lastAlert = now
                return true
            }
        } else {
            highWindows = 0
            lowWindows += 1
            if lowWindows >= 3 { latched = false }
        }
        return false
    }
}
