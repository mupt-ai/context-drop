import SwiftUI
import UserNotifications

struct ActiveWorkoutView: View {
    @ObservedObject var workouts: WorkoutStore
    @ObservedObject var health: HealthStore
    let finished: (WorkoutHistoryItem) -> Void
    @State private var addingExercise = false
    @State private var confirmFinish = false
    @State private var confirmDiscard = false
    @State private var organizing = false
    @State private var renaming = false
    @State private var name = ""
    @FocusState private var focusedField: String?
    var body: some View {
        if let session = workouts.active {
            VStack(spacing: 0) {
                ScrollViewReader { proxy in
                    ScrollView {
                        LazyVStack(spacing: 16) {
                            Text(session.startedAt, style: .timer)
                                .font(.subheadline).monospacedDigit().foregroundStyle(HealthStyle.secondaryInk)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            HStack {
                                Text("\(session.completedSets) of \(session.plannedSets) sets").font(.subheadline).monospacedDigit()
                                Spacer()
                                Button("Exercises") { organizing = true }.font(.subheadline)
                            }
                            ProgressView(value: Double(session.completedSets), total: Double(max(1, session.plannedSets))).tint(workoutInk)
                            if session.exercises.isEmpty {
                                VStack(spacing: 16) {
                                    Image(systemName: "dumbbell").font(.system(size: 42))
                                    Text("Add Your First Exercise").font(.title3.weight(.semibold))
                                    Text("Choose a previous lift or add a new one.").font(.subheadline).foregroundStyle(.secondary)
                                }.frame(maxWidth: .infinity).padding(.vertical, 50)
                            }
                            ForEach(session.exercises) { exercise in
                                WorkoutExerciseCard(exercise: exercise, workouts: workouts, focusedField: $focusedField).id(exercise.id)
                            }
                            Button { addingExercise = true } label: {
                                Label("Add Exercise", systemImage: "plus").font(.headline).frame(maxWidth: .infinity).padding(18)
                                    .background(.white, in: RoundedRectangle(cornerRadius: 16))
                            }.buttonStyle(.plain)
                            if let error = workouts.error ?? health.storageError { Text(error).font(.caption).foregroundStyle(.red) }
                        }.padding(.horizontal, 20).padding(.top, 14).padding(.bottom, 22)
                    }.scrollDismissesKeyboard(.interactively)
                        .onChange(of: session.nextExerciseID) { _, id in
                            guard let id else { return }
                            withAnimation(.easeInOut(duration: 0.25)) { proxy.scrollTo(id, anchor: .top) }
                        }
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) { restBar(session) }
            .toolbar {
                workoutActions(session)
                ToolbarItemGroup(placement: .keyboard) { Spacer(); Button("Done") { focusedField = nil } }
            }
            .sheet(isPresented: $addingExercise) { ExercisePicker(health: health) { workouts.addExercise($0) } }
            .sheet(isPresented: $organizing) { OrganizeExercises(workouts: workouts) }
            .confirmationDialog("Finish Workout?", isPresented: $confirmFinish, titleVisibility: .visible) {
                Button("Save \(session.completedSets) Completed Sets") {
                    if let item = workouts.finish(health: health) {
                        WorkoutRestAlerts.cancel(); finished(item); Task { await health.refresh() }
                    }
                }
            } message: { Text("Only checked sets will be included in your workout history.") }
            .confirmationDialog("Discard This Workout?", isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button("Discard Workout", role: .destructive) { if workouts.discard() { WorkoutRestAlerts.cancel() } }
            } message: { Text("This removes the current session and its sets.") }
            .alert("Workout Name", isPresented: $renaming) {
                TextField("Name", text: $name)
                Button("Cancel", role: .cancel) {}
                Button("Save") { let title = name.trimmingCharacters(in: .whitespacesAndNewlines); if !title.isEmpty { workouts.update { $0.title = title } } }
            }
            .onChange(of: session.restUntil) { _, deadline in WorkoutRestAlerts.schedule(deadline) }
        }
    }
    @ToolbarContentBuilder private func workoutActions(_ session: WorkoutSession) -> some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            Menu {
                Button("Rename Workout", systemImage: "pencil") { name = session.title; renaming = true }
                Picker("Rest Between Sets", selection: Binding(get: { session.restSeconds }, set: { value in workouts.update { $0.restSeconds = value } })) {
                    Text("No Timer").tag(0); Text("30 Seconds").tag(30); Text("60 Seconds").tag(60); Text("90 Seconds").tag(90); Text("2 Minutes").tag(120); Text("3 Minutes").tag(180)
                }
                Button("Discard Workout", role: .destructive) { confirmDiscard = true }
            } label: { Label("Workout Options", systemImage: "ellipsis.circle") }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button("Finish") { focusedField = nil; confirmFinish = true }
                .disabled(session.completedSets == 0)
        }
    }
    @ViewBuilder private func restBar(_ session: WorkoutSession) -> some View {
        if let deadline = session.restUntil {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                let remaining = max(0, Int(ceil(deadline.timeIntervalSince(context.date))))
                HStack(spacing: 12) {
                    Image(systemName: remaining > 0 ? "timer" : "checkmark.circle").font(.title2)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(remaining > 0 ? "Rest" : "Rest Finished").font(.caption)
                        Text(String(format: "%d:%02d", remaining / 60, remaining % 60)).font(.title2.weight(.semibold)).monospacedDigit().contentTransition(.numericText())
                    }
                    Spacer()
                    if remaining > 0 {
                        Button("+30s") { workouts.update { $0.restUntil = deadline.addingTimeInterval(30) } }.font(.subheadline.weight(.semibold))
                    }
                    Button(remaining > 0 ? "Skip" : "Done") { workouts.update { $0.restUntil = nil } }.font(.subheadline.weight(.semibold)).padding(.leading, 6)
                }.padding(16).foregroundStyle(.white).background(workoutInk, in: RoundedRectangle(cornerRadius: 20))
            }.padding(.horizontal, 16).padding(.bottom, 8).background(workoutPaper)
        }
    }
}

