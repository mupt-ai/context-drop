import SwiftUI
import UIKit

enum HealthStyle {
    static let ink = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 0.84, green: 0.90, blue: 0.85, alpha: 1)
            : UIColor(red: 0.16, green: 0.22, blue: 0.18, alpha: 1)
    })
    static let paper = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 0.07, green: 0.09, blue: 0.08, alpha: 1)
            : UIColor(red: 0.95, green: 0.95, blue: 0.90, alpha: 1)
    })
    static let surface = Color(uiColor: .secondarySystemGroupedBackground)
    static let secondaryInk = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 0.65, green: 0.70, blue: 0.66, alpha: 1)
            : UIColor(red: 0.36, green: 0.40, blue: 0.37, alpha: 1)
    })
    static let onAccent = paper
    static let subtleFill = ink.opacity(0.06)
    static let pageInset: CGFloat = 20
    static let sectionGap: CGFloat = 20
    static let cardInset: CGFloat = 20
    static let cardRadius: CGFloat = 22
    static let controlRadius: CGFloat = 14
    static let controlHeight: CGFloat = 48
    static let title = Font.system(.largeTitle, design: .rounded, weight: .semibold)
    static let heading = Font.system(.title2, design: .rounded, weight: .semibold)
    static let metric = Font.system(.title, design: .rounded, weight: .medium)
    static let heroMetric = Font.system(.largeTitle, design: .rounded, weight: .medium)
    static let navigationTitleFont = navigationFont(size: 17, textStyle: .headline)
    static let navigationTitle = Font.system(.headline, design: .rounded, weight: .semibold)

    @MainActor static func configureNavigation() {
        let navigationBar = UINavigationBar.appearance()
        navigationBar.titleTextAttributes = [
            .foregroundColor: UIColor(ink),
            .font: navigationTitleFont
        ]
        navigationBar.largeTitleTextAttributes = [
            .foregroundColor: UIColor(ink),
            .font: navigationFont(size: 34, textStyle: .largeTitle)
        ]
    }

    private static func navigationFont(size: CGFloat, textStyle: UIFont.TextStyle) -> UIFont {
        let font = UIFont.systemFont(ofSize: size, weight: .semibold)
        let descriptor = font.fontDescriptor.withDesign(.rounded) ?? font.fontDescriptor
        return UIFontMetrics(forTextStyle: textStyle).scaledFont(for: UIFont(descriptor: descriptor, size: size))
    }
}

struct HealthPrimaryButtonStyle: ButtonStyle {
    var fillsWidth = true
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.headline)
            .frame(maxWidth: fillsWidth ? .infinity : nil)
            .padding(.horizontal, 16)
            .frame(minHeight: HealthStyle.controlHeight)
            .foregroundStyle(HealthStyle.onAccent)
            .background(HealthStyle.ink, in: RoundedRectangle(cornerRadius: HealthStyle.controlRadius))
            .opacity(isEnabled ? (configuration.isPressed ? 0.75 : 1) : 0.4)
    }
}

struct HealthAdaptiveStack<Content: View>: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    var spacing: CGFloat = 24
    @ViewBuilder var content: () -> Content

    var body: some View {
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 12))
            : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: spacing))
        layout { content() }
    }
}

extension View {
    func healthNavigationTitle(_ title: String) -> some View {
        self.navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar(.visible, for: .navigationBar)
            .toolbarBackground(HealthStyle.paper, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(title).font(HealthStyle.navigationTitle)
                        .foregroundStyle(HealthStyle.ink)
                        .lineLimit(1).accessibilityAddTraits(.isHeader)
                }
            }
    }

    func healthCard(padding: CGFloat = HealthStyle.cardInset) -> some View {
        self.padding(padding)
            .background(HealthStyle.surface, in: RoundedRectangle(cornerRadius: HealthStyle.cardRadius))
    }

    func healthListStyle() -> some View {
        self.listStyle(.insetGrouped)
            .contentMargins(.horizontal, HealthStyle.pageInset, for: .scrollContent)
            .listSectionSpacing(HealthStyle.sectionGap)
            .scrollContentBackground(.hidden)
            .background(HealthStyle.paper)
            .foregroundStyle(HealthStyle.ink)
            .fontDesign(.rounded)
            .tint(HealthStyle.ink)
    }

    func healthTheme() -> some View {
        self.fontDesign(.rounded)
            .foregroundStyle(HealthStyle.ink)
            .tint(HealthStyle.ink)
    }
}
