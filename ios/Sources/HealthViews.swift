import SwiftUI
import Charts



enum HealthTab: Hashable { case today, habits, weights, food, digest }

struct HealthRootView: View {
    @ObservedObject var monitor: RingMonitor
    @ObservedObject var recordings: RecordingStore
    @ObservedObject var health: HealthStore
    @State private var tab: HealthTab = .today
    @StateObject private var workouts = WorkoutStore()
    @Environment(\.scenePhase) private var scenePhase
    private let timer = Timer.publish(every: 60, on: .main, in: .common).autoconnect()
    var body: some View {
        TabView(selection: $tab) {
            TodayView(health: health, recordings: recordings)
                .tag(HealthTab.today).tabItem { Label("Today", systemImage: "sun.max") }
            HabitsView(health: health)
                .tag(HealthTab.habits).tabItem { Label("Habits", systemImage: "plus.forwardslash.minus") }
            WorkoutsView(health: health, workouts: workouts)
                .tag(HealthTab.weights).tabItem { Label("Weights", systemImage: "dumbbell") }
            FoodView(health: health)
                .tag(HealthTab.food).tabItem { Label("Food", systemImage: "fork.knife") }
            DigestView()
                .tag(HealthTab.digest).tabItem { Label("Digest", systemImage: "newspaper") }
        }.tint(HealthStyle.ink)
            .task { health.syncTouches(recordings.sessions); await health.refresh() }
            .onReceive(timer) { _ in
                guard scenePhase == .active else { return }
                health.syncTouches(recordings.sessions); Task { await health.refresh() }
            }
            .onChange(of: scenePhase) { _, phase in
                if phase == .active { health.syncTouches(recordings.sessions); Task { await health.refresh() } }
            }
    }
}

