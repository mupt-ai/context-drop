import Foundation
import Combine

enum TouchLabel: String, Codable, CaseIterable {
    case unreviewed, touch, notTouch
    var title: String {
        switch self { case .unreviewed: return "Unreviewed"; case .touch: return "Touch"; case .notTouch: return "Not a Touch" }
    }
}

struct TouchCandidate: Codable, Identifiable {
    let id: UUID
    let time: Date
    let from: Date
    let through: Date
    let probability: Double?
    let source: String
    var label: TouchLabel
}

struct MotionSession: Codable, Identifiable {
    let id: UUID
    let startedAt: Date
    var endedAt: Date?
    var state: String
    var samples: Int
    var lastFrameAt: Date?
    var candidates: [TouchCandidate]
    let model: TouchModel
    let threshold: Double
    var reviewed: Int { candidates.filter { $0.label != .unreviewed }.count }
}

/// Append-only raw motion, with separately editable human labels. Never stores a ring key.
final class RecordingStore: ObservableObject {
    static let shared = RecordingStore()
    @Published private(set) var sessions: [MotionSession] = []
    @Published private(set) var activeID: UUID?
    @Published private(set) var errorMessage: String?
    private let directory: URL
    private var output: FileHandle?
    private var buffer = Data()
    private var flushedAt = Date.distantPast
    var isRecording: Bool { activeID != nil }
    var active: MotionSession? { sessions.first { $0.id == activeID } }

    init(directory: URL = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0].appendingPathComponent("Recordings")) {
        self.directory = directory
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            for folder in try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) {
                let metadata = folder.appendingPathComponent("session.json")
                guard FileManager.default.fileExists(atPath: metadata.path) else { continue }
                var session = try JSONDecoder().decode(MotionSession.self, from: Data(contentsOf: metadata))
                if session.state == "recording" {
                    session.state = "interrupted"
                    session.endedAt = session.lastFrameAt ?? session.startedAt
                    try JSONEncoder().encode(session).write(to: metadata, options: .atomic)
                }
                sessions.append(session)
            }
            sessions.sort { $0.startedAt > $1.startedAt }
        } catch { errorMessage = "Could not load recordings: \(error.localizedDescription)" }
    }

    func start(model: TouchModel, threshold: Double) throws {
        guard !isRecording else { return }
        let session = MotionSession(id: UUID(), startedAt: Date(), state: "recording", samples: 0,
                                    candidates: [], model: model, threshold: threshold)
        let folder = directory.appendingPathComponent(session.id.uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let url = folder.appendingPathComponent("motion.jsonl")
        guard FileManager.default.createFile(atPath: url.path, contents: Data(), attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication]) else {
            throw CocoaError(.fileWriteUnknown)
        }
        output = try FileHandle(forWritingTo: url)
        sessions.insert(session, at: 0)
        activeID = session.id
        errorMessage = nil
        flushedAt = Date()
        do { try save(session.id) } catch { finishAfterError(error); throw error }
    }

    func append(frame: MotionFrame, at time: Date, uptime: TimeInterval) {
        guard let id = activeID, let index = sessions.firstIndex(where: { $0.id == id }) else { return }
        do {
            let packet: [String: Any] = ["time": time.timeIntervalSince1970, "uptime": uptime,
                "rate": Int(frame.rate), "sequence": Int(frame.sequence),
                "samples": frame.samples.map { [$0.x, $0.y, $0.z] }]
            buffer.append(try JSONSerialization.data(withJSONObject: packet))
            buffer.append(0x0a)
            sessions[index].samples += frame.samples.count
            sessions[index].lastFrameAt = time
            if time.timeIntervalSince(flushedAt) >= 1 { try flush(); try save(id) }
        } catch { finishAfterError(error) }
    }

    @discardableResult
    func candidate(probability: Double?, manual: Bool = false, at time: Date = Date()) -> TouchCandidate? {
        guard let id = activeID, let index = sessions.firstIndex(where: { $0.id == id }),
              let last = sessions[index].lastFrameAt, time.timeIntervalSince(last) < 1 else { return nil }
        let item = TouchCandidate(id: UUID(), time: time,
            from: max(sessions[index].startedAt, time.addingTimeInterval(-3)),
            through: time.addingTimeInterval(manual ? 0 : 2), probability: probability,
            source: manual ? "manual" : "classifier", label: manual ? .touch : .unreviewed)
        sessions[index].candidates.insert(item, at: 0)
        do { try flush(); try save(id); return item }
        catch { finishAfterError(error); return nil }
    }

    func label(sessionID: UUID, candidateID: UUID, as label: TouchLabel) {
        guard let s = sessions.firstIndex(where: { $0.id == sessionID }),
              let c = sessions[s].candidates.firstIndex(where: { $0.id == candidateID }) else { return }
        let previous = sessions[s].candidates[c].label
        sessions[s].candidates[c].label = label
        do { try save(sessionID) }
        catch {
            sessions[s].candidates[c].label = previous
            errorMessage = "Label was not saved: \(error.localizedDescription)"
        }
    }

    func end() {
        guard let id = activeID, let index = sessions.firstIndex(where: { $0.id == id }) else { return }
        sessions[index].endedAt = Date()
        sessions[index].state = "saved"
        do { try flush(); try save(id); try output?.close() }
        catch { finishAfterError(error) }
        output = nil
        activeID = nil
    }

    private func save(_ id: UUID) throws {
        guard let session = sessions.first(where: { $0.id == id }) else { return }
        let url = directory.appendingPathComponent(id.uuidString).appendingPathComponent("session.json")
        try JSONEncoder().encode(session).write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    private func flush() throws {
        guard let output else { return }
        if !buffer.isEmpty { try output.write(contentsOf: buffer); buffer.removeAll(keepingCapacity: true) }
        try output.synchronize()
        flushedAt = Date()
    }

    private func finishAfterError(_ error: Error) {
        errorMessage = "Recording stopped: \(error.localizedDescription)"
        try? output?.close()
        output = nil
        activeID = nil
        buffer.removeAll()
    }
}