struct WorkoutExerciseCard: View {
    let exercise: WorkoutExercise
    @ObservedObject var workouts: WorkoutStore
    var focusedField: FocusState<String?>.Binding
    @State private var showNotes = false
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(exercise.title).font(.title3.weight(.semibold))
                    Text(exercise.loadType == "assisted" ? "Assistance Weight" : exercise.loadType == "bodyweight" ? "Bodyweight" : exercise.unit.uppercased()).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Menu {
                    Picker("Load", selection: Binding(get: { exercise.loadType }, set: { value in workouts.editExercise(exercise.id) { $0.loadType = value } })) {
                        Text("Added Weight").tag("weight"); Text("Assistance").tag("assisted"); Text("Bodyweight").tag("bodyweight")
                    }
                    Picker("Unit", selection: Binding(get: { exercise.unit }, set: { value in workouts.editExercise(exercise.id) { $0.unit = value } })) {
                        Text("lb").tag("lb"); Text("kg").tag("kg")
                    }
                    Button(showNotes ? "Hide Notes" : "Exercise Notes") { showNotes.toggle() }
                } label: { Image(systemName: "ellipsis").frame(width: 32, height: 32) }.accessibilityLabel("\(exercise.title) Options")
            }
            HStack(spacing: 8) {
                Text("Set").frame(width: 28)
                Text("Last Time").frame(maxWidth: .infinity, alignment: .leading)
                Text(exercise.loadType == "bodyweight" ? "Load" : exercise.unit.uppercased()).frame(width: 63)
                Text("Reps").frame(width: 48)
                Image(systemName: "checkmark").frame(width: 40)
            }.font(.caption2.weight(.medium)).foregroundStyle(.secondary)
            ForEach(Array(exercise.sets.enumerated()), id: \.element.id) { index, set in
                WorkoutSetRow(set: set, index: index, exercise: exercise, workouts: workouts, focusedField: focusedField)
            }
            Button { workouts.addSet(exercise: exercise.id) } label: {
                Label("Add Set", systemImage: "plus").font(.subheadline.weight(.medium)).frame(maxWidth: .infinity).padding(.vertical, 10)
            }.buttonStyle(.plain).background(workoutInk.opacity(0.06), in: RoundedRectangle(cornerRadius: 12))
            if showNotes || !exercise.notes.isEmpty {
                TextField("Exercise Notes", text: Binding(get: { exercise.notes }, set: { value in workouts.editExercise(exercise.id) { $0.notes = value } }), axis: .vertical)
                    .font(.caption).foregroundStyle(.secondary).lineLimit(2...5).focused(focusedField, equals: exercise.id + "-notes")
            }
        }.padding(16).background(.white, in: RoundedRectangle(cornerRadius: 22))
    }
}

