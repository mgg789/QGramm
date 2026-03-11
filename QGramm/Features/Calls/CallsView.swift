import SwiftUI
import UIKit

struct CallsView: View {
    @EnvironmentObject private var store: AppStore
    @State private var selectedCall: CallRecord?

    private var groupedCalls: [(String, [CallRecord])] {
        let today = Calendar.current.startOfDay(for: .now)
        let yesterday = Calendar.current.date(byAdding: .day, value: -1, to: today) ?? today

        let todayCalls = store.state.calls.filter { Calendar.current.isDate($0.startedAt, inSameDayAs: today) }
        let yesterdayCalls = store.state.calls.filter { Calendar.current.isDate($0.startedAt, inSameDayAs: yesterday) }
        let olderCalls = store.state.calls.filter {
            !Calendar.current.isDate($0.startedAt, inSameDayAs: today) &&
            !Calendar.current.isDate($0.startedAt, inSameDayAs: yesterday)
        }

        return [
            (language.text(ru: "Сегодня, \(QGFormatters.dayTitle.string(from: today))", en: "Today, \(QGFormatters.dayTitle.string(from: today))"), todayCalls),
            (language.text(ru: "Вчера, \(QGFormatters.dayTitle.string(from: yesterday))", en: "Yesterday, \(QGFormatters.dayTitle.string(from: yesterday))"), yesterdayCalls),
            (language.text(ru: "Ранее", en: "Earlier"), olderCalls)
        ].filter { !$0.1.isEmpty }
    }

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 24) {
                WordmarkView(title: "QGramm", size: 38)
                    .frame(maxWidth: .infinity)

                if groupedCalls.isEmpty {
                    EmptyStateView(
                        title: language.text(ru: "Пока нет звонков", en: "No calls yet"),
                        message: language.text(
                            ru: "Аудио-вызовы V1 появятся здесь после первого соединения.",
                            en: "V1 audio calls will appear here after your first connection."
                        ),
                        systemImage: "phone.connection.fill"
                    )
                } else {
                    ForEach(groupedCalls, id: \.0) { title, calls in
                        VStack(alignment: .leading, spacing: 18) {
                            Text(title)
                                .font(.system(size: 22, weight: .bold, design: .rounded))
                                .foregroundStyle(QGTheme.Palette.ink)

                            ForEach(calls) { call in
                                Button {
                                    selectedCall = call
                                } label: {
                                    CallRowView(call: call, peer: store.user(id: call.peerUserID))
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }

                Button {
                    store.clearCallHistory()
                } label: {
                    Text(language.text(ru: "Очистить историю", en: "Clear history"))
                        .font(.system(size: 18, weight: .bold, design: .rounded))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 18)
                        .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.destructiveSurface))
                        .foregroundStyle(QGTheme.Palette.destructive)
                }
                .buttonStyle(.plain)
                .padding(.top, 24)
            }
            .padding(.horizontal, QGTheme.pagePadding)
            .padding(.top, 10)
            .padding(.bottom, QGTheme.floatingBottomInset)
        }
        .qgScreenBackground()
        .confirmationDialog(language.text(ru: "Вы хотите позвонить?", en: "Do you want to call?"), isPresented: Binding(
            get: { selectedCall != nil },
            set: { if !$0 { selectedCall = nil } }
        ), presenting: selectedCall) { call in
            Button(language.text(ru: "Аудио", en: "Audio")) {
                store.startCall(with: call.peerUserID, kind: .audio)
            }
            Button(language.text(ru: "Отмена", en: "Cancel"), role: .cancel) {}
        } message: { call in
            Text(call.title)
        }
        .task {
            await store.refreshCallsFromServer()
        }
    }

    private var language: AppLanguage {
        store.state.session.language
    }
}

private struct CallRowView: View {
    let call: CallRecord
    let peer: UserProfile?

