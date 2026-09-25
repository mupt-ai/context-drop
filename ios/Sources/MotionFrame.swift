import Foundation

struct MotionFrame {
    let rate: UInt8
    let sequence: UInt8
    let samples: [MotionSample]
    // This ring reports either 49 or 50 for its nominal 50 Hz motion stream.
    // Keep the reported rate in recordings; both use the same signed XYZ layout.
    var supportsClassifier: Bool { rate == 49 || rate == 50 }

    init?(_ bytes: [UInt8]) {
        guard bytes.count >= 10, bytes[0] == 0x33,
              Int(bytes[1]) + 2 == bytes.count, (bytes.count - 4) % 6 == 0 else { return nil }
        rate = bytes[2]
        sequence = bytes[3]
        samples = stride(from: 4, to: bytes.count, by: 6).map { offset in
            func axis(_ i: Int) -> Double {
                Double(Int16(bitPattern: UInt16(bytes[i]) | UInt16(bytes[i + 1]) << 8)) / 1000
            }
            return MotionSample(x: axis(offset), y: axis(offset + 2), z: axis(offset + 4))
        }
    }
}
