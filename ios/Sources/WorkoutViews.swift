import SwiftUI

struct WorkoutsView: View {
    @ObservedObject var health: HealthStore
    @ObservedObject var workouts: WorkoutStore
    @State private var starting = false
    @State private var historyOpen = false
    @State private var summary: WorkoutHistoryItem?
    @State private var finishedID: String?
    @State private var animationTitle: String?
    @State private var bodyWeight: HealthRecord?
    @State private var bodyGoalsOpen = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    private var history: [WorkoutHistoryItem] { workoutHistory(health.records) }
    var body: some View {
        NavigationStack {
            ZStack {
                HealthStyle.paper.ignoresSafeArea()
                if workouts.active != nil {
                    ActiveWorkoutView(workouts: workouts, health: health) { item in finishedID = item.id; summary = item }
                } else {
                    home
                }
                if let title = animationTitle {
                    WorkoutStartAnimation(title: title)
                        .transition(.opacity).zIndex(2)
                        .task {
                            try? await Task.sleep(for: .milliseconds(reduceMotion ? 250 : 1000))
                            withAnimation(.easeOut(duration: 0.2)) { animationTitle = nil }
                        }
                }
            }.foregroundStyle(HealthStyle.ink).healthNavigationTitle(workouts.active?.title ?? "Weights")
                .toolbar {
                    if workouts.active == nil {
                        ToolbarItem(placement: .topBarTrailing) {
                            Button("Workout History", systemImage: "clock.arrow.circlepath") { historyOpen = true }
                        }
                    }
                }
                .sheet(isPresented: $starting) {
                    StartWorkoutSheet(history: history) { title, exercises in
                        guard workouts.start(title: title, exercises: exercises) else { return false }
                        starting = false
                        withAnimation(reduceMotion ? nil : .easeInOut(duration: 0.2)) { animationTitle = title }
                        WorkoutRestAlerts.requestPermission()
                        return true
                    }
                }
                .sheet(isPresented: $historyOpen) { NavigationStack { WorkoutHistoryList(health: health) } }
                .sheet(item: $summary) { item in
                    NavigationStack { WorkoutSummaryView(item: item, isJustFinished: item.id == finishedID) }
                }
                .sheet(isPresented: $bodyGoalsOpen) { BodyWeightGoalsView(health: health) }
                .sheet(item: $bodyWeight) { EntryEditor(health: health, record: $0) }
        }.tint(HealthStyle.ink)
    }
    private var home: some View {
        GeometryReader { geometry in
            let compact = geometry.size.height < 650
            ScrollView {
                VStack(alignment: .leading, spacing: compact ? 14 : HealthStyle.sectionGap) {
                    HealthAdaptiveStack {
                        let week = history.filter { Calendar.current.isDate(healthDate($0.day), equalTo: Date(), toGranularity: .weekOfYear) }
                        workoutStat("This Week", "\(week.count) \(week.count == 1 ? "Workout" : "Workouts")")
                        workoutStat("Completed", "\(week.reduce(0) { $0 + $1.completedSets }) Sets")
                    }
                VStack(alignment: .leading, spacing: compact ? 14 : 22) {
                    HStack {
                        Image(systemName: "dumbbell.fill").font(.system(size: 34, weight: .medium))
                        Spacer()
                        Text("WORKOUT").font(.system(.caption2, design: .monospaced).weight(.medium)).tracking(2)
                    }
                    Text("Start a Session").font(.system(size: 28, weight: .semibold, design: .rounded))
                    Button { starting = true } label: {
                        HStack { Text("Start Workout"); Spacer(); Image(systemName: "arrow.up.right") }.font(.headline).padding(16).foregroundStyle(HealthStyle.onAccent).background(HealthStyle.ink, in: RoundedRectangle(cornerRadius: 16))
                    }.buttonStyle(.plain)
                }.padding(22).background(HealthStyle.surface, in: RoundedRectangle(cornerRadius: 24))
                HStack { Text("Recent Workouts").font(.headline); Spacer(); Button("See All") { historyOpen = true }.font(.caption) }
                VStack(spacing: 0) {
                    if history.isEmpty { Text("Your finished workouts will appear here.").font(.subheadline).foregroundStyle(HealthStyle.secondaryInk).padding(18).frame(maxWidth: .infinity, alignment: .leading) }
                    ForEach(Array(history.prefix(compact ? 2 : 3).enumerated()), id: \.element.id) { index, item in
                        if index > 0 { Divider().padding(.horizontal, 16) }
                        Button { summary = item } label: {
                            HStack {
                                VStack(alignment: .leading, spacing: 4) { Text(item.title).font(.subheadline.weight(.semibold)); Text("\(item.completedSets) sets · \(item.exerciseNames.count) exercises").font(.caption).foregroundStyle(HealthStyle.secondaryInk) }
                                Spacer()
                                Text(healthDate(item.day), format: .dateTime.month(.abbreviated).day()).font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                                Image(systemName: "chevron.right").font(.caption2)
                            }.padding(16)
                        }.buttonStyle(.plain)
                    }
                }.background(HealthStyle.surface, in: RoundedRectangle(cornerRadius: 20))
                Spacer(minLength: 0)
                HStack {
                    Button { bodyGoalsOpen = true } label: {
                        HStack {
                            Text("Body Weight").font(.subheadline)
                            if let weight = health.entries("bodyweight").first {
                                Text("\(healthNumber(weight.value)) \(weight.unit ?? "lb")").font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
                            }
                            Image(systemName: "chevron.right").font(.caption)
                        }
                    }.buttonStyle(.plain)
                    Spacer()
                    Button { bodyWeight = HealthRecord(kind: "bodyweight", day: healthDay(), title: "Body Weight", unit: "lb") } label: { Image(systemName: "plus.circle").font(.title3).frame(width: 40, height: 40) }.accessibilityLabel("Log Body Weight")
                }
                if let error = workouts.error ?? health.storageError { Text(error).font(.caption).foregroundStyle(.red).lineLimit(2) }
                }.padding(.horizontal, HealthStyle.pageInset).padding(.vertical, 12)
            }
        }
    }
}

