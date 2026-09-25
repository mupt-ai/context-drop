import Foundation
let goal = HealthRecord(id: "target-protein", kind: "target", day: "2026-09-13", title: "Daily Protein", value: 140, unit: "g")
precondition(HealthGoal.record(.protein, in: [goal])?.value == 140)
var cleared = goal; cleared.deleted = true
precondition(HealthGoal.record(.protein, in: [cleared]) == nil)
precondition(HealthGoal.remaining(total: nil, target: 140) == nil)
precondition(HealthGoal.remaining(total: 70, target: 140) == 70)
precondition(HealthGoal.remaining(total: 160, target: 140) == 0)
let kilograms = HealthGoal.convertedWeight(150, from: "lb", to: "kg")
precondition(abs(HealthGoal.convertedWeight(kilograms, from: "kg", to: "lb") - 150) < 1e-8)
print("PASS: shared target identity, removed targets, missing nutrition, over-goal progress and unit conversion")

let sugar = HealthRecord(id: "target-added-sugar", kind: "target", day: "2026-09-14", title: "Daily Added Sugar Limit", value: 36, unit: "g")
precondition(HealthGoal.record(.addedSugar, in: [sugar])?.value == 36)
var snack = HealthRecord(id: "snack", kind: "food", day: "2026-09-14", title: "Snack")
precondition(snack.added_sugar == nil)
snack.added_sugar = 0
let roundTrip = try JSONDecoder().decode(HealthRecord.self, from: JSONEncoder().encode(snack))
precondition(roundTrip.added_sugar == 0)
