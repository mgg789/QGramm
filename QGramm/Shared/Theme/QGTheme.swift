import SwiftUI
import UIKit

enum QGTheme {
    enum Palette {
        private static func color(light: UIColor, dark: UIColor) -> Color {
            Color(uiColor: UIColor { traits in
                traits.userInterfaceStyle == .dark ? dark : light
            })
        }

        static let background = color(
            light: UIColor(red: 0.93, green: 0.93, blue: 0.93, alpha: 1),
            dark: UIColor(red: 0.04, green: 0.04, blue: 0.05, alpha: 1)
        )
        static let screen = color(
            light: UIColor(red: 0.95, green: 0.95, blue: 0.95, alpha: 1),
            dark: UIColor(red: 0.05, green: 0.05, blue: 0.07, alpha: 1)
        )
        static let ink = color(
            light: UIColor(red: 0.07, green: 0.07, blue: 0.07, alpha: 1),
            dark: UIColor(red: 0.96, green: 0.96, blue: 0.98, alpha: 1)
        )
        static let muted = color(
            light: UIColor(red: 0.33, green: 0.33, blue: 0.33, alpha: 1),
            dark: UIColor(red: 0.76, green: 0.76, blue: 0.82, alpha: 1)
        )
        static let secondary = color(
            light: UIColor(red: 0.51, green: 0.51, blue: 0.51, alpha: 1),
            dark: UIColor(red: 0.64, green: 0.65, blue: 0.73, alpha: 1)
        )
        static let accent = Color(red: 0.51, green: 0.43, blue: 0.97)
        static let accentSoft = Color(red: 0.69, green: 0.46, blue: 0.91)
        static let accentBlue = Color(red: 0.56, green: 0.64, blue: 0.94)
        static let online = Color(red: 0.09, green: 0.76, blue: 0.48)
        static let dock = color(
            light: UIColor(red: 0.07, green: 0.07, blue: 0.07, alpha: 1),
            dark: UIColor(red: 0.02, green: 0.02, blue: 0.03, alpha: 1)
        )
        static let destructive = color(
            light: UIColor(red: 1.0, green: 0.49, blue: 0.49, alpha: 1),
            dark: UIColor(red: 1.0, green: 0.53, blue: 0.53, alpha: 1)
        )
        static let destructiveSurface = color(
            light: UIColor(red: 1.0, green: 0.77, blue: 0.77, alpha: 1),
            dark: UIColor(red: 0.31, green: 0.14, blue: 0.16, alpha: 1)
        )
        static let line = color(
            light: UIColor.black.withAlphaComponent(0.08),
            dark: UIColor.white.withAlphaComponent(0.12)
        )
        static let bubbleIncoming = color(
            light: UIColor.white.withAlphaComponent(0.78),
            dark: UIColor(red: 0.12, green: 0.12, blue: 0.15, alpha: 0.9)
        )
        static let bubbleOutgoing = color(
            light: UIColor(red: 0.56, green: 0.51, blue: 0.93, alpha: 1),
            dark: UIColor(red: 0.43, green: 0.36, blue: 0.84, alpha: 1)
        )
        static let bubbleOutgoingSoft = color(
            light: UIColor(red: 0.78, green: 0.74, blue: 0.98, alpha: 1),
            dark: UIColor(red: 0.28, green: 0.25, blue: 0.45, alpha: 1)
        )
        static let cardFill = color(
            light: UIColor.white.withAlphaComponent(0.72),
            dark: UIColor(red: 0.11, green: 0.11, blue: 0.14, alpha: 0.86)
        )
        static let searchFill = color(
            light: UIColor.white.withAlphaComponent(0.78),
            dark: UIColor(red: 0.14, green: 0.14, blue: 0.18, alpha: 0.92)
        )
        static let headerSurface = color(
            light: UIColor.white.withAlphaComponent(0.6),
            dark: UIColor(red: 0.08, green: 0.08, blue: 0.11, alpha: 0.92)
        )
    }

    static let wordmark = LinearGradient(
        colors: [Palette.accentSoft, Palette.accentBlue],
        startPoint: .leading,
        endPoint: .trailing
    )

    static let ambient = RadialGradient(
        colors: [
            Palette.accent.opacity(0.10),
            Palette.accentSoft.opacity(0.08),
            .clear
        ],
        center: .top,
        startRadius: 20,
        endRadius: 480
    )

    static let bottomGlow = RadialGradient(
        colors: [
            Palette.cardFill.opacity(0.85),
            Palette.accent.opacity(0.12),
            .clear
        ],
        center: .bottom,
        startRadius: 30,
        endRadius: 340
    )

    static let pagePadding: CGFloat = 24
    static let floatingBottomInset: CGFloat = 92
}

extension View {
    func qgScreenBackground() -> some View {
        modifier(QGScreenBackgroundModifier())
    }

    func qgCardStyle(cornerRadius: CGFloat = 28, fill: Color = QGTheme.Palette.cardFill) -> some View {
        background(
            RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
                .fill(fill)
                .overlay(
                    RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
                        .stroke(QGTheme.Palette.line, lineWidth: 1)
                )
        )
    }

    func qgPillBorder(selected: Bool) -> some View {
        overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(selected ? Color.clear : QGTheme.Palette.secondary.opacity(0.55), lineWidth: 1.5)
        )
    }
}

private struct QGScreenBackgroundModifier: ViewModifier {
    @AppStorage(QGHaptics.powerSavingKey) private var isPowerSavingEnabled = false

    func body(content: Content) -> some View {
        content.background {
            if isPowerSavingEnabled {
                QGTheme.Palette.screen.ignoresSafeArea()
            } else {
                ZStack {
                    QGTheme.Palette.screen.ignoresSafeArea()
                    QGTheme.ambient.ignoresSafeArea()
                    VStack {
                        Spacer()
                        QGTheme.bottomGlow
                            .frame(height: 320)
                            .ignoresSafeArea()
                    }
                }
            }
        }
    }
}

enum QGFormatters {
    static let messageTime: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "ru_RU")
        formatter.dateFormat = "HH:mm"
        return formatter
    }()

    static let dayTitle: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "ru_RU")
        formatter.dateFormat = "d MMMM"
        return formatter
    }()

    static let storage: ByteCountFormatter = {
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useKB, .useMB, .useGB]
        formatter.countStyle = .file
        return formatter
    }()
}
