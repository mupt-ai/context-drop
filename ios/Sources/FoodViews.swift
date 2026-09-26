import SwiftUI

struct FoodView: View {
    @ObservedObject var health: HealthStore
    @State private var date = Date()
    @State private var selected: HealthRecord?
    @State private var editing: HealthRecord?
    @State private var recentOpen = false
    private var meals: [HealthRecord] { health.entries("food", day: healthDay(date)) }
    var body: some View {
        NavigationStack {
            List {
                Section {
                    DatePicker("Day", selection: $date, in: ...Date(), displayedComponents: .date)
                    FoodTotalsView(meals: meals)
                }
                Section {
                    Button("Log a Meal", systemImage: "plus") { editing = HealthRecord(kind: "food", day: healthDay(date), title: "Meal") }.font(.headline)
                    Button("Use a Recent Meal", systemImage: "arrow.clockwise") { recentOpen = true }
                }
                Section(meals.isEmpty ? "Meals" : "\(meals.count) \(meals.count == 1 ? "Meal" : "Meals")") {
                    if meals.isEmpty { Text("No meals logged for this day.").foregroundStyle(.secondary) }
                    ForEach(meals) { meal in
                        Button { selected = meal } label: { FoodMealRow(meal: meal) }.buttonStyle(.plain)
                            .swipeActions { Button("Delete", role: .destructive) { health.delete(meal) } }
                    }
                }
                if let error = health.storageError ?? health.syncError { Text(error).font(.caption).foregroundStyle(.secondary) }
            }.healthListStyle().healthNavigationTitle("Food")
                .refreshable { await health.refresh() }
                .sheet(item: $selected) { MealDetailView(health: health, meal: $0) }
                .sheet(item: $editing) { MealEditor(health: health, meal: $0) }
                .sheet(isPresented: $recentOpen) {
                    RecentMealsView(health: health, day: healthDay(date))
                }
        }.tint(HealthStyle.ink)
    }
}

struct FoodTotalsView: View {
    let meals: [HealthRecord]
    private var estimated: Bool { meals.contains { $0.nutritionEstimated == true } }
    private var partial: Bool { meals.contains { $0.value == nil || $0.nutritionPartial == true } }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(healthNumber(FoodData.sum(meals.map(\.value)))).font(.system(size: 38, weight: .semibold, design: .rounded)).monospacedDigit()
                Text("cal").foregroundStyle(.secondary)
                Spacer()
                if estimated { Text("Estimated").font(.caption).foregroundStyle(.secondary) }
            }
            HStack(spacing: 24) {
                macro("Protein", FoodData.sum(meals.map(\.protein)))
                macro("Carbs", FoodData.sum(meals.map(\.carbs)))
                macro("Fat", FoodData.sum(meals.map(\.fat)))
            }
            if partial { Text("Totals include only logged nutrition.").font(.caption).foregroundStyle(.secondary) }
        }.padding(.vertical, 10)
    }
    private func macro(_ title: String, _ value: Double?) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(value.map { "\(healthNumber($0))g" } ?? "—").font(.headline).monospacedDigit()
            Text(title).font(.caption).foregroundStyle(.secondary)
        }.frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct FoodMealRow: View {
    let meal: HealthRecord
    var showDate = false
    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack(alignment: .firstTextBaseline) {
                Text(meal.title).font(.headline)
                Spacer()
                if let calories = meal.value { Text("\(meal.nutritionEstimated == true ? "≈" : "")\(healthNumber(calories)) cal").font(.subheadline).foregroundStyle(.secondary) }
            }
            if let items = meal.foodItems, !items.isEmpty {
                ForEach(Array(items.prefix(3))) { item in
                    VStack(alignment: .leading, spacing: 3) {
                        Text(item.name).font(.subheadline).lineLimit(2)
                        if let portion = item.portion, !portion.isEmpty { Text(portion).font(.caption).foregroundStyle(.secondary) }
                    }
                }
                if items.count > 3 { Text("+\(items.count - 3) More Foods").font(.caption).foregroundStyle(.secondary) }
            } else if let notes = FoodData.notes(meal) { Text(notes).font(.subheadline).foregroundStyle(.secondary).lineLimit(2) }
            HStack {
                if let protein = meal.protein { Text("\(healthNumber(protein))g Protein").font(.caption).foregroundStyle(.secondary) }
                else if meal.value == nil { Text("Nutrition Not Logged").font(.caption).foregroundStyle(.secondary) }
                Spacer()
                if showDate { Text(healthDate(meal.day), format: .dateTime.month(.abbreviated).day()).font(.caption).foregroundStyle(.secondary) }
                Image(systemName: "chevron.right").font(.caption2).foregroundStyle(.secondary)
            }
        }.padding(.vertical, 7)
    }
}

