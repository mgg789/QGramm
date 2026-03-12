import LocalAuthentication
import SwiftUI
import UserNotifications
import UIKit

final class QGApplicationDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound, .badge])
    }
}

@main
struct QGrammApp: App {
    @UIApplicationDelegateAdaptor(QGApplicationDelegate.self) private var appDelegate
    @StateObject private var store = AppStore()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(store)
        }
    }
}

struct RootView: View {
    @Environment(\.scenePhase) private var scenePhase
    @EnvironmentObject private var store: AppStore
    @AppStorage("qg.themeMode") private var themeModeRaw = AppThemeMode.system.rawValue
    @AppStorage("qg.darkModeEnabled") private var legacyDarkModeEnabled = false
    @State private var hasMigratedThemeMode = false
    @State private var isAppLocked = false

    var body: some View {
        Group {
            if store.state.session.isAuthenticated {
                MainShellView()
            } else {
                AuthFlowView()
            }
        }
        .environment(\.locale, Locale(identifier: store.state.session.language.localeIdentifier))
        .preferredColorScheme(preferredColorScheme)
        .tint(QGTheme.Palette.accent)
        .onAppear(perform: migrateLegacyThemeModeIfNeeded)
        .onAppear(perform: updateLockState)
        .task {
            await store.bootstrapRemoteSession()
        }
        .onChange(of: store.state.session.currentUserID) { _, _ in updateLockState() }
        .onChange(of: store.state.session.appPinCode) { _, _ in updateLockState() }
        .onChange(of: store.state.session.isFaceIDEnabled) { _, _ in updateLockState() }
        .onChange(of: scenePhase) { _, phase in
            if phase != .active, store.needsAppUnlock {
                isAppLocked = true
            }
        }
        .fullScreenCover(isPresented: $isAppLocked) {
            AppLockView(isLocked: $isAppLocked)
                .environmentObject(store)
        }
    }

    private var selectedThemeMode: AppThemeMode {
        AppThemeMode(rawValue: themeModeRaw) ?? .system
    }

    private var preferredColorScheme: ColorScheme? {
        switch selectedThemeMode {
        case .system:
            return nil
        case .light:
            return .light
        case .dark:
            return .dark
        }
    }

    private func migrateLegacyThemeModeIfNeeded() {
        guard !hasMigratedThemeMode else { return }
        hasMigratedThemeMode = true
        let defaults = UserDefaults.standard
        guard defaults.object(forKey: "qg.themeMode") == nil else { return }
        themeModeRaw = legacyDarkModeEnabled ? AppThemeMode.dark.rawValue : AppThemeMode.light.rawValue
    }