struct TodayView: View {
    @ObservedObject var health: HealthStore
    @ObservedObject var recordings: RecordingStore
    @State private var showDetails = false
    @State private var editingGoals = false
    @State private var showWeight = false
    private var night: HealthSnapshot.Night? { health.snapshot?.currentNight }
    private var sleep: SleepRecovery {
        SleepRecovery(minutes: night?.totalSleepMinutes, goalHours: HealthGoal.sleepHours(in: health.records))
    }
    private var meals: [HealthRecord] { health.entries("food", day: healthDay()) }
    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: HealthStyle.sectionGap) {
                    Text(Date(), format: .dateTime.weekday(.wide).month(.abbreviated).day())
                        .font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
                    VStack(alignment: .leading, spacing: 16) {
                        Label("Daily Nutrition", systemImage: "target").font(.headline)
                        FoodGoalProgress(health: health, meals: meals)
                        if meals.contains(where: { $0.value == nil || $0.protein == nil }) {
                            Text("Some meals have no nutrition values. Progress reflects known values only.")
                                .font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                        }
                    }.healthCard()

                    Button { showDetails = true } label: {
                        VStack(alignment: .leading, spacing: 12) {
                            HStack {
                                Label("Recovery", systemImage: "moon").font(.headline)
                                Spacer()
                                Image(systemName: "chevron.right").font(.caption)
                            }
                            Text(night?.sleepDateLabel ?? "No sleep data yet")
                                .font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                            HealthAdaptiveStack {
                                metric(sleep.targetMinutes.map { "of \(healthDuration($0)) goal" } ?? "Sleep", healthDuration(sleep.minutes))
                                Spacer()
                                metric("Readiness", healthNumber(night?.readiness))
                            }
                            if let progress = sleep.progress, let comparison = sleep.comparison {
                                ProgressView(value: progress).tint(HealthStyle.ink)
                                Text(comparison).font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                            } else if sleep.targetMinutes != nil {
                                Text("Sleep data unavailable for this night.").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                            }
                        }.healthCard()
                    }.buttonStyle(.plain)
                    if let target = HealthGoal.record(.bodyweight, in: health.records), let value = target.value {
                        VStack(alignment: .leading, spacing: 10) {
                            Label("Weight Goal", systemImage: "scalemass").font(.headline)
                            HStack(alignment: .firstTextBaseline) {
                                Text("\(healthNumber(value)) \(target.unit ?? "lb")")
                                    .font(HealthStyle.metric)
                                Spacer()
                                Button("View Progress") { showWeight = true }.font(.subheadline)
                            }
                            Text("Follow your weight trend, not a single weigh-in.").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                        }.healthCard()
                    }
                    if let error = health.storageError ?? health.syncError {
                        Text(error).font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                    }
                    HStack {
                        if let checked = health.lastChecked {
                            Text("Last checked \(checked, style: .relative) ago")
                        } else { Text("Pull to refresh your goals") }
                        Spacer()
                        if health.pendingCount > 0 { Text("\(health.pendingCount) Pending") }
                    }.font(.caption2).foregroundStyle(HealthStyle.secondaryInk)
                }.padding(.horizontal, HealthStyle.pageInset).padding(.vertical, 16)
            }.refreshable { await health.refresh() }
                .background(HealthStyle.paper).foregroundStyle(HealthStyle.ink)
                .healthNavigationTitle("Your Goals")
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Edit Goals", systemImage: "slider.horizontal.3") { editingGoals = true }
                    }
                }
                .sheet(isPresented: $showDetails) { NavigationStack { HealthDetailView(health: health) } }
                .sheet(isPresented: $editingGoals) { HealthGoalsEditor(health: health) }
                .sheet(isPresented: $showWeight) { NavigationStack { BodyWeightGoalsView(health: health) } }
        }
    }

    private func metric(_ title: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 4) { Text(value).font(.title3.weight(.semibold)); Text(title).font(.caption).foregroundStyle(HealthStyle.secondaryInk) }
    }
    private func summaryCard(_ title: String, value: String, detail: String, icon: String) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            Label(title, systemImage: icon).font(.caption.weight(.semibold))
            Text(value).font(HealthStyle.metric).minimumScaleFactor(0.7).lineLimit(1)
            Text(detail).font(.caption2).foregroundStyle(HealthStyle.secondaryInk).lineLimit(1)
        }.frame(maxWidth: .infinity, alignment: .leading).healthCard()
    }
}

func foodTotal(_ meals: [HealthRecord]) -> String {
    let known = meals.compactMap(\.value)
    guard !known.isEmpty else { return "\(meals.count) Meals" }
    let suffix = known.count < meals.count ? " + Uncounted" : ""
    return "\(healthNumber(known.reduce(0, +))) cal\(suffix)"
}

