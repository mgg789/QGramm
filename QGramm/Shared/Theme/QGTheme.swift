import SwiftUI

enum QGTheme {
    enum Palette {
        static let background = Color(red: 0.93, green: 0.93, blue: 0.93)
        static let screen = Color(red: 0.95, green: 0.95, blue: 0.95)
        static let ink = Color(red: 0.07, green: 0.07, blue: 0.07)
        static let muted = Color(red: 0.33, green: 0.33, blue: 0.33)
        static let secondary = Color(red: 0.51, green: 0.51, blue: 0.51)
        static let accent = Color(red: 0.51, green: 0.43, blue: 0.97)
        static let accentSoft = Color(red: 0.69, green: 0.46, blue: 0.91)
        static let accentBlue = Color(red: 0.56, green: 0.64, blue: 0.94)
        static let online = Color(red: 0.09, green: 0.76, blue: 0.48)
        static let dock = Color(red: 0.07, green: 0.07, blue: 0.07)
        static let destructive = Color(red: 1.0, green: 0.49, blue: 0.49)
        static let destructiveSurface = Color(red: 1.0, green: 0.77, blue: 0.77)
        static let line = Color.black.opacity(0.08)
        static let bubbleIncoming = Color.white.opacity(0.78)
        static let bubbleOutgoing = Color(red: 0.56, green: 0.51, blue: 0.93)
        static let bubbleOutgoingSoft = Color(red: 0.78, green: 0.74, blue: 0.98)
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
            .white.opacity(0.85),
            Palette.accent.opacity(0.12),
            .clear
        ],
        center: .bottom,
        startRadius: 30,
        endRadius: 340
    )

    static let pagePadding: CGFloat = 24
    static let floatingBottomInset: CGFloat = 118
}

extension View {
    func qgScreenBackground() -> some View {
        background {
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

    func qgCardStyle(cornerRadius: CGFloat = 28, fill: Color = .white.opacity(0.72)) -> some View {
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
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(selected ? Color.clear : QGTheme.Palette.secondary.opacity(0.55), lineWidth: 1.5)
        )
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
