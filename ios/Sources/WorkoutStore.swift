import Foundation
import Combine

@MainActor
final class WorkoutStore: ObservableObject {
    @Published private(set) var active: WorkoutSession?
    @Published private(set) var error: String?
    private let file: URL
    private var readable = true
    init(directory: URL? = nil) {
        let root = directory ?? FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        file = root.appendingPathComponent("active-workout.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return }
        do { active = try JSONDecoder().decode(WorkoutSession?.self, from: Data(contentsOf: file)) }
        catch { self.error = "Couldn’t open your saved workout. It has been kept on this phone."; readable = false }
    }
    @discardableResult private func commit(_ session: WorkoutSession?) -> Bool {
        guard readable else { return false }
        do {
            let data = try JSONEncoder().encode(session)
            try data.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            active = session; error = nil; return true
        } catch { self.error = "Couldn’t save the workout. Please try again."; return false }
    }
    @discardableResult func start(title: String, exercises: [WorkoutExercise] = []) -> Bool {
        guard active == nil else { return false }
        return commit(WorkoutSession(title: title.isEmpty ? "Workout" : title, exercises: exercises))
    }
    @discardableResult func update(_ change: (inout WorkoutSession) -> Void) -> Bool {
        guard var session = active else { return false }; change(&session); return commit(session)
    }
    func editExercise(_ id: String, change: (inout WorkoutExercise) -> Void) {
        update { session in
            guard let i = session.exercises.firstIndex(where: { $0.id == id }) else { return }
            change(&session.exercises[i])
        }
    }
    func editSet(exercise id: String, set setID: String, change: (inout WorkoutSet) -> Void) {
        editExercise(id) { exercise in
            guard let index = exercise.sets.firstIndex(where: { $0.id == setID }) else { return }
            change(&exercise.sets[index])
        }
    }
    @discardableResult func complete(exercise id: String, set setID: String, now: Date = Date()) -> Bool {
        guard let session = active, let exercise = session.exercises.first(where: { $0.id == id }),
              let set = exercise.sets.first(where: { $0.id == setID }), set.completed || set.isValid(loadType: exercise.loadType) else { return false }
        return update { session in
            let ei = session.exercises.firstIndex { $0.id == id }!
            let si = session.exercises[ei].sets.firstIndex { $0.id == setID }!
            session.exercises[ei].sets[si].completed.toggle()
            session.restUntil = session.exercises[ei].sets[si].completed && session.restSeconds > 0 ? now.addingTimeInterval(Double(session.restSeconds)) : nil
        }
    }
    func addSet(exercise id: String) {
        editExercise(id) { exercise in
            let last = exercise.sets.last
            exercise.sets.append(WorkoutSet(weight: last?.weight ?? "", reps: last?.reps ?? "", previous: last?.previous))
        }
    }
    func addExercise(_ exercise: WorkoutExercise) { update { $0.exercises.append(exercise) } }
    func discard() -> Bool { commit(nil) }
    // The draft remains until all completed sets and metadata are saved atomically.
    // Retrying after a crash uses the same IDs, so it cannot duplicate sets.
    func finish(health: HealthStore, now: Date = Date()) -> WorkoutHistoryItem? {
        guard let session = active, session.completedSets > 0 else { return nil }
        let records = session.records(finishedAt: now)
        guard health.saveBatch(records) else { error = health.storageError; return nil }
        guard commit(nil) else { return nil }
        return workoutHistory(records).first
    }
}