struct HealthDetailView: View {
    @ObservedObject var health: HealthStore
    @Environment(\.dismiss) private var dismiss
    @State private var metric = "Sleep"
    var day: String?
    private var night: HealthSnapshot.Night? {
        if let day { return health.snapshot?.allNights.last { $0.day == day } }
        return health.snapshot?.currentNight
    }
    private var activity: HealthSnapshot.Activity.Day? {
        if let day { return health.snapshot?.activity?.daily.last { $0.day == day } }
        return health.snapshot?.activity?.latest
    }
    var body: some View {
        List {
            Section(night?.sleepDateLabel ?? day ?? "Sleep") {
                valueRow("Sleep", healthDuration(night?.totalSleepMinutes))
                valueRow("Awake", "\(healthNumber(night?.awakeMinutes)) min")
                valueRow("Deep", healthDuration(night?.deepMinutes))
                valueRow("REM", healthDuration(night?.remMinutes))
                valueRow("Sleep Score", healthNumber(night?.sleepScore))
                valueRow("Readiness", healthNumber(night?.readiness))
                valueRow("Resting Heart Rate", "\(healthNumber(night?.restingHr)) bpm")
                valueRow("HRV", "\(healthNumber(night?.hrv)) ms")
                valueRow("Efficiency", "\(healthNumber(night?.efficiency))%")
            }
            Section("Movement · \(activity?.day ?? day ?? "No Data")") {
                valueRow("Steps", healthNumber(activity?.steps))
                valueRow("Active Calories", healthNumber(activity?.activeCalories))
                valueRow("Activity Score", healthNumber(activity?.score))
                valueRow("Moderate Activity", "\(healthNumber(activity?.mediumMinutes)) min")
                valueRow("High Activity", "\(healthNumber(activity?.highMinutes)) min")
            }
            Section("Last 30 Nights") {
                Picker("Metric", selection: $metric) { ForEach(["Sleep", "Awake", "HRV", "Readiness"], id: \.self) { Text($0) } }.pickerStyle(.segmented)
                Chart(Array((health.snapshot?.allNights ?? []).suffix(30))) { night in
                    if let value = chartValue(night) {
                        LineMark(x: .value("Day", healthDate(night.day)), y: .value(metric, value)).foregroundStyle(HealthStyle.ink)
                    }
                }.frame(height: 160)
                Text(metric == "Sleep" || metric == "Awake" ? "Minutes" : metric == "HRV" ? "Milliseconds" : "Score").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
            }
            Section("Sync") {
                Text("Checks Oura’s cloud every five minutes. New ring data appears after Oura syncs it.").font(.footnote)
                if let fetched = health.snapshot?.meta.fetchedAt { valueRow("Cloud Checked", fetched) }
                if let generated = health.snapshot?.meta.generatedAt { valueRow("Data Changed", generated) }
                Button("Refresh Now") { Task { await health.refresh() } }.disabled(health.isSyncing)
                Link("Open Health Website", destination: URL(string: "https://health.avyayv.com")!)
            }
        }.healthListStyle().healthNavigationTitle("Health")
            .toolbar { if day == nil { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } } }
    }
    private func chartValue(_ n: HealthSnapshot.Night) -> Double? {
        switch metric { case "Awake": return n.awakeMinutes; case "HRV": return n.hrv; case "Readiness": return n.readiness; default: return n.totalSleepMinutes }
    }
    private func valueRow(_ title: String, _ value: String) -> some View {
        HStack { Text(title); Spacer(); Text(value).foregroundStyle(HealthStyle.secondaryInk).multilineTextAlignment(.trailing) }
    }
}

struct HabitsView: View {
    @ObservedObject var health: HealthStore
    @State private var newHabit = ""
    @State private var editing: HealthRecord?
    private let day = healthDay()

    var body: some View {
        NavigationStack {
            List {
                Section {
                    HStack {
                        TextField("Counter name", text: $newHabit)
                            .textInputAutocapitalization(.sentences)
                            .submitLabel(.done)
                            .onSubmit(addHabit)
                        Button("Add", action: addHabit)
                            .disabled(cleanName.isEmpty)
                    }
                } footer: {
                    Text("Create counters for anything you want to track. Each counter starts fresh every day.")
                }

                Section("Today") {
                    if health.habits.isEmpty {
                        ContentUnavailableView("No Counters Yet", systemImage: "plus.forwardslash.minus", description: Text("Add your first counter above."))
                    }
                    ForEach(health.habits) { habit in
                        let count = health.count(for: habit, on: day)
                        HStack(spacing: 14) {
                            Button { health.adjustCount(habit, on: day, by: -1) } label: {
                                Image(systemName: "minus.circle.fill").font(.title2)
                            }
                            .buttonStyle(.plain)
                            .disabled(count == 0)
                            .accessibilityLabel("Decrease \(habit.title)")

                            Button { editing = habit } label: {
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(habit.title).font(.body.weight(.medium))
                                    Text("Tap to rename or set an exact value")
                                        .font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                                }.frame(maxWidth: .infinity, alignment: .leading)
                            }.buttonStyle(.plain)

                            Text("\(count)")
                                .font(HealthStyle.metric).monospacedDigit()
                                .frame(minWidth: 42, alignment: .trailing)
                                .accessibilityLabel("\(count)")
                            Button { health.adjustCount(habit, on: day, by: 1) } label: {
                                Image(systemName: "plus.circle.fill").font(.title2)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("Increase \(habit.title)")
                        }
                        .padding(.vertical, 5)
                        .swipeActions {
                            Button("Delete", role: .destructive) { health.delete(habit) }
                            Button("Edit") { editing = habit }.tint(HealthStyle.ink)
                        }
                    }
                }
                if let error = health.storageError ?? health.syncError {
                    Section { Text(error).font(.caption).foregroundStyle(HealthStyle.secondaryInk) }
                }
            }
            .healthListStyle()
            .healthNavigationTitle("Counters")
            .refreshable { await health.refresh() }
            .sheet(item: $editing) { HabitEditor(health: health, habit: $0, day: day) }
        }.tint(HealthStyle.ink)
    }

