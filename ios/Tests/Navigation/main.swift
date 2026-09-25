import Foundation

let sources = URL(fileURLWithPath: CommandLine.arguments[1])
let style = try String(contentsOf: sources.appendingPathComponent("HealthStyle.swift"), encoding: .utf8)
precondition(style.contains("func healthNavigationTitle(_ title: String)"))
precondition(style.contains(".navigationBarTitleDisplayMode(.inline)"))
precondition(style.contains(".toolbar(.visible, for: .navigationBar)"))
precondition(style.contains(".toolbarBackground(HealthStyle.paper, for: .navigationBar)"))
precondition(style.contains(".toolbarBackground(.visible, for: .navigationBar)"))
precondition(style.contains("ToolbarItem(placement: .principal)"))
precondition(style.contains("Text(title).font(HealthStyle.navigationTitle)"))

let screens = ["HealthViews.swift", "ContextDropApp.swift", "WorkoutViews.swift", "ActiveWorkoutView.swift", "FoodViews.swift", "HealthGoalViews.swift"]
for screen in screens {
    let source = try String(contentsOf: sources.appendingPathComponent(screen), encoding: .utf8)
    precondition(source.contains(".healthNavigationTitle("), "Missing shared navigation: \(screen)")
    precondition(!source.contains(".navigationTitle("), "Bypassed shared navigation: \(screen)")
    precondition(!source.contains(".navigationBarTitleDisplayMode("), "Screen overrides title sizing: \(screen)")
    precondition(!source.contains(".toolbar(.hidden, for: .navigationBar)"), "Hidden native navigation: \(screen)")
}
let root = try String(contentsOf: sources.appendingPathComponent("HealthViews.swift"), encoding: .utf8)
precondition(root.contains(".tag(HealthTab.digest).tabItem"), "Keep the native Digest tab")
precondition(!root.contains("HealthTab.history"), "History tab is gone; no tab-bar More overflow")
precondition(root.contains(".healthNavigationTitle(\"Your Goals\")"))
precondition(root.contains(".refreshable { await health.refresh() }"), "Keep Today pull-to-refresh")
let workouts = try String(contentsOf: sources.appendingPathComponent("WorkoutViews.swift"), encoding: .utf8)
precondition(workouts.contains(".healthNavigationTitle(workouts.active?.title ?? \"Weights\")"))
let active = try String(contentsOf: sources.appendingPathComponent("ActiveWorkoutView.swift"), encoding: .utf8)
precondition(active.contains("ToolbarItem(placement: .topBarLeading)"))
precondition(active.contains("ToolbarItem(placement: .topBarTrailing)"))
precondition(active.contains("Button(\"Rename Workout\""))
precondition(active.contains(".disabled(session.completedSets == 0)"))
precondition(active.contains(".safeAreaInset(edge: .bottom, spacing: 0) { restBar(session) }"))
print("PASS: shared inline navigation, system bars/backgrounds, toolbar actions, native Digest tab without History/More overflow, refresh/rest behavior")