struct WorkoutSetRow: View {
    let set: WorkoutSet
    let index: Int
    let exercise: WorkoutExercise
    @ObservedObject var workouts: WorkoutStore
    var focusedField: FocusState<String?>.Binding
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        HStack(spacing: 8) {
            Menu {
                Button(set.warmup ? "Mark as Working Set" : "Mark as Warm-Up") { workouts.editSet(exercise: exercise.id, set: set.id) { $0.warmup.toggle() } }
                if !set.completed { Button("Remove Set", role: .destructive) { workouts.editExercise(exercise.id) { $0.sets.removeAll { $0.id == set.id } } } }
            } label: { Text(set.warmup ? "W" : "\(index + 1)").font(.caption.weight(.semibold)).frame(width: 28, height: 44) }
                .accessibilityLabel("Set \(index + 1) Options")
            Text(set.previous ?? "—").font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
            if exercise.loadType == "bodyweight" {
                Text("BW").font(.subheadline).frame(width: 63, height: 40)
            } else {
                TextField("—", text: Binding(get: { set.weight }, set: { value in workouts.editSet(exercise: exercise.id, set: set.id) { $0.weight = value } }))
                    .keyboardType(.decimalPad).multilineTextAlignment(.center).font(.subheadline).monospacedDigit()
                    .frame(width: 63, height: 40).background(workoutInk.opacity(0.055), in: RoundedRectangle(cornerRadius: 9))
                    .focused(focusedField, equals: set.id + "-weight").disabled(set.completed)
                    .accessibilityLabel("\(exercise.title), Set \(index + 1), Weight in \(exercise.unit)")
            }
            TextField("—", text: Binding(get: { set.reps }, set: { value in workouts.editSet(exercise: exercise.id, set: set.id) { $0.reps = value } }))
                .keyboardType(.numberPad).multilineTextAlignment(.center).font(.subheadline).monospacedDigit()
                .frame(width: 48, height: 40).background(workoutInk.opacity(0.055), in: RoundedRectangle(cornerRadius: 9))
                .focused(focusedField, equals: set.id + "-reps").disabled(set.completed)
                .accessibilityLabel("\(exercise.title), Set \(index + 1), Reps")
            Button {
                focusedField.wrappedValue = nil
                withAnimation(reduceMotion ? nil : .spring(response: 0.25)) {
                    if workouts.complete(exercise: exercise.id, set: set.id) { UIImpactFeedbackGenerator(style: .light).impactOccurred() }
                }
            } label: {
                Image(systemName: set.completed ? "checkmark.circle.fill" : "checkmark.circle")
                    .font(.system(size: 27)).foregroundStyle(set.completed ? workoutInk : workoutInk.opacity(0.28)).frame(width: 40, height: 44)
                    .scaleEffect(set.completed ? 1.04 : 1)
            }.buttonStyle(.plain).disabled(!set.completed && !set.isValid(loadType: exercise.loadType))
                .accessibilityLabel(set.completed ? "Undo Set \(index + 1)" : "Complete Set \(index + 1)")
        }.padding(.vertical, 1)
            .background(set.completed ? workoutInk.opacity(0.045) : .clear, in: RoundedRectangle(cornerRadius: 10))
    }
}