    private func updateLockState() {
        isAppLocked = store.needsAppUnlock
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
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .contentShape(Rectangle())

            if !(store.state.selectedTab == .chats && store.isChatRoomOpen) {
                FloatingDock(
                    selectedTab: Binding(
                        get: { store.state.selectedTab },
                        set: store.selectTab(_:)
                    )
                )
                .padding(.horizontal, QGTheme.pagePadding)
                .padding(.bottom, 20)
                .zIndex(10)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .qgScreenBackground()
        .ignoresSafeArea(edges: .bottom)
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

private struct AppLockView: View {
    @Environment(\.scenePhase) private var scenePhase
    @EnvironmentObject private var store: AppStore
    @Binding var isLocked: Bool

    @State private var pinInput = ""
    @State private var errorText = ""
    @State private var isRunningFaceID = false
    @State private var pinShakeSeed: CGFloat = 0
    @FocusState private var isPinFieldFocused: Bool

    var body: some View {
        ZStack {
            Rectangle()
                .fill(.ultraThinMaterial)
                .ignoresSafeArea()

            VStack(spacing: 20) {
                Spacer()

                Image(systemName: "lock.fill")
                    .font(.system(size: 34, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.accent)

                Text("QGramm")
                    .font(.system(size: 34, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.wordmark)

                Text(store.state.session.language.text(ru: "Подтвердите вход", en: "Unlock the app"))
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.secondary)

                if store.hasPinCodeEnabled {
                    VStack(spacing: 12) {
                        HStack(spacing: 10) {
                            ForEach(0..<4, id: \.self) { index in
                                Circle()
                                    .fill(index < pinInput.count ? QGTheme.Palette.accent : QGTheme.Palette.line)
                                    .frame(width: 12, height: 12)
                            }
                        }
                        .modifier(ShakeEffect(animatableData: pinShakeSeed))

                        TextField("", text: Binding(
                            get: { pinInput },
                            set: { newValue in
                                let digits = newValue.filter(\.isNumber)
                                pinInput = String(digits.prefix(4))
                                if pinInput.count == 4 {
                                    unlockWithPin()
                                }
                            }
                        ))
                        .keyboardType(.numberPad)
                        .textContentType(.oneTimeCode)
                        .focused($isPinFieldFocused)
                        .frame(width: 1, height: 1)
                        .opacity(0.01)
                    }
                    .frame(maxWidth: 220)
                }

                if store.state.session.isFaceIDEnabled {
                    Button {
                        authenticateWithFaceID()
                    } label: {
                        Label("Face ID", systemImage: "faceid")
                            .font(.system(size: 16, weight: .bold))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 14)
                            .background(RoundedRectangle(cornerRadius: 18, style: .continuous).fill(QGTheme.Palette.accent))
                            .foregroundStyle(.white)
                    }
                    .buttonStyle(.plain)
                    .disabled(isRunningFaceID)
                    .frame(maxWidth: 260)
                }

                if !errorText.isEmpty {
                    Text(errorText)
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.destructive)
                }

                Spacer()
            }
            .padding(QGTheme.pagePadding)
        }
        .onAppear {
            if store.hasPinCodeEnabled {
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) {
                    isPinFieldFocused = true
                }
            }
            triggerBiometricUnlockIfNeeded()
        }
        .onTapGesture {
            if store.hasPinCodeEnabled {
                isPinFieldFocused = true
            }
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active {
                triggerBiometricUnlockIfNeeded()
            }
        }
    }

    private func unlockWithPin() {
        if store.validatePin(pinInput) {
            QGHaptics.light()
            isLocked = false
            errorText = ""
            pinInput = ""
        } else {
            QGHaptics.error()
            errorText = store.state.session.language.text(ru: "Неверный PIN", en: "Invalid PIN")
            pinInput = ""
            qgAnimate(.easeInOut(duration: 0.42)) {
                pinShakeSeed += 1
            }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) {
                isPinFieldFocused = true
            }
        }
    }

    private func authenticateWithFaceID() {
        guard !isRunningFaceID else { return }
        isRunningFaceID = true

        let context = LAContext()
        var authError: NSError?

        guard context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &authError) else {
            isRunningFaceID = false
            errorText = store.state.session.language.text(
                ru: "Face ID недоступен на этом устройстве.",
                en: "Face ID is not available on this device."
            )
            return
        }

        let reason = store.state.session.language.text(
            ru: "Разблокируйте QGramm",
            en: "Unlock QGramm"
        )
        context.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, localizedReason: reason) { success, _ in
            Task { @MainActor in
                isRunningFaceID = false
                if success {
                    QGHaptics.light()
                    errorText = ""
                    isLocked = false
                } else {
                    errorText = store.state.session.language.text(ru: "Face ID не подтверждён.", en: "Face ID verification failed.")
                }
            }
        }
    }

    private func triggerBiometricUnlockIfNeeded() {
        if store.state.session.isFaceIDEnabled {
            authenticateWithFaceID()
        }
    }
}

private struct ShakeEffect: GeometryEffect {
    var amount: CGFloat = 8
    var shakesPerUnit: CGFloat = 3
    var animatableData: CGFloat

    func effectValue(size: CGSize) -> ProjectionTransform {
        ProjectionTransform(
            CGAffineTransform(
                translationX: amount * sin(animatableData * .pi * shakesPerUnit),
                y: 0
            )
        )
    }
}
