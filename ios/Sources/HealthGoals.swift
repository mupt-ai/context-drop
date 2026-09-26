import Foundation

enum HealthGoal: String, CaseIterable {
    case calories, protein, bodyweight
    var id: String { "target-\(rawValue)" }
    var title: String {
        switch self { case .calories: return "Daily Calories"; case .protein: return "Daily Protein"; case .bodyweight: return "Goal Weight" }
    }
    var unit: String {
        switch self { case .calories: return "cal"; case .protein: return "g"; case .bodyweight: return "lb" }
    }
    static func record(_ goal: HealthGoal, in records: [HealthRecord]) -> HealthRecord? {
        records.first { $0.id == goal.id && $0.kind == "target" && !$0.deleted && ($0.value ?? 0) > 0 }
    }
    static func convertedWeight(_ value: Double, from: String, to: String) -> Double {
        if from == to { return value }
        return from == "kg" ? value * 2.2046226218 : value / 2.2046226218
    }
    static func remaining(total: Double?, target: Double) -> Double? {
        total.map { max(0, target - $0) }
    }
    static func sleepHours(in records: [HealthRecord]) -> Double? {
        records.first { $0.id == "target-sleep" && !$0.deleted }?.value
    }
}

struct SleepRecovery {
    let minutes: Double?
    let goalHours: Double?
    var targetMinutes: Double? { goalHours.map { $0 * 60 } }
    var progress: Double? {
        guard let minutes, let targetMinutes, targetMinutes > 0 else { return nil }
        return min(1, minutes / targetMinutes)
    }
    var comparison: String? {
        guard let minutes, let targetMinutes else { return nil }
        let difference = Int(abs(minutes - targetMinutes).rounded())
        if difference < 5 { return "You met your sleep goal." }
        return minutes >= targetMinutes ? "\(difference) minutes above your goal." : "\(difference) minutes below your goal."
    }
}