    private var cleanName: String { newHabit.trimmingCharacters(in: .whitespacesAndNewlines) }
    private func addHabit() {
        guard !cleanName.isEmpty else { return }
        if health.save(HealthRecord(kind: "habit", day: day, title: cleanName)) {
            newHabit = ""
            Task { await health.refresh() }
        }
    }
}

struct HabitEditor: View {
    @ObservedObject var health: HealthStore
    let habit: HealthRecord
    let day: String
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var count: Int

    init(health: HealthStore, habit: HealthRecord, day: String) {
        self.health = health
        self.habit = habit
        self.day = day
        _name = State(initialValue: habit.title)
        _count = State(initialValue: health.count(for: habit, on: day))
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Counter") { TextField("Name", text: $name) }
                Section("Today's Value") {
                    Stepper("\(count)", value: $count, in: 0...999_999)
                    Button("Reset to Zero", role: .destructive) { count = 0 }
                        .disabled(count == 0)
                }
                Section {
                    Button("Delete Counter", role: .destructive) {
                        health.delete(habit)
                        dismiss()
                    }
                }
            }
            .healthListStyle()
            .healthNavigationTitle("Edit Counter")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        health.rename(habit, to: cleanName)
                        _ = health.setCount(habit, on: day, to: count)
                        dismiss()
                    }.disabled(cleanName.isEmpty)
                }
            }
        }.tint(HealthStyle.ink)
    }

    private var cleanName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }
}

