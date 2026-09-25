import Foundation

struct HealthSnapshot: Codable {
    struct Meta: Codable { var generatedAt: String; var fetchedAt: String? }
    struct Night: Codable, Identifiable {
        var day: String
        var period: Int?
        var totalSleepMinutes: Double?
        var awakeMinutes: Double?
        var deepMinutes: Double?
        var remMinutes: Double?
        var efficiency: Double?
        var restingHr: Double?
        var hrv: Double?
        var readiness: Double?
        var sleepScore: Double?
        var bedtimeU: Double?
        var wakeU: Double?
        var id: String { "\(day)-\(period ?? 0)" }
        var sleepDateLabel: String {
            guard let date = HealthSnapshot.sleepDate(day) else { return "Oura · Sleep date unavailable" }
            let formatter = DateFormatter()
            formatter.locale = Locale(identifier: "en_US_POSIX")
            formatter.timeZone = TimeZone(secondsFromGMT: 0)
            formatter.dateFormat = "MMM d, yyyy"
            return "Oura · Woke \(formatter.string(from: date))"
        }
    }
    struct Activity: Codable {
        struct Day: Codable, Identifiable {
            var day: String
            var score: Double?
            var activeCalories: Double?
            var mediumMinutes: Double?
            var highMinutes: Double?
            var steps: Double?
            var id: String { day }
        }
        var latest: Day?
        var daily: [Day]
    }
    struct Goal: Codable { var targetMinutes: Double; var hitRatePct: Double? }
    struct Rolling: Codable { var night: Night }
    var meta: Meta
    var latestNight: Night?
    var nights: [Night]?
    var rolling30: [Rolling]
    var activity: Activity?
    var awakeTarget: Goal?
    var allNights: [Night] {
        var byID: [String: Night] = [:]
        for night in rolling30.map(\.night) + (nights ?? []) + [latestNight].compactMap({ $0 }) {
            guard Self.sleepDate(night.day) != nil else { continue }
            byID[night.id] = night
        }
        return byID.values.sorted { ($0.day, $0.period ?? 0) < ($1.day, $1.period ?? 0) }
    }
    var currentNight: Night? { allNights.last }
    func predates(_ other: HealthSnapshot) -> Bool {
        guard let incoming = Self.timestamp(meta.generatedAt),
              let saved = Self.timestamp(other.meta.generatedAt) else { return false }
        return incoming < saved
    }
    private static func timestamp(_ value: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.date(from: value) ?? ISO8601DateFormatter().date(from: value)
    }
    private static let sleepDayFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd"
        formatter.isLenient = false
        return formatter
    }()
    private static func sleepDate(_ day: String) -> Date? {
        let formatter = sleepDayFormatter
        guard let date = formatter.date(from: day), formatter.string(from: date) == day else { return nil }
        return date
    }
}

struct HealthRecord: Codable, Identifiable, Equatable {
    var id = UUID().uuidString
    var kind: String
    var day: String
    var title: String
    var updatedAt = Date().timeIntervalSince1970
    var deleted = false
    var value: Double?
    var protein: Double?
    var reps: Double?
    var sets: Double?
    var unit: String?
    var notes: String?
    var parentID: String?
    var loadType: String?
    var warmup: Bool?
    var exerciseOrder: Int?
    var setOrder: Int?
    var startedAt: Double?
    var endedAt: Double?
    var foodItems: [FoodItem]?
    var carbs: Double?
    var fat: Double?
    var fiber: Double?
    var added_sugar: Double?
    var nutritionEstimated: Bool?
    var nutritionPartial: Bool?
    var nutritionManualTotals: Bool?
    var eatenAt: String?
    var isImported: Bool { id.hasPrefix("legacy-") }
}

func healthDay(_ date: Date = Date()) -> String {
    let f = DateFormatter(); f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "yyyy-MM-dd"; return f.string(from: date)
}
func healthDate(_ day: String) -> Date {
    let f = DateFormatter(); f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "yyyy-MM-dd"; return f.date(from: day) ?? Date()
}
func healthNumber(_ value: Double?) -> String {
    guard let value else { return "—" }
    return value.formatted(.number.precision(.fractionLength(0...1)))
}
func healthDuration(_ minutes: Double?) -> String {
    guard let minutes else { return "—" }
    let m = Int(minutes.rounded()); return "\(m / 60)h \(m % 60)m"
}
