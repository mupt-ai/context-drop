import SwiftUI
import Charts

struct FoodGoalProgress: View {
    @ObservedObject var health: HealthStore
    let meals: [HealthRecord]
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            ForEach([HealthGoal.calories, .protein], id: \.rawValue) { goal in
                if let target = HealthGoal.record(goal, in: health.records), let value = target.value {
                    let total = FoodData.sum(meals.map { goal == .calories ? $0.value : $0.protein })
                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text(goal == .calories ? "Calories" : "Protein").font(.subheadline.weight(.medium))
                            Spacer()
                            Text("\(healthNumber(total)) / \(healthNumber(value)) \(goal.unit)").font(.subheadline).monospacedDigit()
                        }
                        ProgressView(value: min(total ?? 0, value), total: value).tint(HealthStyle.ink)
                        Text(HealthGoal.remaining(total: total, target: value).map { $0 == 0 ? "Goal reached from logged food" : "\(healthNumber($0)) \(goal.unit) left from logged food" } ?? "No nutrition logged yet")
                            .font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                    }
                }
            }
            if HealthGoal.record(.calories, in: health.records) == nil && HealthGoal.record(.protein, in: health.records) == nil {
                Text("Set calorie and protein targets to see your daily progress.").font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
            }
        }.padding(.vertical, 4)
    }
}

struct HealthGoalsEditor: View {
    @ObservedObject var health: HealthStore
    @Environment(\.dismiss) private var dismiss
    @State private var calories = ""
    @State private var protein = ""
    @State private var weight = ""
    @State private var unit = "lb"
    private func number(_ text: String) -> Double? { Double(text.trimmingCharacters(in: .whitespaces)) }
    private var valid: Bool {
        [calories, protein, weight].allSatisfy { $0.trimmingCharacters(in: .whitespaces).isEmpty || (number($0).map { $0.isFinite && $0 > 0 && $0 <= 100000 } ?? false) }
    }
    var body: some View {
        NavigationStack {
            Form {
                Section("Daily Food Goals") {
                    field("Calories", text: $calories, unit: "cal")
                    field("Protein", text: $protein, unit: "g")
                }
                Section("Body Weight Goal") {
                    field("Goal Weight", text: $weight, unit: unit)
                    Picker("Unit", selection: $unit) { Text("lb").tag("lb"); Text("kg").tag("kg") }.pickerStyle(.segmented)
                        .onChange(of: unit) { old, new in
                            if let value = number(weight) { weight = String(format: "%.1f", HealthGoal.convertedWeight(value, from: old, to: new)) }
                        }
                }
                Section { Text("Leave a target blank to remove it. These goals are shared with Context Drop.").font(.caption).foregroundStyle(HealthStyle.secondaryInk) }
                if let error = health.storageError { Text(error).foregroundStyle(.red) }
            }.healthListStyle().healthNavigationTitle("Goals")
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                    ToolbarItem(placement: .confirmationAction) { Button("Save") { save() }.disabled(!valid) }
                }
                .onAppear {
                    calories = HealthGoal.record(.calories, in: health.records)?.value.map { String($0) } ?? ""
                    protein = HealthGoal.record(.protein, in: health.records)?.value.map { String($0) } ?? ""
                    let goal = HealthGoal.record(.bodyweight, in: health.records)
                    unit = goal?.unit ?? "lb"
                    weight = goal?.value.map { String($0) } ?? ""
                }
        }.tint(HealthStyle.ink)
    }
    private func field(_ label: String, text: Binding<String>, unit: String) -> some View {
        HStack { Text(label); Spacer(); TextField("Not Set", text: text).keyboardType(.decimalPad).multilineTextAlignment(.trailing); Text(unit).foregroundStyle(HealthStyle.secondaryInk) }
    }
    private func save() {
        let input: [(HealthGoal, String)] = [(.calories, calories), (.protein, protein), (.bodyweight, weight)]
        let records = input.map { goal, text in
            var record = health.records.first { $0.id == goal.id } ?? HealthRecord(id: goal.id, kind: "target", day: healthDay(), title: goal.title)
            record.value = number(text); record.deleted = record.value == nil
            record.unit = goal == .bodyweight ? unit : goal.unit
            record.day = healthDay()
            return record
        }
        if health.saveBatch(records) { Task { await health.refresh() }; dismiss() }
    }
}

struct BodyWeightGoalsView: View {
    @ObservedObject var health: HealthStore
    @State private var editingGoals = false
    @State private var entry: HealthRecord?
    private var target: HealthRecord? { HealthGoal.record(.bodyweight, in: health.records) }
    private var unit: String { target?.unit ?? health.entries("bodyweight").first?.unit ?? "lb" }
    private var weights: [HealthRecord] { health.entries("bodyweight").filter { $0.value != nil && ["lb", "kg"].contains($0.unit ?? "lb") } }
    private func value(_ record: HealthRecord) -> Double { HealthGoal.convertedWeight(record.value ?? 0, from: record.unit ?? "lb", to: unit) }
    var body: some View {
        NavigationStack {
            List {
                Section {
                    HStack { metric("Latest", weights.first.map { value($0) }); Spacer(); metric("Goal", target?.value) }
                    if let latest = weights.first, let goal = target?.value {
                        let difference = goal - value(latest)
                        Text(abs(difference) < 0.05 ? "At your goal weight" : "\(healthNumber(abs(difference))) \(unit) \(difference > 0 ? "to gain" : "above goal")").font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
                        Text("Last logged \(healthDate(latest.day).formatted(date: .abbreviated, time: .omitted))").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                    }
                    Button(target == nil ? "Set Goals" : "Edit Goals") { editingGoals = true }
                }
                if !weights.isEmpty {
                    Section("Weight History") {
                        Chart {
                            ForEach(weights.sorted { $0.day < $1.day }) { record in
                                LineMark(x: .value("Day", healthDate(record.day)), y: .value("Weight", value(record)))
                                PointMark(x: .value("Day", healthDate(record.day)), y: .value("Weight", value(record)))
                            }
                            if let target = target?.value { RuleMark(y: .value("Goal", target)).lineStyle(StrokeStyle(dash: [5, 4])).foregroundStyle(HealthStyle.secondaryInk).annotation(position: .top, alignment: .leading) { Text("Goal").font(.caption).foregroundStyle(HealthStyle.secondaryInk) } }
                        }.chartYScale(domain: .automatic(includesZero: false)).frame(height: 190).foregroundStyle(HealthStyle.ink)
                    }
                }
                Section {
                    Button("Log Body Weight", systemImage: "plus") { entry = HealthRecord(kind: "bodyweight", day: healthDay(), title: "Body Weight", unit: unit) }
                    ForEach(weights) { record in
                        HStack { Text(healthDate(record.day), format: .dateTime.month(.abbreviated).day()); Spacer(); Text("\(healthNumber(value(record))) \(unit)") }
                    }
                }
            }.healthListStyle().healthNavigationTitle("Body Weight")
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
                .sheet(isPresented: $editingGoals) { HealthGoalsEditor(health: health) }
                .sheet(item: $entry) { EntryEditor(health: health, record: $0) }
        }.tint(HealthStyle.ink)
    }
    @Environment(\.dismiss) private var dismiss
    private func metric(_ label: String, _ number: Double?) -> some View {
        VStack(alignment: .leading, spacing: 6) { Text(label).font(.caption).foregroundStyle(HealthStyle.secondaryInk); Text("\(healthNumber(number)) \(unit)").font(.title2.weight(.semibold)).monospacedDigit() }
    }
}
