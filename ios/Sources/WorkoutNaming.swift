import Foundation

/// Keep aliases aligned with the private API's workout_names.py.
enum WorkoutNaming {
    private static let aliases: [String: String] = [
        "assisted pull ups": "Assisted Pull-Ups",
        "assisted pullups": "Assisted Pull-Ups",
        "bicep curl": "Biceps Curl",
        "bicep curls": "Biceps Curl",
        "biceps curls": "Biceps Curl",
        "calf raises": "Calf Raise",
        "dumbbell bench press": "Flat Dumbbell Bench Press",
        "dumbbell bicep curl": "Dumbbell Curl",
        "dumbbell biceps curl": "Dumbbell Curl",
        "dumbbell curls": "Dumbbell Curl",
        "dumbbell hammer curls": "Dumbbell Hammer Curl",
        "dumbbell rdl": "Dumbbell Romanian Deadlift",
        "dumbbell skull crushers": "Dumbbell Skull Crusher",
        "incline dumbbell press": "Incline Dumbbell Bench Press",
        "lateral raises": "Lateral Raise",
        "lying tricep extension": "Lying Triceps Extension",
        "pull ups": "Pull-Ups",
        "pullups": "Pull-Ups",
        "sit ups": "Sit-Ups",
        "situps": "Sit-Ups",
        "tricep overhead pulldown": "Overhead Triceps Pulldown",
        "tricep pulldown": "Triceps Pulldown",
        "tricep pushdown": "Triceps Pushdown",
    ]
    static func canonical(_ value: String) -> String {
        let text = value.split(whereSeparator: { $0.isWhitespace }).joined(separator: " ")
        let key = text.lowercased().replacingOccurrences(of: "–", with: "-").replacingOccurrences(of: "—", with: "-")
        if let name = aliases[key] { return name }
        let acronyms = ["db": "DB", "bb": "BB", "rdl": "RDL", "ez": "EZ"]
        return text.capitalized(with: Locale(identifier: "en_US_POSIX")).split(separator: " ").map { acronyms[$0.lowercased()] ?? String($0) }.joined(separator: " ")
    }
    static func record(_ record: HealthRecord) -> HealthRecord {
        var copy = FoodData.normalized(record)
        if ["workout", "lift"].contains(copy.kind) { copy.title = canonical(copy.title) }
        return copy
    }
}
