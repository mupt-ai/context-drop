import Foundation
import Security
import Combine

@MainActor
final class HealthStore: ObservableObject {
    @Published private(set) var snapshot: HealthSnapshot?
    @Published private(set) var records: [HealthRecord] = []
    @Published private(set) var isSyncing = false
    @Published private(set) var lastChecked: Date?
    @Published private(set) var syncError: String?
    @Published private(set) var storageError: String?
    @Published private(set) var configured = false
    private var dirty: Set<String> = []
    private let directory: URL
    private let session: URLSession
    private let base = URL(string: "https://health.avyayv.com")!
    private var token: String?
    private var cacheLoaded = true
    private struct Cache: Codable { var records: [HealthRecord]; var dirty: Set<String> }
    private struct Reply: Codable { var records: [HealthRecord]; var legacy: [HealthRecord]? }
    private struct Batch: Codable { var records: [HealthRecord] }

    init(directory: URL? = nil, session: URLSession = .shared) {
        self.session = session
        self.directory = directory ?? FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let file = self.directory.appendingPathComponent("health-records.json")
        if FileManager.default.fileExists(atPath: file.path) {
            do {
                let cache = try JSONDecoder().decode(Cache.self, from: Data(contentsOf: file))
                records = cache.records.map(WorkoutNaming.record); dirty = cache.dirty
                if records != cache.records { try persist() }
            } catch { storageError = "Saved logs could not be opened. They have been kept on this phone."; cacheLoaded = false }
        }
        if let data = try? Data(contentsOf: self.directory.appendingPathComponent("health-dashboard.json")) {
            snapshot = try? JSONDecoder().decode(HealthSnapshot.self, from: data)
        }
        importSetup()
    }
    var entries: [HealthRecord] { records.filter { !$0.deleted && $0.kind != "habit" && $0.kind != "target" }.sorted { ($0.day, $0.updatedAt) > ($1.day, $1.updatedAt) } }
    var habits: [HealthRecord] { records.filter { !$0.deleted && $0.kind == "habit" }.sorted { $0.updatedAt < $1.updatedAt } }
    var pendingCount: Int { dirty.count }
    func entries(_ kind: String, day: String? = nil) -> [HealthRecord] {
        entries.filter { $0.kind == kind && (day == nil || $0.day == day) }
    }
    @discardableResult func save(_ record: HealthRecord) -> Bool { saveBatch([record]) }
    @discardableResult func saveBatch(_ batch: [HealthRecord]) -> Bool {
        guard cacheLoaded else { return false }
        let old = records, oldDirty = dirty
        let now = Date().timeIntervalSince1970
        for original in batch {
            var item = WorkoutNaming.record(original)
            item.updatedAt = now
            records.removeAll { $0.id == item.id }; records.append(item); dirty.insert(item.id)
        }
        do { try persist(); storageError = nil; return true }
        catch { records = old; dirty = oldDirty; storageError = "Couldn’t save this change. Please try again."; return false }
    }
    func toggle(_ habit: HealthRecord, on day: String) {
        let next = count(for: habit, on: day) == 0 ? 1 : 0
        _ = setCount(habit, on: day, to: next)
    }
    func isDone(_ habit: HealthRecord, on day: String) -> Bool {
        count(for: habit, on: day) > 0
    }
    func delete(_ record: HealthRecord) {
        var batch = [record]
        batch[0].deleted = true
        if record.kind == "habit" {
            for child in records where !child.deleted && child.parentID == record.id {
                var item = child; item.deleted = true; batch.append(item)
            }
        }
        if saveBatch(batch) { Task { await refresh() } }
    }
    func count(for habit: HealthRecord, on day: String) -> Int {
        let value = records.first { !$0.deleted && $0.kind == "completion" && $0.parentID == habit.id && $0.day == day }?.value ?? 0
        return max(0, Int(value.rounded()))
    }
    @discardableResult func setCount(_ habit: HealthRecord, on day: String, to value: Int) -> Bool {
        let count = max(0, value)
        let id = "done-\(habit.id)-\(day)"
        var record = records.first { $0.id == id } ?? HealthRecord(id: id, kind: "completion", day: day, title: habit.title, parentID: habit.id)
        record.title = habit.title
        record.day = day
        record.parentID = habit.id
        record.value = Double(count)
        record.deleted = false
        let saved = save(record)
        if saved { Task { await refresh() } }
        return saved
    }
    func adjustCount(_ habit: HealthRecord, on day: String, by delta: Int) {
        _ = setCount(habit, on: day, to: count(for: habit, on: day) + delta)
    }
    func rename(_ habit: HealthRecord, to title: String) {
        let name = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name != habit.title else { return }
        var item = habit; item.title = name
        var batch = [item]
        for child in records where !child.deleted && child.parentID == habit.id {
            var copy = child; copy.title = name; batch.append(copy)
        }
        if saveBatch(batch) { Task { await refresh() } }
    }
    func syncTouches(_ sessions: [MotionSession]) {
        let days = Set(sessions.flatMap { $0.candidates.map { healthDay($0.time) } })
        for day in days {
            let value = Double(HabitSummary.confirmedTouches(in: sessions, on: healthDate(day)))
            let id = "touch-\(day)"
            if records.first(where: { $0.id == id })?.value != value {
                _ = save(HealthRecord(id: id, kind: "touch", day: day, title: "Hair & Face", value: value))
            }
        }
    }
    private func persist() throws {
        let data = try JSONEncoder().encode(Cache(records: records, dirty: dirty))
        try data.write(to: directory.appendingPathComponent("health-records.json"), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
    func importSetup() {
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "com.avyay.health", kSecAttrAccount as String: "sync"]
        let file = directory.appendingPathComponent("health-setup.json")
        if let data = try? Data(contentsOf: file), let object = try? JSONSerialization.jsonObject(with: data) as? [String: String], let secret = object["token"], secret.count >= 32 {
            let value = Data(secret.utf8)
            let status = SecItemUpdate(query as CFDictionary, [kSecValueData as String: value] as CFDictionary)
            var add = query; add[kSecValueData as String] = value
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            if status == errSecSuccess || (status == errSecItemNotFound && SecItemAdd(add as CFDictionary, nil) == errSecSuccess) {
                try? FileManager.default.removeItem(at: file)
            }
        }
        var read = query; read[kSecReturnData as String] = true
        var result: CFTypeRef?
        if SecItemCopyMatching(read as CFDictionary, &result) == errSecSuccess, let data = result as? Data {
            token = String(data: data, encoding: .utf8)
        }
        configured = token != nil
    }
    private func fetch(_ path: String, body: Data? = nil, authenticated: Bool = false) async throws -> Data {
        var request = URLRequest(url: base.appendingPathComponent(path), cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 25)
        if path == "dashboard-data.json" { request.setValue("no-cache", forHTTPHeaderField: "Cache-Control") }
        if authenticated { request.setValue("Bearer \(token ?? "")", forHTTPHeaderField: "Authorization") }
        if let body { request.httpMethod = "POST"; request.httpBody = body; request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else { throw URLError(.badServerResponse) }
        return data
    }
    func refresh() async {
        guard !isSyncing else { return }
        isSyncing = true; defer { isSyncing = false }
        importSetup()
        var errors: [String] = []
        do {
            let data = try await fetch("dashboard-data.json")
            var next = try JSONDecoder().decode(HealthSnapshot.self, from: data)
            if let saved = snapshot, next.predates(saved) {
                errors.append("The wearable feed returned an older snapshot. Showing newer saved data.")
            } else {
                if let savedNight = snapshot?.currentNight,
                   (next.currentNight?.day ?? "") < savedNight.day {
                    next.latestNight = savedNight
                    errors.append("The wearable feed is missing the latest sleep record. Showing the newer saved Oura night.")
                }
                try JSONEncoder().encode(next).write(to: directory.appendingPathComponent("health-dashboard.json"), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
                snapshot = next
            }
            lastChecked = Date()
        } catch { errors.append("Health refresh failed. Showing saved data.") }
        if token != nil && cacheLoaded {
            do {
                let sent = records.filter { dirty.contains($0.id) }
                if !sent.isEmpty {
                    let data = try await fetch("api/health", body: JSONEncoder().encode(Batch(records: sent)), authenticated: true)
                    _ = try JSONDecoder().decode(Reply.self, from: data)
                }
                let data = try await fetch("api/health", authenticated: true)
                let reply = try JSONDecoder().decode(Reply.self, from: data)
                let old = records, oldDirty = dirty
                // Do not clear edits made while the request was in flight.
                for r in sent where records.first(where: { $0.id == r.id }) == r { dirty.remove(r.id) }
                var merged = Dictionary(uniqueKeysWithValues: records.map { ($0.id, $0) })
                for r in ((reply.legacy ?? []) + reply.records).map(WorkoutNaming.record) where !dirty.contains(r.id) {
                    if let local = merged[r.id], local.updatedAt > r.updatedAt { continue }
                    merged[r.id] = r
                }
                records = Array(merged.values)
                do { try persist() } catch { records = old; dirty = oldDirty; throw error }
            } catch { errors.append("Logs haven’t synced. Changes are saved on this phone.") }
        }
        syncError = errors.isEmpty ? nil : errors.joined(separator: " ")
    }
}