struct LogView: View {
    @ObservedObject var health: HealthStore
    let kind: String
    @State private var selectedDate = Date()
    @State private var editing: HealthRecord?
    private var day: String { healthDay(selectedDate) }
    private var rows: [HealthRecord] { health.entries(kind, day: day) }
    private var recent: [HealthRecord] {
        var seen = Set<String>()
        return health.entries(kind).filter { seen.insert($0.title.lowercased()).inserted }.prefix(8).map { $0 }
    }
    var body: some View {
        NavigationStack {
            List {
                Section {
                    DatePicker("Day", selection: $selectedDate, in: ...Date(), displayedComponents: .date)
                    if kind == "food" {
                        HStack { Text(foodTotal(rows)).font(.title2.weight(.semibold)); Spacer(); Text(proteinTotal).foregroundStyle(HealthStyle.secondaryInk) }
                    } else {
                        HStack { Text("\(rows.count) Exercises").font(.title2.weight(.semibold)); Spacer(); Text("\(healthNumber(rows.compactMap(\.sets).reduce(0, +))) Sets").foregroundStyle(HealthStyle.secondaryInk) }
                    }
                }
                Section(kind == "food" ? "Meals" : "Workout") {
                    if rows.isEmpty { Text(kind == "food" ? "No meals logged." : "No exercises logged.").foregroundStyle(HealthStyle.secondaryInk) }
                    ForEach(rows) { item in
                        Button { editing = item } label: { HealthRecordRow(record: item) }.buttonStyle(.plain)
                            .swipeActions { Button("Delete", role: .destructive) { health.delete(item) } }
                    }
                    Button(kind == "food" ? "Log a Meal" : "Log an Exercise", systemImage: "plus") {
                        editing = HealthRecord(kind: kind, day: day, title: "", unit: kind == "lift" ? "lb" : nil)
                    }
                }
                if kind == "lift" {
                    Section("Body Weight") {
                        if let item = health.entries("bodyweight").first {
                            Button { editing = item } label: { HealthRecordRow(record: item) }.buttonStyle(.plain)
                        }
                        Button("Log Body Weight", systemImage: "plus") { editing = HealthRecord(kind: "bodyweight", day: day, title: "Body Weight", unit: "lb") }
                    }
                }
                if !recent.isEmpty {
                    Section(kind == "food" ? "Recent Meals · Tap to Log Again" : "Previous Lifts · Tap to Log Again") {
                        ForEach(recent) { item in
                            Button {
                                var copy = item; copy.id = UUID().uuidString; copy.day = day; copy.updatedAt = Date().timeIntervalSince1970
                                editing = copy
                            } label: { HealthRecordRow(record: item, showDay: true) }.buttonStyle(.plain)
                        }
                    }
                }
                if let error = health.storageError ?? health.syncError { Text(error).font(.caption).foregroundStyle(HealthStyle.secondaryInk) }
            }.healthListStyle().healthNavigationTitle(kind == "food" ? "Food" : "Weights")
                .refreshable { await health.refresh() }
                .sheet(item: $editing) { EntryEditor(health: health, record: $0) }
        }
    }
    private var proteinTotal: String {
        let known = rows.compactMap(\.protein)
        if known.isEmpty { return "Protein —" }
        return "\(healthNumber(known.reduce(0, +)))g Protein\(known.count < rows.count ? " + Uncounted" : "")"
    }
}

struct HealthRecordRow: View {
    let record: HealthRecord
    var showDay = false
    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack { Text(record.title).font(.body.weight(.medium)); Spacer(); if showDay { Text(record.day).font(.caption).foregroundStyle(HealthStyle.secondaryInk) } }
            Text(detail).font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
            if let notes = record.notes, !notes.isEmpty { Text(record.kind == "food" ? (FoodData.notes(record) ?? "") : notes).font(.caption).foregroundStyle(HealthStyle.secondaryInk).lineLimit(2) }
        }.padding(.vertical, 4)
    }
    private var detail: String {
        switch record.kind {
        case "lift": return "\(workoutSetDescription(record)) · \(healthNumber(record.sets)) sets"
        case "workout": return "\(Int((record.value ?? 0) / 60)) min · \(healthNumber(record.sets)) sets"
        case "bodyweight": return "\(healthNumber(record.value)) \(record.unit ?? "lb")"
        case "food": return "\(record.value.map { "\(healthNumber($0)) cal" } ?? "Calories Not Entered") · \(record.protein.map { "\(healthNumber($0))g Protein" } ?? "Protein —")"
        case "touch": return "\(healthNumber(record.value)) Confirmed"
        case "completion": return record.value == 1 ? "Completed" : (record.notes ?? "Not Completed")
        default: return record.day
        }
    }
}

