import Foundation
import UserNotifications
import UIKit

final class AlertCenter: NSObject, UNUserNotificationCenterDelegate {
    static let shared = AlertCenter()

    func requestPermission(completion: @escaping (Bool) -> Void) {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.requestAuthorization(options: [.alert, .sound]) { allowed, _ in
            DispatchQueue.main.async { completion(allowed) }
        }
    }

    func notify(test: Bool = false) {
        let center = UNUserNotificationCenter.current()
        center.removeDeliveredNotifications(withIdentifiers: ["face-touch"])
        let content = UNMutableNotificationContent()
        content.title = test ? "Your reminder sounds like this" : "Hands down"
        content.body = test ? "Face Guard is ready to give you a gentle nudge." : "You may be touching your hair or face."
        content.sound = .default
        center.add(UNNotificationRequest(identifier: "face-touch", content: content, trigger: nil))
        if UIApplication.shared.applicationState == .active {
            UINotificationFeedbackGenerator().notificationOccurred(.warning)
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }
}