struct MealDetailView: View {
    @ObservedObject var health: HealthStore
    let meal: HealthRecord
    @Environment(\.dismiss) private var dismiss
    @State private var editing = false
    private var current: HealthRecord { health.entries.first { $0.id == meal.id } ?? meal }
    var body: some View {
        NavigationStack {
            List {
                Section(current.day) { FoodTotalsView(meals: [current]) }
                Section("Foods") {
                    ForEach(current.foodItems ?? []) { item in
                        VStack(alignment: .leading, spacing: 7) {
                            HStack(alignment: .top) { Text(item.name).font(.headline); Spacer(); if let cal = item.calories { Text("\(item.estimated == true ? "≈" : "")\(healthNumber(cal)) cal").font(.caption).foregroundStyle(.secondary) } }
                            if let portion = item.portion { Text(portion).font(.subheadline).foregroundStyle(.secondary) }
                            if let ingredients = item.ingredients, !ingredients.isEmpty { Text(ingredients.joined(separator: ", ")).font(.caption).foregroundStyle(.secondary) }
                        }.padding(.vertical, 5)
                    }
                    if current.foodItems?.isEmpty != false { Text(FoodData.notes(current) ?? "No food details entered.").foregroundStyle(.secondary) }
                }
                if let notes = FoodData.notes(current), current.foodItems?.isEmpty == false { Section("Notes") { Text(notes) } }
                let sources = (current.foodItems ?? []).filter { $0.nutritionNotes != nil }
                if !sources.isEmpty {
                    Section {
                        DisclosureGroup("Nutrition Notes") {
                            ForEach(sources) { item in
                                VStack(alignment: .leading, spacing: 5) { Text(item.name).font(.subheadline.weight(.medium)); Text(item.nutritionNotes ?? "").font(.caption).foregroundStyle(.secondary) }.padding(.vertical, 5)
                            }
                        }
                    }
                }
            }.healthListStyle().healthNavigationTitle(current.title)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Done") { dismiss() } }
                    ToolbarItem(placement: .confirmationAction) { Button("Edit") { editing = true } }
                }
                .sheet(isPresented: $editing) { MealEditor(health: health, meal: current) }
        }.tint(HealthStyle.ink)
    }
}

struct RecentMealsView: View {
    @ObservedObject var health: HealthStore
    let day: String
    @Environment(\.dismiss) private var dismiss
    @State private var selected: HealthRecord?
    private var recent: [HealthRecord] {
        var seen = Set<String>()
        return health.entries("food").filter { seen.insert(FoodData.signature($0)).inserted }
    }
    var body: some View {
        NavigationStack {
            List {
                ForEach(recent) { meal in
                    Button {
                        var copy = meal; copy.id = UUID().uuidString; copy.day = day; copy.eatenAt = nil
                        copy.foodItems = copy.foodItems?.map { item in var x = item; x.id = UUID().uuidString; return x }
                        selected = copy
                    } label: { FoodMealRow(meal: meal, showDate: true) }.buttonStyle(.plain)
                }
                if recent.isEmpty { Text("Your logged meals will appear here.").foregroundStyle(.secondary) }
            }.healthListStyle().healthNavigationTitle("Recent Meals")
                .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done") { dismiss() } } }
                .sheet(item: $selected) { MealEditor(health: health, meal: $0) }
        }.tint(HealthStyle.ink)
    }
}

