import Foundation

@main struct WorkoutTests {
    @MainActor static func main() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let health = HealthStore(directory: dir)
        let store = WorkoutStore(directory: dir)
        let exercise = WorkoutExercise(title: "Row", sets: [WorkoutSet(weight: "22.5", reps: "10"), WorkoutSet(weight: "22.5", reps: "8")])
        precondition(store.start(title: "Pull", exercises: [exercise]))
        precondition(!store.start(title: "Duplicate"))
        let start = Date(timeIntervalSince1970: 1789338000)
        precondition(store.complete(exercise: exercise.id, set: exercise.sets[0].id, now: start))
        precondition(store.active?.completedSets == 1)
        precondition(store.active?.restUntil == start.addingTimeInterval(90))
        let reloaded = WorkoutStore(directory: dir)
        precondition(reloaded.active == store.active, "Draft/sets/rest deadline must survive restart")
        precondition(reloaded.active?.volume(unit: "lb") == 225)
        let sessionID = reloaded.active!.id
        let summary = reloaded.finish(health: health)!
        precondition(summary.completedSets == 1 && health.entries("lift").count == 1, "Only checked sets should be saved")
        precondition(health.entries("workout").count == 1)
        precondition(WorkoutStore(directory: dir).active == nil)
        // Simulate crash after health records committed, before the old draft was cleared.
        _ = store.finish(health: health)
        precondition(health.entries("lift").count == 1 && health.entries("workout").count == 1, "Retry duplicated a session")
        precondition(health.entries("workout")[0].id == sessionID)
        let repeated = summary.repeated()
        precondition(repeated[0].sets[0].weight == "22.5")
        precondition(!repeated[0].sets[0].completed)
        precondition(repeated[0].sets[0].id != exercise.sets[0].id)
        let invalid = WorkoutSet(weight: "-2", reps: "10")
        precondition(!invalid.isValid(loadType: "weight"))
        precondition(!WorkoutSet(weight: "nan", reps: "10").isValid(loadType: "weight"))
        precondition(!WorkoutSet(weight: "10", reps: "0").isValid(loadType: "weight"))
        precondition(WorkoutSet(reps: "5").isValid(loadType: "bodyweight"))
        let legacy = HealthRecord(id: "legacy-lift-1", kind: "lift", day: "2026-09-12", title: "Pull-Ups", value: 30, reps: 10, sets: 4, unit: "lb", notes: "Pull day · Assisted", loadType: "assisted")
        let past = workoutHistory([legacy])[0]
        precondition(past.repeated()[0].loadType == "assisted")
        precondition(past.repeated()[0].sets.count == 4)
        let newStore = WorkoutStore(directory: dir)
        precondition(newStore.start(title: "Keep on failure", exercises: [exercise]))
        _ = newStore.complete(exercise: exercise.id, set: exercise.sets[0].id)
        try FileManager.default.removeItem(at: dir)
        precondition(newStore.finish(health: health) == nil)
        precondition(newStore.active != nil, "Failed save lost draft")
        precondition(health.entries("lift").count == 1, "Failed batch partially changed health log")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let file = dir.appendingPathComponent("active-workout.json")
        try Data("invalid".utf8).write(to: file)
        let corrupt = WorkoutStore(directory: dir)
        precondition(corrupt.error != nil && !corrupt.start(title: "Overwrite"))
        print("PASS: workout draft/restart, rest deadline, checked sets only, atomic finish, crash retry deduplication, repeat hints, assisted/bodyweight semantics, invalid inputs, failure and corrupt-file preservation")
    }
}