struct ExercisePicker: View {
    @ObservedObject var health: HealthStore
    let add: (WorkoutExercise) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var search = ""
    private var previous: [WorkoutExercise] {
        var seen = Set<String>()
        return workoutHistory(health.records).flatMap { $0.repeated() }.filter { seen.insert($0.title.lowercased()).inserted }
            .filter { search.isEmpty || $0.title.localizedCaseInsensitiveContains(search) }
    }
    private let common = ["Dumbbell Bench Press", "Barbell Bench Press", "Squat", "Romanian Deadlift", "Pull-Ups", "Lat Pulldown", "Seated Cable Row", "Dumbbell Row", "Shoulder Press", "Lateral Raise", "Biceps Curl", "Triceps Pushdown", "Leg Press", "Leg Curl", "Calf Raise"]
    var body: some View {
        NavigationStack {
            List {
                if !search.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Section { Button("Add “\(search)”", systemImage: "plus") { select(WorkoutExercise(title: search.trimmingCharacters(in: .whitespacesAndNewlines))) } }
                }
                if !previous.isEmpty {
                    Section("Your Exercises") {
                        ForEach(previous) { exercise in
                            Button { select(exercise) } label: {
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(exercise.title).font(.body.weight(.medium))
                                    if let last = exercise.sets.first?.previous { Text("Last: \(last) · \(exercise.sets.count) sets").font(.caption).foregroundStyle(.secondary) }
                                }.padding(.vertical, 4)
                            }.buttonStyle(.plain)
                        }
                    }
                }
                Section("Exercises") {
                    ForEach(common.filter { name in (search.isEmpty || name.localizedCaseInsensitiveContains(search)) && !previous.contains { $0.title.lowercased() == name.lowercased() } }, id: \.self) { name in
                        Button(name) { select(WorkoutExercise(title: name, loadType: name == "Pull-Ups" ? "bodyweight" : "weight")) }
                    }
                }
            }.scrollContentBackground(.hidden).background(workoutPaper).searchable(text: $search, prompt: "Find or Add an Exercise")
                .healthNavigationTitle("Add Exercise")
                .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
        }.tint(workoutInk)
    }
    private func select(_ exercise: WorkoutExercise) { add(exercise); dismiss() }
}

struct OrganizeExercises: View {
    @ObservedObject var workouts: WorkoutStore
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        NavigationStack {
            List {
                ForEach(workouts.active?.exercises ?? []) { exercise in
                    HStack { Text(exercise.title); Spacer(); Text("\(exercise.completedSets)/\(exercise.sets.count)").foregroundStyle(.secondary) }
                        .deleteDisabled(exercise.completedSets > 0)
                }
                .onMove { source, destination in workouts.update { $0.exercises.move(fromOffsets: source, toOffset: destination) } }
                .onDelete { offsets in workouts.update { session in
                    let removable = offsets.filter { session.exercises[$0].completedSets == 0 }
                    session.exercises.remove(atOffsets: IndexSet(removable))
                } }
            }.environment(\.editMode, .constant(.active)).scrollContentBackground(.hidden).background(workoutPaper)
                .healthNavigationTitle("Exercises")
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }.tint(workoutInk)
    }
}

@MainActor enum WorkoutRestAlerts {
    static let identifier = "workout-rest"
    static func requestPermission() { AlertCenter.shared.requestPermission { _ in } }
    static func cancel() {
        let center = UNUserNotificationCenter.current()
        center.removePendingNotificationRequests(withIdentifiers: [identifier])
        center.removeDeliveredNotifications(withIdentifiers: [identifier])
    }
    static func schedule(_ deadline: Date?) {
        cancel()
        guard let deadline, deadline > Date() else { return }
        let content = UNMutableNotificationContent()
        content.title = "Rest Finished"; content.body = "Time for your next set."; content.sound = .default
        let trigger = UNTimeIntervalNotificationTrigger(timeInterval: max(1, deadline.timeIntervalSinceNow), repeats: false)
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: identifier, content: content, trigger: trigger))
    }
}
