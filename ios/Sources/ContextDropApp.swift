import SwiftUI

@main
struct ContextDropApp: App {
    init() { HealthStyle.configureNavigation() }
    @StateObject private var monitor = RingMonitor()
    @StateObject private var health = HealthStore()
    @Environment(\.scenePhase) private var scenePhase
    var body: some Scene {
        WindowGroup {
            HealthRootView(monitor: monitor, recordings: monitor.recordings, health: health)
                .healthTheme()
                .onChange(of: scenePhase) { _, phase in
                    if phase == .active { monitor.loadSetup() }
                }
        }
    }
}

struct HomeView: View {
    @ObservedObject var health: HealthStore
    @State private var showHabits = false
    @ObservedObject var monitor: RingMonitor
    @ObservedObject var recordings: RecordingStore

    private var latest: TouchCandidate? { recordings.active?.candidates.first }
    var body: some View {
        NavigationStack {
            GeometryReader { geometry in
                let compact = geometry.size.height < 700
                ScrollView {
                    VStack(alignment: .leading, spacing: compact ? 14 : HealthStyle.sectionGap) {
                        if recordings.isRecording {
                            Text(monitor.isReceiving ? "Tracking" : monitor.status)
                                .font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                        }
                        if recordings.isRecording && !monitor.isReceiving {
                            Text(monitor.detail).font(.caption).foregroundStyle(HealthStyle.secondaryInk).lineLimit(3)
                        }
                        Spacer(minLength: 0)
                        VStack(alignment: .leading, spacing: compact ? 14 : HealthStyle.sectionGap) {
                            if let item = latest, let active = recordings.active {
                                if item.label == .unreviewed {
                                    Text("Looks like you touched your hair or face.")
                                        .font(HealthStyle.title)
                                        .fixedSize(horizontal: false, vertical: true)
                                    Text("Did I get that right?").font(.body).foregroundStyle(HealthStyle.secondaryInk)
                                    CheckInAnswers(selected: item.label) {
                                        recordings.label(sessionID: active.id, candidateID: item.id, as: $0)
                                    }
                                } else {
                                    Image(systemName: item.label == .touch ? "hand.wave" : "checkmark.circle").font(.system(size: 36))
                                    Text(item.label == .touch ? "Touch confirmed." : "Not a touch.")
                                        .font(HealthStyle.title)
                                    Button("Undo My Answer") {
                                        recordings.label(sessionID: active.id, candidateID: item.id, as: .unreviewed)
                                    }.font(.subheadline).frame(minHeight: 44)
                                }
                            } else {
                                Image(systemName: "hand.raised").font(.system(size: 40))
                                Text(recordings.isRecording ? "No check-ins yet." : "Hair & Face")
                                    .font(HealthStyle.title)
                                Text(recordings.isRecording ? "I’ll ask when it looks like you touched your hair or face." : "Start tracking to get reminders.")
                                    .font(.subheadline).foregroundStyle(HealthStyle.secondaryInk)
                            }
                        }.frame(maxWidth: .infinity, alignment: .leading).healthCard()
                        Spacer(minLength: 0)
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text("\(HabitSummary.confirmedTouches(in: recordings.sessions, on: Date()))")
                                .font(HealthStyle.metric)
                            Text("confirmed today").font(.subheadline)
                            Spacer()
                        }
                        if let error = recordings.errorMessage { Text(error).font(.caption).foregroundStyle(.red).lineLimit(2) }
                        VStack(spacing: 8) {
                            Button {
                                if recordings.isRecording { monitor.endRecording() } else { monitor.startRecording() }
                            } label: {
                                Text(recordings.isRecording ? "Pause" : "Start")
                            }.buttonStyle(HealthPrimaryButtonStyle()).disabled(!monitor.isConfigured)
                            if recordings.isRecording {
                                Button("I Just Touched My Hair or Face", systemImage: "plus.circle") {
                                    recordings.candidate(probability: nil, manual: true)
                                }.font(.subheadline).frame(minHeight: 44).disabled(!monitor.isReceiving)
                            }
                        }
                    }.padding(.horizontal, HealthStyle.pageInset).padding(.vertical, 12)
                        .frame(minHeight: geometry.size.height)
                }.scrollBounceBehavior(.basedOnSize)
            }.background(HealthStyle.paper).foregroundStyle(HealthStyle.ink).tint(HealthStyle.ink)
                .healthNavigationTitle("Habits")
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) {
                        Menu {
                            Button("Goals") { showHabits = true }
                            Button("Test Alert in 5 Seconds", systemImage: "bell") { monitor.testAlert() }
                            Button("Notification Settings", systemImage: "gearshape") {
                                if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
                            }
                        } label: {
                            Label("Habit Options", systemImage: "ellipsis.circle")
                        }
                    }
                }
                .sheet(isPresented: $showHabits) { HabitsView(health: health) }
        }
    }
}

