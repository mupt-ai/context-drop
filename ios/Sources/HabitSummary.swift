import Foundation

/// Overlapping manual and automatic confirmations describe one noticed episode.
struct HabitSummary {
    static func confirmedTouches(in sessions: [MotionSession], on day: Date, calendar: Calendar = .current) -> Int {
        let items = sessions.flatMap(\.candidates).filter { $0.label == .touch }.sorted { $0.from < $1.from }
        var episodes: [(start: Date, end: Date, noticed: Date)] = []
        for item in items {
            if let previous = episodes.last, item.from <= previous.end {
                episodes[episodes.count - 1].end = max(previous.end, item.through)
            } else {
                episodes.append((item.from, item.through, item.time))
            }
        }
        return episodes.filter { calendar.isDate($0.noticed, inSameDayAs: day) }.count
    }
}