func workoutStat(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 5) { Text(value).font(.headline).monospacedDigit(); Text(title).font(.caption).foregroundStyle(HealthStyle.secondaryInk) }
}

struct WorkoutStartAnimation: View {
    let title: String
    @State private var appeared = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        VStack(spacing: 24) {
            ZStack {
                Circle().stroke(HealthStyle.ink.opacity(0.1), lineWidth: 3)
                Circle().trim(from: 0, to: appeared ? 1 : 0).stroke(HealthStyle.ink, style: StrokeStyle(lineWidth: 3, lineCap: .round)).rotationEffect(.degrees(-90))
                Image(systemName: "dumbbell.fill").font(.system(size: 52)).rotationEffect(.degrees(appeared ? 0 : -25)).scaleEffect(appeared ? 1 : 0.65)
            }.frame(width: 132, height: 132)
            Text(title).font(.system(size: 30, weight: .semibold, design: .rounded))
            Text("Session Started").font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
        }.frame(maxWidth: .infinity, maxHeight: .infinity).background(HealthStyle.paper)
            .onAppear { withAnimation(reduceMotion ? nil : .spring(response: 0.65, dampingFraction: 0.65)) { appeared = true } }
            .accessibilityElement(children: .combine)
    }
}

struct StartWorkoutSheet: View {
    let history: [WorkoutHistoryItem]
    let start: (String, [WorkoutExercise]) -> Bool
    @Environment(\.dismiss) private var dismiss
    @State private var name = "Workout"
    @State private var error: String?
    private var templates: [WorkoutHistoryItem] {
        var seen = Set<String>()
        return history.filter { seen.insert($0.title.lowercased()).inserted }.prefix(6).map { $0 }
    }
    var body: some View {
        NavigationStack {
            List {
                Section("New Session") {
                    TextField("Workout Name", text: $name)
                    Button("Start Empty Workout", systemImage: "plus") { begin(name, []) }.disabled(name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
                if !templates.isEmpty {
                    Section("Repeat a Workout") {
                        ForEach(templates) { item in
                            Button { begin(item.title, item.repeated()) } label: {
                                VStack(alignment: .leading, spacing: 5) {
                                    HStack { Text(item.title).font(.headline); Spacer(); Image(systemName: "arrow.clockwise") }
                                    Text(item.exerciseNames.joined(separator: " · ")).font(.caption).foregroundStyle(HealthStyle.secondaryInk).lineLimit(3)
                                    Text("Last: \(item.day) · \(item.completedSets) sets").font(.caption2).foregroundStyle(HealthStyle.secondaryInk)
                                }.padding(.vertical, 6)
                            }.buttonStyle(.plain)
                        }
                    }
                }
                if let error { Text(error).foregroundStyle(.red) }
            }.healthListStyle().healthNavigationTitle("Start Workout")
                .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }.tint(HealthStyle.ink)
    }
    private func begin(_ title: String, _ exercises: [WorkoutExercise]) {
        if !start(title.trimmingCharacters(in: .whitespacesAndNewlines), exercises) { error = "Couldn’t start the workout. Please try again." }
    }
}

struct WorkoutHistoryList: View {
    @ObservedObject var health: HealthStore
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        List {
            ForEach(workoutHistory(health.records)) { item in
                NavigationLink { WorkoutSummaryView(item: item) } label: {
                    VStack(alignment: .leading, spacing: 5) {
                        Text(item.title).font(.headline)
                        Text("\(item.day) · \(item.completedSets) sets · \(item.exerciseNames.count) exercises").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                    }.padding(.vertical, 5)
                }
            }
        }.healthListStyle().healthNavigationTitle("Workouts")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
    }
}

struct WorkoutSummaryView: View {
    let item: WorkoutHistoryItem
    var isJustFinished = false
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        List {
            Section {
                VStack(alignment: .leading, spacing: 18) {
                    if isJustFinished { Image(systemName: "checkmark.circle.fill").font(.system(size: 42)).foregroundStyle(HealthStyle.ink) }
                    Text(isJustFinished ? "Workout Saved" : item.title).font(.system(size: 28, weight: .semibold, design: .rounded))
                    Text(item.day).foregroundStyle(HealthStyle.secondaryInk)
                    HStack(spacing: 24) {
                        workoutStat("Sets", "\(item.completedSets)")
                        workoutStat("Exercises", "\(item.exerciseNames.count)")
                        if let duration = item.duration { workoutStat("Duration", "\(max(1, Int(duration / 60))) min") }
                    }
                }.padding(.vertical, 10)
            }
            ForEach(item.exerciseNames, id: \.self) { title in
                Section(title) {
                    ForEach(item.lifts.filter { $0.title == title }) { record in
                        HStack {
                            Text(record.warmup == true ? "Warm-Up" : "\(Int(record.sets ?? 1)) \(record.sets == 1 ? "Set" : "Sets")").foregroundStyle(HealthStyle.secondaryInk)
                            Spacer(); Text(workoutSetDescription(record)).monospacedDigit()
                        }
                    }
                }
            }
        }.healthListStyle().healthNavigationTitle(isJustFinished ? "Finished" : "Workout")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
    }
}