struct HabitProgress: View {
    @ObservedObject var recordings: RecordingStore
    private var days: [Date] {
        let calendar = Calendar.current
        let today = calendar.startOfDay(for: Date())
        return (-6...0).compactMap { calendar.date(byAdding: .day, value: $0, to: today) }
    }
    private var counts: [Int] { days.map { HabitSummary.confirmedTouches(in: recordings.sessions, on: $0) } }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("\(counts.last ?? 0)").font(HealthStyle.metric)
                VStack(alignment: .leading, spacing: 4) {
                    Text("confirmed today").font(.headline)
                }
            }
            Text("Your Week").font(.subheadline.weight(.semibold))
            HStack(alignment: .bottom, spacing: 12) {
                ForEach(Array(days.enumerated()), id: \.offset) { index, day in
                    VStack(spacing: 8) {
                        Text("\(counts[index])").font(.caption2).foregroundStyle(HealthStyle.secondaryInk)
                        RoundedRectangle(cornerRadius: 5)
                            .fill(HealthStyle.ink.opacity(index == 6 ? 1 : 0.28))
                            .frame(height: counts[index] == 0 ? 3 : max(8, 52 * CGFloat(counts[index]) / CGFloat(max(counts.max() ?? 1, 1))))
                            .frame(height: 52, alignment: .bottom)
                        Text(day, format: .dateTime.weekday(.narrow)).font(.caption2)
                    }.frame(maxWidth: .infinity)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel("\(day.formatted(date: .abbreviated, time: .omitted)): \(counts[index]) confirmed touches")
                }
            }
            Text("Only touches you confirm are counted.").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
        }.healthCard()
    }
}

struct CheckInAnswers: View {
    let selected: TouchLabel
    let answer: (TouchLabel) -> Void
    var body: some View {
        VStack(spacing: 10) {
            answerButton("Yes, I Did", value: .touch)
            answerButton("No, You’re Wrong", value: .notTouch)
        }
    }
    private func answerButton(_ title: String, value: TouchLabel) -> some View {
        Button { answer(value) } label: {
            HStack {
                Text(title).font(.headline)
                Spacer()
                if selected == value { Image(systemName: "checkmark") }
            }.padding(16)
                .foregroundStyle(selected == value ? HealthStyle.onAccent : HealthStyle.ink)
                .background(selected == value ? HealthStyle.ink : HealthStyle.subtleFill, in: RoundedRectangle(cornerRadius: HealthStyle.controlRadius))
        }.buttonStyle(.plain)
    }
}

struct SessionHistory: View {
    @ObservedObject var recordings: RecordingStore
    var body: some View {
        List {
            Section { HabitProgress(recordings: recordings).listRowInsets(EdgeInsets()).listRowBackground(Color.clear) }
            if recordings.sessions.isEmpty { Text("Your check-ins will appear here.").foregroundStyle(HealthStyle.secondaryInk) }
            ForEach(recordings.sessions) { session in
                NavigationLink { SessionReview(recordings: recordings, sessionID: session.id) } label: {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(session.startedAt, format: .dateTime.month(.abbreviated).day().hour().minute())
                        Text("\(session.candidates.count) check-ins · \(session.reviewed) answered").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                    }.padding(.vertical, 6)
                }
            }
            Section {
                Text("Motion and your answers are saved on this phone while reminders are on. Answers help tune future check-ins.").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
            }
        }.healthListStyle()
            .healthNavigationTitle("History")
    }
}

struct CandidateRow: View {
    let item: TouchCandidate
    let start: Date
    let label: (TouchLabel) -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("+\(Int(item.time.timeIntervalSince(start)))s").monospacedDigit().font(.headline)
                Spacer()
                Text(item.source == "manual" ? "You told me" : "Check-in").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
            }
            Text("Looks like you touched your hair or face. Did I get that right?").font(.body)
            CheckInAnswers(selected: item.label, answer: label)
            HStack {
                Text(item.label == .touch ? "You said yes" : item.label == .notTouch ? "You said no" : "Not answered yet").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                Spacer()
                if item.label != .unreviewed { Button("Undo") { label(.unreviewed) }.font(.caption) }
            }
        }.healthCard()
    }

}

struct SessionReview: View {
    @ObservedObject var recordings: RecordingStore
    let sessionID: UUID
    var body: some View {
        ScrollView {
            if let session = recordings.sessions.first(where: { $0.id == sessionID }) {
                LazyVStack(alignment: .leading, spacing: 16) {
                    Text("\(session.reviewed) of \(session.candidates.count) check-ins answered").font(.subheadline)
                    Text("Only answer the moments you remember. It’s fine to skip the rest.").font(.caption).foregroundStyle(HealthStyle.secondaryInk)
                    DisclosureGroup("Recording Details") {
                        Text("\(session.samples) motion samples · \(session.state.capitalized)").font(.caption)
                    }
                    ForEach(session.candidates) { item in
                        CandidateRow(item: item, start: session.startedAt) {
                            recordings.label(sessionID: sessionID, candidateID: item.id, as: $0)
                        }
                    }
                }.padding(24)
            }
        }.background(HealthStyle.paper).healthNavigationTitle("Check-Ins")
    }
}
