import Foundation

@main struct HealthTests {
    @MainActor static func main() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = HealthStore(directory: dir)
        let meal = HealthRecord(kind: "food", day: "2026-09-13", title: "Test Meal", value: 500, protein: 35)
        precondition(store.save(meal)); precondition(store.pendingCount == 1)
        let reopened = HealthStore(directory: dir)
        precondition(reopened.entries("food").first?.value == 500)
        precondition(reopened.pendingCount == 1)
        let habit = HealthRecord(kind: "habit", day: "2026-09-13", title: "Test Habit")
        precondition(reopened.save(habit))
        precondition(reopened.count(for: habit, on: "2026-09-13") == 0)
        precondition(reopened.setCount(habit, on: "2026-09-13", to: 2))
        reopened.adjustCount(habit, on: "2026-09-13", by: -1)
        precondition(reopened.count(for: habit, on: "2026-09-13") == 1)
        reopened.rename(habit, to: "Renamed Counter")
        precondition(reopened.habits.first(where: { $0.id == habit.id })?.title == "Renamed Counter")
        precondition(reopened.records.first(where: { $0.parentID == habit.id })?.title == "Renamed Counter")
        // Persistence failure must preserve the last saved collection.
        let before = reopened.records
        try FileManager.default.removeItem(at: dir)
        precondition(!reopened.save(HealthRecord(kind: "food", day: "2026-09-13", title: "Fail")))
        precondition(reopened.records == before)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let legacy = #"{"records":[{"id":"legacy-meal","kind":"food","day":"2026-09-12","title":"Legacy Meal","updatedAt":1,"eatenAt":"2026-09-12T19:30:00Z","deleted":false}],"dirty":[]}"#
        let corrupt = dir.appendingPathComponent("health-records.json")
        try Data(legacy.utf8).write(to: corrupt)
        let migrated = HealthStore(directory: dir)
        precondition(migrated.storageError == nil)
        precondition(migrated.entries("food").first?.eatenAt == "2026-09-12T19:30:00Z")
        try Data("corrupt".utf8).write(to: corrupt)
        let blocked = HealthStore(directory: dir)
        precondition(blocked.storageError != nil)
        precondition(!blocked.save(meal))
        let contents = try String(contentsOf: corrupt, encoding: .utf8)
        precondition(contents == "corrupt")
        let fixture = URL(fileURLWithPath: CommandLine.arguments[1])
        let snapshot = try JSONDecoder().decode(HealthSnapshot.self, from: Data(contentsOf: fixture))
        precondition(snapshot.latestNight != nil)
        precondition(snapshot.allNights.count > 100)
        print("Health decoding, legacy cache compatibility, counters, offline persistence, write rollback, and corruption preservation passed.")
    }
}
