import Foundation

func check(_ condition: Bool, _ message: String) {
    if !condition { fatalError(message) }
}
struct Fixture: Decodable {
    let model: TouchModel
    let samples: [[Double]]
    let probability: Double
}
let fixture = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[1])))
let samples = fixture.samples.map { MotionSample(x: $0[0] / 1000, y: $0[1] / 1000, z: $0[2] / 1000) }
check(abs(fixture.model.probability(samples) - fixture.probability) < 1e-10, "Python / Swift model mismatch")
var gate = EpisodeGate()
check(!gate.observe(probability: 0.9, threshold: 0.7, now: 0), "Single window should not alert")
check(gate.observe(probability: 0.9, threshold: 0.7, now: 0.5), "Two high windows should alert")
check(!gate.observe(probability: 0.9, threshold: 0.7, now: 20), "Sustained touch must not repeat")
for time in [21.0, 21.5, 22.0] { check(!gate.observe(probability: 0.1, threshold: 0.7, now: time), "Rest must not alert") }
check(!gate.observe(probability: 0.9, threshold: 0.7, now: 23), "Rearm requires two windows")
check(gate.observe(probability: 0.9, threshold: 0.7, now: 23.5), "New episode should alert")
for time in [24.0, 24.5, 25.0] { _ = gate.observe(probability: 0.1, threshold: 0.7, now: time) }
for time in [26.0, 26.5, 27.0] { check(!gate.observe(probability: 0.9, threshold: 0.7, now: time), "Cooldown must suppress alerts") }
// AES-128 ECB public known-answer vector for zero key and nonce, with protocol padding byte 01.
let setup = RingSetup(keyHex: String(repeating: "0", count: 32), peripheralID: nil, model: fixture.model)
let encrypted = try setup.authenticate(nonce: Data(repeating: 0, count: 15))
check(encrypted.map { String(format: "%02x", $0) }.joined() == "58e2fccefa7e3061367f1d57a4e7455a", "AES authentication mismatch")
let captured: [UInt8] = [0x33,0x0e,0x31,0x55,0xb2,0xfc,0xbe,0xfd,0x3f,0xfe,0xc1,0xfc,0xaf,0xfd,0x3a,0xfe]
let frame = MotionFrame(captured)!
check(frame.supportsClassifier, "49 Hz stream rejected")
check(frame.rate == 49 && frame.sequence == 85 && frame.samples.count == 2, "Frame metadata mismatch")
check(frame.samples[0].x == -0.846 && frame.samples[0].y == -0.578 && frame.samples[0].z == -0.449, "Signed axis decode mismatch")
check(frame.samples[1].x == -0.831 && frame.samples[1].z == -0.454, "Second sample mismatch")
check(MotionFrame(Array(captured.dropLast())) == nil, "Truncated frame accepted")
check(MotionFrame([0x33,0x03,0x31,0x55,0]) == nil, "Malformed frame accepted")
var rate50 = captured
rate50[2] = 50
let frame50 = MotionFrame(rate50)!
check(frame50.supportsClassifier && frame50.rate == 50, "50 Hz stream rejected")
check(frame50.samples[0].x == frame.samples[0].x && frame50.samples[1].z == frame.samples[1].z, "Rate changed axis decoding")
rate50[2] = 100

[3 more lines in file. Use offset=41 to continue.]