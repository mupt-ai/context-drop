import Foundation

struct FoodItem: Codable, Identifiable, Equatable {
    var id = UUID().uuidString
    var name: String
    var portion: String?
    var ingredients: [String]?
    var calories: Double?
    var protein: Double?
    var carbs: Double?
    var fat: Double?
    var fiber: Double?
    var added_sugar: Double?
    var estimated: Bool?
    var nutritionNotes: String?
}

enum FoodData {
    static func title(_ text: String) -> String { text.trimmingCharacters(in: .whitespacesAndNewlines).capitalized(with: Locale(identifier: "en_US_POSIX")) }
    static func normalized(_ original: HealthRecord) -> HealthRecord {
        guard original.kind == "food" else { return original }
        var record = original; record.title = title(record.title)
        if record.foodItems == nil, let notes = record.notes {
            let lines = notes.split(separator: "\n").map(String.init)
            let objects = lines.compactMap { line -> [String: Any]? in
                guard let data = line.data(using: .utf8), let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any], object["food"] is String else { return nil }
                return object
            }
            if !objects.isEmpty && objects.count == lines.count {
                record.foodItems = objects.enumerated().map { i, object in
                    let macros = object["estimated_macros"] as? [String: Any] ?? [:]
                    var portion = object["reported_amount"] as? String
                    if portion == nil, let quantity = object["quantity"] as? NSNumber {
                        portion = "\(quantity.stringValue) \(object["unit"] as? String ?? "")".trimmingCharacters(in: .whitespaces)
                    }
                    let type = macros["value_type"] as? String ?? ""
                    return FoodItem(id: "\(record.id)-item-\(i)", name: title((object["food"] as? String ?? "Food").replacingOccurrences(of: " (brand/flavor unspecified)", with: "")),
                                    portion: portion.map(title), ingredients: (object["ingredients"] as? [String])?.map(title),
                                    calories: macros["calories_kcal"] as? Double, protein: macros["protein_g"] as? Double,
                                    carbs: macros["carbs_g"] as? Double, fat: macros["fat_g"] as? Double, fiber: macros["fiber_g"] as? Double,
                                    estimated: type.contains("estimate") || type.contains("assumption"), nutritionNotes: macros["basis"] as? String)
                }
                record.notes = nil
            }
        }
        if let items = record.foodItems, !items.isEmpty {
            record.foodItems = items.map { item in var copy = item; copy.name = title(item.name); return copy }
            if record.nutritionManualTotals == true { return record }
            record.value = record.value ?? sum(items.map(\.calories))
            record.protein = record.protein ?? sum(items.map(\.protein))
            record.carbs = record.carbs ?? sum(items.map(\.carbs))
            record.fat = record.fat ?? sum(items.map(\.fat))
            record.fiber = record.fiber ?? sum(items.map(\.fiber))
            if record.added_sugar == nil, items.allSatisfy({ $0.added_sugar != nil }) {
                record.added_sugar = sum(items.map(\.added_sugar))
            }
            record.nutritionEstimated = record.nutritionEstimated == true || items.contains { $0.estimated == true }
            record.nutritionPartial = record.nutritionPartial ?? items.contains { $0.calories == nil || $0.protein == nil }
        }
        return record
    }
    static func sum(_ values: [Double?]) -> Double? {
        let known = values.compactMap { $0 }.filter { $0.isFinite && $0 >= 0 }
        return known.isEmpty ? nil : known.reduce(0, +)
    }
    static func notes(_ record: HealthRecord) -> String? {
        guard let notes = record.notes?.trimmingCharacters(in: .whitespacesAndNewlines), !notes.isEmpty,
              !notes.hasPrefix("{"), !notes.hasPrefix("[") else { return nil }
        return notes
    }
    static func signature(_ meal: HealthRecord) -> String {
        meal.title.lowercased() + "|" + (meal.foodItems ?? []).map { $0.name.lowercased() + ":" + ($0.portion ?? "") }.joined(separator: "|") + "|" + (notes(meal) ?? "")
    }
}