    var body: some View {
        HStack(spacing: 14) {
            QGAvatarView(user: peer, size: 48)

            VStack(alignment: .leading, spacing: 4) {
                Text(call.title)
                    .font(.system(size: 18, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.ink)
                HStack(spacing: 6) {
                    Image(systemName: "clock")
                    Text(QGFormatters.messageTime.string(from: call.startedAt))
                }
                .font(.system(size: 13, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.secondary)
            }

            Spacer()

            Image(systemName: symbol)
                .font(.system(size: 22, weight: .bold))
                .foregroundStyle(color)
        }
        .padding(16)
        .qgCardStyle(cornerRadius: 24)
    }

    private var symbol: String {
        switch call.kind {
        case .audio:
            return call.direction == .outgoing ? "phone.arrow.up.right" : "phone.arrow.down.left"
        case .video:
            return "video.fill"
        case .group:
            return "person.3.fill"
        }
    }

    private var color: Color {
        switch call.direction {
        case .incoming: return .red
        case .outgoing: return QGTheme.Palette.online
        }
    }
}

struct CallSessionView: View {
    @EnvironmentObject private var store: AppStore
    @Environment(\.dismiss) private var dismiss
    @State private var isProximityCovered = false

    var body: some View {
        let call = store.activeCall

        ZStack {
            LinearGradient(
                colors: [QGTheme.Palette.accent, QGTheme.Palette.accentSoft],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            .ignoresSafeArea()

            VStack(spacing: 28) {
                HStack(spacing: 8) {
                    Circle()
                        .fill(qualityColor)
                        .frame(width: 10, height: 10)
                    Text(qualityText)
                        .font(.system(size: 13, weight: .bold, design: .monospaced))
                        .foregroundStyle(.white.opacity(0.9))
                    Spacer()
                }
                .padding(.top, 18)

                Spacer(minLength: 62)

                if let user = call.flatMap({ store.user(id: $0.userID) }) {
                    QGAvatarView(user: user, size: 104)
                }

                Text(call?.title ?? "Вызов")
                    .font(.system(size: 28, weight: .bold, design: .rounded))
                    .foregroundStyle(.white)

                Text(call.map { $0.kind == .video ? "Видео-звонок" : "Аудио-звонок" } ?? "")
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(.white.opacity(0.82))

                Text(durationText)
                    .font(.system(size: 16, weight: .bold, design: .monospaced))
                    .foregroundStyle(.white)

                Spacer()

                HStack(spacing: 18) {
                    SessionControlButton(
                        icon: store.activeCall?.isSpeakerEnabled == true ? "speaker.wave.3.fill" : "speaker.slash.fill",
                        tint: .white.opacity(0.18)
                    ) {
                        store.toggleSpeaker()
                    }

                    SessionControlButton(
                        icon: store.activeCall?.isMuted == true ? "mic.slash.fill" : "mic.fill",
                        tint: .white.opacity(0.18)
                    ) {
                        store.toggleMute()
                    }

                    SessionControlButton(icon: "phone.down.fill", tint: Color(red: 1, green: 0.14, blue: 0.16)) {
                        store.endActiveCall()
                        dismiss()
                    }
                }
                .padding(.bottom, 60)
            }
            .padding(.horizontal, QGTheme.pagePadding)

            if isProximityCovered {
                Color.black
                    .ignoresSafeArea()
                    .transition(.opacity)
            }
        }
        .onAppear {
            UIDevice.current.isProximityMonitoringEnabled = true
            isProximityCovered = UIDevice.current.proximityState
        }
        .onReceive(NotificationCenter.default.publisher(for: UIDevice.proximityStateDidChangeNotification)) { _ in
            isProximityCovered = UIDevice.current.proximityState
        }
        .onDisappear {
            UIDevice.current.isProximityMonitoringEnabled = false
            isProximityCovered = false
        }
    }

    private var durationText: String {
        let startedAt = store.activeCall?.startedAt ?? .now
        let elapsed = Int(Date().timeIntervalSince(startedAt))
        let minutes = elapsed / 60
        let seconds = elapsed % 60
        return String(format: "%02d:%02d", minutes, seconds)
    }

    private var qualityColor: Color {
        guard store.isOnline else { return .red }
        let ping = store.connectionPingMs
        if ping <= 85 { return QGTheme.Palette.online }
        if ping <= 160 { return .yellow }
        return .red
    }

    private var qualityText: String {
        guard store.isOnline else { return "offline" }
        return "\(store.connectionPingMs) ms"
    }
}

private struct SessionControlButton: View {
    let icon: String
    let tint: Color
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: icon)
                .font(.system(size: 22, weight: .bold))
                .foregroundStyle(.white)
                .frame(width: 72, height: 72)
                .background(Circle().fill(tint))
        }
        .buttonStyle(.plain)
    }
}
