import SwiftUI

@main
struct QGrammApp: App {
    @StateObject private var store = AppStore()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(store)
        }
    }
}

struct RootView: View {
    @EnvironmentObject private var store: AppStore

    var body: some View {
        Group {
            if store.state.session.isAuthenticated {
                MainShellView()
            } else {
                AuthFlowView()
            }
        }
        .preferredColorScheme(.light)
        .tint(QGTheme.Palette.accent)
    }
}

struct MainShellView: View {
    @EnvironmentObject private var store: AppStore

    var body: some View {
        ZStack(alignment: .bottom) {
            Group {
                switch store.state.selectedTab {
                case .chats:
                    ChatListView()
                case .network:
                    NetworkView()
                case .calls:
                    CallsView()
                case .settings:
                    SettingsView()
                }
            }

            FloatingDock(
                selectedTab: Binding(
                    get: { store.state.selectedTab },
                    set: store.selectTab(_:)
                )
            )
            .padding(.bottom, 28)
        }
        .qgScreenBackground()
        .fullScreenCover(item: $store.activeCall) { _ in
            CallSessionView()
                .environmentObject(store)
        }
        .sheet(isPresented: Binding(
            get: { store.state.session.shouldRevealRecoveryKey },
            set: { if !$0 { store.dismissRecoveryReveal() } }
        )) {
            RecoveryKeySheet()
                .environmentObject(store)
                .presentationDetents([.medium])
        }
    }
}

private struct RecoveryKeySheet: View {
    @EnvironmentObject private var store: AppStore

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("Recovery key")
                .font(.system(size: 28, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("Сохраните этот ключ отдельно. Он нужен для восстановления локальной истории переписок при потере доступа к аккаунту.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            Text(store.state.session.recoveryKey)
                .font(.system(size: 20, weight: .bold, design: .monospaced))
                .foregroundStyle(QGTheme.Palette.ink)
                .padding(18)
                .frame(maxWidth: .infinity, alignment: .leading)
                .qgCardStyle(cornerRadius: 24)

            Button("Открыть мессенджер") {
                store.dismissRecoveryReveal()
            }
            .buttonStyle(.borderedProminent)
            .tint(QGTheme.Palette.accent)
        }
        .padding(QGTheme.pagePadding)
        .qgScreenBackground()
    }
}