struct MealEditor: View {
    @ObservedObject var health: HealthStore
    @State var meal: HealthRecord
    @Environment(\.dismiss) private var dismiss
    @State private var date = Date()
    @State private var calories = ""
    @State private var protein = ""
    @State private var carbs = ""
    @State private var fat = ""
    @State private var estimated = false
    @State private var foods: [FoodItem] = []
    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Meal Name", text: $meal.title)
                    DatePicker("Day", selection: $date, in: ...Date(), displayedComponents: .date)
                }
                Section("Foods") {
                    ForEach($foods) { $item in
                        VStack(alignment: .leading, spacing: 8) {
                            TextField("Food Name", text: $item.name)
                            TextField("Portion, e.g. 1 Bowl", text: Binding(get: { item.portion ?? "" }, set: { item.portion = $0 })).font(.subheadline).foregroundStyle(.secondary)
                        }.padding(.vertical, 5)
                    }.onDelete { foods.remove(atOffsets: $0) }
                    Button("Add Food", systemImage: "plus") { foods.append(FoodItem(name: "")) }
                }
                Section {
                    numeric("Calories", $calories)
                    numeric("Protein (g)", $protein)
                    numeric("Carbs (g)", $carbs)
                    numeric("Fat (g)", $fat)
                    Toggle("Estimated", isOn: $estimated)
                } header: { Text("Meal Totals") } footer: { Text("Leave unknown amounts blank. Update totals if you change portions.") }
                Section("Notes") { TextField("Optional Notes", text: Binding(get: { FoodData.notes(meal) ?? "" }, set: { meal.notes = $0 }), axis: .vertical).lineLimit(2...4) }
                if let error = health.storageError { Text(error).foregroundStyle(.red) }
            }.healthListStyle().healthNavigationTitle("Meal")
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                    ToolbarItem(placement: .confirmationAction) { Button("Save") { save() }.disabled(!valid) }
                }.onAppear {
                    meal = FoodData.normalized(meal); date = healthDate(meal.day)
                    foods = meal.foodItems ?? []
                    calories = text(meal.value); protein = text(meal.protein); carbs = text(meal.carbs); fat = text(meal.fat); estimated = meal.nutritionEstimated ?? false
                }
        }.tint(HealthStyle.ink)
    }
    private func text(_ value: Double?) -> String { value.map { String(format: "%g", $0) } ?? "" }
    private func number(_ value: String) -> Double? { Double(value.replacingOccurrences(of: ",", with: ".")) }
    private var valid: Bool {
        !meal.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty &&
        foods.allSatisfy { !$0.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty } &&
        [calories,protein,carbs,fat].allSatisfy { $0.isEmpty || (number($0).map { $0.isFinite && $0 >= 0 && $0 <= 100000 } ?? false) }
    }
    private func numeric(_ title: String, _ value: Binding<String>) -> some View {
        HStack { Text(title); Spacer(); TextField("—", text: value).keyboardType(.decimalPad).multilineTextAlignment(.trailing).frame(width: 110) }
    }
    private func save() {
        // If food/portion changed, item-level old nutrition no longer describes it.
        // Keep the explicitly edited meal totals, and clear only stale item estimates.
        let prior = Dictionary(uniqueKeysWithValues: (meal.foodItems ?? []).map { ($0.id,$0) })
        meal.foodItems = foods.map { food in
            var item = food; item.name = FoodData.title(item.name)
            if let old = prior[item.id], old.name != item.name || old.portion != item.portion {
                item.calories=nil;item.protein=nil;item.carbs=nil;item.fat=nil;item.fiber=nil;item.estimated=nil;item.nutritionNotes=nil
            }
            return item
        }
        meal.title = FoodData.title(meal.title); meal.day = healthDay(date)
        meal.value=number(calories);meal.protein=number(protein);meal.carbs=number(carbs);meal.fat=number(fat)
        meal.nutritionEstimated=estimated;meal.nutritionPartial=meal.value == nil || meal.protein == nil
        if health.save(meal) { dismiss(); Task { await health.refresh() } }
    }
}