struct EntryEditor: View {
    @ObservedObject var health: HealthStore
    @Environment(\.dismiss) private var dismiss
    @State var record: HealthRecord
    @State private var value = ""
    @State private var protein = ""
    @State private var sets = ""
    @State private var reps = ""
    @State private var date = Date()
    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(record.kind == "food" ? "Meal" : "Exercise", text: $record.title)
                    DatePicker("Day", selection: $date, in: ...Date(), displayedComponents: .date)
                }
                Section {
                    numberField(record.kind == "food" ? "Calories" : "Weight", text: $value)
                    if record.kind == "food" { numberField("Protein (g)", text: $protein) }
                    else { Picker("Unit", selection: Binding(get: { record.unit ?? "lb" }, set: { record.unit = $0 })) { Text("lb").tag("lb"); Text("kg").tag("kg") } }
                    if record.kind == "lift" { numberField("Sets", text: $sets); numberField("Reps", text: $reps) }
                } footer: { if record.kind == "food" { Text("Leave unknown amounts blank.") } }
                Section("Notes") { TextField("Details", text: Binding(get: { record.notes ?? "" }, set: { record.notes = $0 }), axis: .vertical).lineLimit(3...6) }
                if let error = health.storageError { Text(error).foregroundStyle(.red) }
            }.healthListStyle()
                .healthNavigationTitle(record.kind == "food" ? "Meal" : record.kind == "lift" ? "Exercise" : "Body Weight")
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                    ToolbarItem(placement: .confirmationAction) { Button("Save") {
                        record.title = record.title.trimmingCharacters(in: .whitespacesAndNewlines)
                        record.day = healthDay(date); record.value = number(value); record.protein = number(protein); record.sets = number(sets); record.reps = number(reps)
                        if health.save(record) { dismiss(); Task { await health.refresh() } }
                    }.disabled(!valid) }
                }.onAppear {
                    date = healthDate(record.day)
                    value = record.value.map { String($0) } ?? ""; protein = record.protein.map { String($0) } ?? ""
                    sets = record.sets.map { String($0) } ?? ""; reps = record.reps.map { String($0) } ?? ""
                }
        }.tint(HealthStyle.ink)
    }
    private func number(_ text: String) -> Double? { Double(text.replacingOccurrences(of: ",", with: ".")) }
    private var valid: Bool {
        !record.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && [value, protein, sets, reps].allSatisfy { $0.isEmpty || (number($0).map { $0.isFinite && $0 >= 0 && $0 < 1_000_000 } ?? false) } && (record.kind != "bodyweight" || (number(value) ?? 0) > 0)
    }
    private func numberField(_ label: String, text: Binding<String>) -> some View {
        HStack { Text(label); Spacer(); TextField("—", text: text).keyboardType(.decimalPad).multilineTextAlignment(.trailing).frame(width: 120) }
    }
}

struct HealthHistoryView: View {
    @ObservedObject var health: HealthStore
    @ObservedObject var recordings: RecordingStore
    @State private var date = Date()
    @State private var editing: HealthRecord?
    private var day: String { healthDay(date) }
    var body: some View {
        NavigationStack {
            List {
                Section { DatePicker("Day", selection: $date, in: ...Date(), displayedComponents: .date) }
                Section("Health") {
                    NavigationLink { HealthDetailView(health: health, day: day) } label: {
                        let night = health.snapshot?.allNights.last { $0.day == day }
                        HStack { Text("Sleep & Activity"); Spacer(); Text(healthDuration(night?.totalSleepMinutes)).foregroundStyle(HealthStyle.secondaryInk) }
                    }
                    NavigationLink("Trends") { HealthDetailView(health: health) }
                }
                Section("Habits") {
                    Text("\(HabitSummary.confirmedTouches(in: recordings.sessions, on: date)) Confirmed Hair / Face Touches")
                    NavigationLink("Review Check-Ins") { SessionHistory(recordings: recordings) }
                }
                Section("Daily Log") {
                    let rows = health.entries.filter { $0.day == day && $0.kind != "touch" }
                    if rows.isEmpty { Text("No entries for this day.").foregroundStyle(HealthStyle.secondaryInk) }
                    ForEach(rows) { item in
                        if item.kind == "food" {
                            NavigationLink { MealDetailView(health: health, meal: item) } label: { FoodMealRow(meal: item) }
                        } else if ["lift", "bodyweight"].contains(item.kind) {
                            Button { editing = item } label: { HealthRecordRow(record: item) }.buttonStyle(.plain)
                        } else { HealthRecordRow(record: item) }
                    }
                }
            }.healthListStyle().healthNavigationTitle("History")
                .refreshable { health.syncTouches(recordings.sessions); await health.refresh() }
                .sheet(item: $editing) { EntryEditor(health: health, record: $0) }
        }
    }
}
