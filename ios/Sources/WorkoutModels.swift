import Foundation

struct WorkoutSet: Codable, Identifiable, Equatable {
    var id = UUID().uuidString
    var weight = ""
    var reps = ""
    var completed = false
    var warmup = false
    var previous: String?
    var load: Double? { Double(weight.replacingOccurrences(of: ",", with: ".")) }
    var repCount: Int? { Int(reps) }
    func isValid(loadType: String) -> Bool {
        guard let reps = repCount, reps > 0, reps <= 1000 else { return false }
        if loadType == "bodyweight" { return true }
        guard let value = load, value.isFinite, value >= 0, value <= 10000 else { return false }
        return true
    }
}

struct WorkoutExercise: Codable, Identifiable, Equatable {
    var id = UUID().uuidString
    var title: String
    var unit = "lb"
    var loadType = "weight"
    var sets: [WorkoutSet] = [WorkoutSet()]
    var notes = ""
    var completedSets: Int { sets.filter(\.completed).count }
}

struct WorkoutSession: Codable, Identifiable, Equatable {
    var id = UUID().uuidString
    var title: String
    var startedAt = Date()
    var exercises: [WorkoutExercise] = []
    var restSeconds = 90
    var restUntil: Date?
    var completedSets: Int { exercises.reduce(0) { $0 + $1.completedSets } }
    var plannedSets: Int { exercises.reduce(0) { $0 + $1.sets.count } }
    var nextExerciseID: String? { exercises.first { $0.sets.contains { !$0.completed } }?.id }
    func volume(unit: String) -> Double {
        exercises.filter { $0.unit == unit && $0.loadType == "weight" }.flatMap(\.sets)
            .filter { $0.completed && !$0.warmup }.reduce(0) { $0 + ($1.load ?? 0) * Double($1.repCount ?? 0) }
    }
    func records(finishedAt: Date) -> [HealthRecord] {
        var records = [HealthRecord(id: id, kind: "workout", day: healthDay(startedAt), title: title,
                                    value: max(0, finishedAt.timeIntervalSince(startedAt)), sets: Double(completedSets),
                                    notes: "\(exercises.filter { $0.completedSets > 0 }.count) exercises", startedAt: startedAt.timeIntervalSince1970,
                                    endedAt: finishedAt.timeIntervalSince1970)]
        for (exerciseIndex, exercise) in exercises.enumerated() {
            for (setIndex, set) in exercise.sets.enumerated() where set.completed {
                records.append(HealthRecord(id: "set-\(set.id)", kind: "lift", day: healthDay(startedAt), title: exercise.title,
                                            value: exercise.loadType == "bodyweight" ? nil : set.load,
                                            reps: set.repCount.map(Double.init), sets: 1, unit: exercise.unit,
                                            notes: exercise.notes.isEmpty ? nil : exercise.notes, parentID: id,
                                            loadType: exercise.loadType, warmup: set.warmup, exerciseOrder: exerciseIndex, setOrder: setIndex))
            }
        }
        return records
    }
}

struct WorkoutHistoryItem: Identifiable {
    var id: String
    var title: String
    var day: String
    var lifts: [HealthRecord]
    var duration: Double?
    var completedSets: Int { lifts.reduce(0) { $0 + Int($1.sets ?? 1) } }
    var exerciseNames: [String] {
        var seen = Set<String>(); return lifts.map(\.title).filter { seen.insert($0).inserted }
    }
    func repeated() -> [WorkoutExercise] {
        exerciseNames.map { title in
            let logs = lifts.filter { $0.title == title }
            let first = logs[0]
            let sets = logs.flatMap { record in
                (0..<min(30, max(1, Int(record.sets ?? 1)))).map { _ in
                    WorkoutSet(weight: record.value.map { String($0) } ?? "", reps: record.reps.map { String(Int($0)) } ?? "",
                               warmup: record.warmup ?? false, previous: workoutSetDescription(record))
                }
            }
            return WorkoutExercise(title: title, unit: first.unit ?? "lb", loadType: first.loadType ?? "weight", sets: sets, notes: first.notes ?? "")
        }
    }
}

func workoutSetDescription(_ record: HealthRecord) -> String {
    let load: String
    switch record.loadType {
    case "bodyweight": load = "BW"
    case "assisted": load = "−\(healthNumber(record.value)) \(record.unit ?? "lb")"
    default: load = "\(healthNumber(record.value)) \(record.unit ?? "lb")"
    }
    return "\(load) × \(healthNumber(record.reps))"
}

func workoutHistory(_ records: [HealthRecord]) -> [WorkoutHistoryItem] {
    let live = records.filter { !$0.deleted }
    let metadata = Dictionary(uniqueKeysWithValues: live.filter { $0.kind == "workout" }.map { ($0.id, $0) })
    let groups = Dictionary(grouping: live.filter { $0.kind == "lift" }) { record in
        record.parentID ?? "legacy-day-\(record.day)"
    }
    return groups.map { id, records in
        let lifts = records.sorted {
            if let a = $0.exerciseOrder, let b = $1.exerciseOrder {
                return a == b ? ($0.setOrder ?? 0) < ($1.setOrder ?? 0) : a < b
            }
            // Legacy exported IDs contain the original movement order.
            let a = Int($0.id.split(separator: "-").last ?? "") ?? 0
            let b = Int($1.id.split(separator: "-").last ?? "") ?? 0
            return a == b ? $0.updatedAt < $1.updatedAt : a < b
        }
        let first = lifts[0]
        let legacyTitle = first.isImported ? first.notes?.components(separatedBy: " · ").first : nil
        return WorkoutHistoryItem(id: id, title: metadata[id]?.title ?? legacyTitle ?? "Workout", day: first.day, lifts: lifts, duration: metadata[id]?.value)
    }.sorted { ($0.day, metadata[$0.id]?.startedAt ?? 0) > ($1.day, metadata[$1.id]?.startedAt ?? 0) }
}
