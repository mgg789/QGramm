import PhotosUI
import SwiftUI
import UIKit

struct SettingsView: View {
    @Environment(\.openURL) private var openURL
    @EnvironmentObject private var store: AppStore
    @AppStorage("qg.themeMode") private var themeModeRaw = AppThemeMode.system.rawValue

    @State private var firstName = ""
    @State private var lastName = ""
    @State private var nickname = ""
    @State private var bio = ""
    @State private var avatarItem: PhotosPickerItem?
    @State private var profileError = ""
    @State private var infoMessage = ""
    @State private var showRecoveryKey = false
    @State private var toastText = ""
    @State private var showPinSetup = false

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 22) {
                WordmarkView(title: "QGramm", size: 38)
                    .frame(maxWidth: .infinity)

                if let user = store.currentUser {
                    profileSection(user: user)
                    encryptionSection(user: user)
                    cacheSection
                    languageSection
                    securitySection
                    powerSavingSection
                    aboutSection
                    logoutSection
                }
            }
            .padding(.horizontal, QGTheme.pagePadding)
            .padding(.top, 10)
            .padding(.bottom, QGTheme.floatingBottomInset)
        }
        .qgScreenBackground()
        .alert(language.text(ru: "QGramm", en: "QGramm"), isPresented: Binding(
            get: { !infoMessage.isEmpty },
            set: { if !$0 { infoMessage = "" } }
        )) {
            Button(language.text(ru: "ОК", en: "OK")) { infoMessage = "" }
        } message: {
            Text(infoMessage)
        }
        .overlay(alignment: .bottom) {
            if !toastText.isEmpty {
                Text(toastText)
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(.white)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(
                        Capsule(style: .continuous)
                            .fill(QGTheme.Palette.dock.opacity(0.95))
                    )
                    .padding(.bottom, QGTheme.floatingBottomInset + 18)
                    .transition(.opacity.combined(with: .move(edge: .bottom)))
            }
        }
        .task(id: avatarItem) {
            guard let avatarItem else { return }
            if let data = try? await avatarItem.loadTransferable(type: Data.self) {
                let tempURL = FileManager.default.temporaryDirectory
                    .appendingPathComponent(UUID().uuidString)
                    .appendingPathExtension("jpg")
                try? data.write(to: tempURL)
                if let copied = try? QGMediaTools.copyItemIntoAppSupport(from: tempURL, folder: "avatars", preferredExtension: "jpg") {
                    store.updateAvatar(path: copied.path)
                }
            }
        }
        .onAppear {
            if let user = store.currentUser {
                firstName = user.firstName
                lastName = user.lastName
                nickname = user.nickname
                bio = user.bio
            }
        }
        .sheet(isPresented: $showPinSetup) {
            PinSetupSheet(language: language) { pin in
                store.setAppPinCode(pin)
                QGHaptics.light()
                showToast(language.text(ru: "PIN сохранён", en: "PIN saved"))
            }
            .presentationDetents([.medium])
        }
    }

    private func profileSection(user: UserProfile) -> some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack {
                Spacer()
                ZStack(alignment: .bottomTrailing) {
                    QGAvatarView(user: user, size: 92, showOnlineRing: false)
                    PhotosPicker(selection: $avatarItem, matching: .images) {
                        Image(systemName: "pencil")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(.white)
                            .frame(width: 34, height: 34)
                            .background(Circle().fill(QGTheme.Palette.accent))
                            .shadow(color: .black.opacity(0.2), radius: 8, y: 2)
                    }
                }
                Spacer()
            }

            Group {
                TextField(language.text(ru: "Имя", en: "First name"), text: $firstName)
                TextField(language.text(ru: "Фамилия", en: "Last name"), text: $lastName)
                TextField("@nickname", text: $nickname)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                TextField(language.text(ru: "О себе", en: "About"), text: $bio, axis: .vertical)
                    .lineLimit(2...4)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
            .qgCardStyle(cornerRadius: 20)

            if !profileError.isEmpty {
                Text(profileError)
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.destructive)
            }

            Button(language.text(ru: "Сохранить профиль", en: "Save profile")) {
                if let validationError = validateProfileInput() {
                    profileError = validationError
                    return
                }

                Task { @MainActor in
                    switch await store.updateProfile(firstName: firstName, lastName: lastName, nickname: nickname, bio: bio) {
                    case .success:
                        profileError = ""
                        QGHaptics.light()
                        showToast(language.text(ru: "Профиль сохранён", en: "Profile saved"))
                    case let .failure(error):
                        profileError = error.localizedDescription
                    }
                }
            }
            .buttonStyle(.borderedProminent)
            .tint(QGTheme.Palette.accent)
            .frame(maxWidth: .infinity, alignment: .center)
        }
        .padding(22)
        .frame(maxWidth: .infinity, alignment: .leading)
        .qgCardStyle()
    }

    private func encryptionSection(user: UserProfile) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 8) {
                Text(language.text(ru: "Шифрование", en: "Encryption"))
                    .font(.system(size: 22, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.ink)
                Button {
                    infoMessage = language.text(
                        ru: "E2E: личные сообщения, голосовые, видео-кружки и файлы. Не E2E: граф инвайтов, Trust telemetry, жалобы и системные события.",
                        en: "E2E: private messages, voice notes, round videos and files. Not E2E: invite graph, Trust telemetry, reports and system events."
                    )
                } label: {
                    Image(systemName: "questionmark.circle")
                        .font(.system(size: 17, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
                .buttonStyle(.plain)
                Spacer()
            }

            Button(showRecoveryKey
                   ? language.text(ru: "Скрыть Recovery key", en: "Hide Recovery key")
                   : language.text(ru: "Показать Recovery key", en: "Show Recovery key")) {
                showRecoveryKey.toggle()
                QGHaptics.light()
            }
            .font(.system(size: 15, weight: .bold))
            .buttonStyle(.bordered)

            if showRecoveryKey {
                Text(store.state.session.recoveryKey.isEmpty
                     ? language.text(ru: "Будет создан после регистрации.", en: "Will be generated after registration.")
                     : store.state.session.recoveryKey)
                    .font(.system(size: 18, weight: .bold, design: .monospaced))
                    .foregroundStyle(QGTheme.Palette.ink)
                    .padding(16)
                    .qgCardStyle(cornerRadius: 20)
                    .onTapGesture {
                        guard !store.state.session.recoveryKey.isEmpty else { return }
                        UIPasteboard.general.string = store.state.session.recoveryKey
                        QGHaptics.light()
                        showToast(language.text(ru: "Recovery key скопирован", en: "Recovery key copied"))
                    }
            }

            Picker(language.text(ru: "Режим безопасности", en: "Safety mode"), selection: Binding(
                get: { store.state.session.safetyMode },
                set: store.setSafetyMode(_:)
            )) {
                ForEach(SafetyMode.allCases) { mode in
                    Text(mode.title).tag(mode)
                }
            }
            .pickerStyle(.segmented)

            HStack(spacing: 8) {
                Text(language.text(ru: "Режим Classic и Local", en: "Classic and Local modes"))
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.secondary)
                Button {
                    infoMessage = language.text(
                        ru: "Classic: обычный E2E. Local: приоритет локальной peer-to-peer передачи при доступной локальной сети.",
                        en: "Classic: standard E2E mode. Local: prioritizes local peer-to-peer transfer when local network is available."
                    )
                } label: {
                    Image(systemName: "questionmark.circle")
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
                .buttonStyle(.plain)
            }

            Text(language.text(ru: "Контакт: \(user.emailOrPhone)", en: "Contact: \(user.emailOrPhone)"))
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.secondary)
        }
        .onChange(of: store.state.session.safetyMode) { _, _ in
            QGHaptics.light()
        }
        .padding(22)
        .frame(maxWidth: .infinity, alignment: .leading)
        .qgCardStyle()
    }

    private var cacheSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(language.text(ru: "Кэш и база", en: "Cache and storage"))
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)
            Text(language.text(ru: "Занято памяти", en: "Used space"))
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.secondary)
            Text(QGFormatters.storage.string(fromByteCount: store.state.cacheSizeBytes))
                .font(.system(size: 28, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)
            Text(language.text(
                ru: "История чатов и звонков хранится локально. Очистка кэша удалит превью и временные файлы, но не сотрёт саму переписку.",
                en: "Chat and call history is stored locally. Clearing cache removes previews and temporary files, but keeps the conversations."
            ))
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            Button(language.text(ru: "Очистить кэш", en: "Clear cache")) {
                store.clearCache()
                QGHaptics.heavy()
                showToast(language.text(ru: "Кэш очищен", en: "Cache cleared"))
            }
            .buttonStyle(.borderedProminent)
            .tint(QGTheme.Palette.destructiveSurface)
            .foregroundStyle(QGTheme.Palette.destructive)
        }
        .padding(22)
        .frame(maxWidth: .infinity, alignment: .leading)
        .qgCardStyle()
    }

    private var languageSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(language.text(ru: "Язык", en: "Language"))
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)
            Picker("Language", selection: Binding(
                get: { store.state.session.language },
                set: store.setLanguage(_:)
            )) {
                ForEach(AppLanguage.allCases) { language in
                    Text(language.title).tag(language)
                }
            }
            .pickerStyle(.segmented)

            Text(language.text(ru: "Тема", en: "Theme"))
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.ink)
            Picker("Theme", selection: themeModeBinding) {
                ForEach(AppThemeMode.allCases) { mode in
                    Text(mode.title(language: language)).tag(mode)
                }
            }
            .pickerStyle(.segmented)
        }
        .onChange(of: store.state.session.language) { _, _ in
            QGHaptics.light()
        }
        .onChange(of: themeModeRaw) { _, _ in
            QGHaptics.light()
        }
        .padding(22)
        .qgCardStyle()
    }

    private var securitySection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(language.text(ru: "Вход и защита", en: "Login security"))
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Toggle(isOn: Binding(
                get: { store.hasPinCodeEnabled },
                set: { enabled in
                    if enabled {
                        showPinSetup = true
                    } else {
                        store.clearAppPinCode()
                        QGHaptics.light()
                    }
                }
            )) {
                HStack(spacing: 8) {
                    Image(systemName: "number.circle")
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text(language.text(ru: "PIN-код (4 цифры)", en: "PIN code (4 digits)"))
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.ink)
                }
            }
            .toggleStyle(.switch)

            Toggle(isOn: Binding(
                get: { store.state.session.isFaceIDEnabled },
                set: {
                    store.setFaceIDEnabled($0)
                    QGHaptics.light()
                }
            )) {
                HStack(spacing: 8) {
                    Image(systemName: "faceid")
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text("Face ID")
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.ink)
                }
            }
            .toggleStyle(.switch)
        }
        .padding(22)
        .qgCardStyle()
    }

    private var powerSavingSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Toggle(isOn: Binding(
                get: { store.state.session.isPowerSavingEnabled },
                set: {
                    store.setPowerSavingEnabled($0)
                    QGHaptics.light()
                }
            )) {
                HStack(spacing: 8) {
                    Image(systemName: "battery.50")
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text(language.text(ru: "Экономия заряда", en: "Power saving"))
                        .font(.system(size: 16, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.ink)
                }
            }
            .toggleStyle(.switch)

            Text(language.text(
                ru: "Режим отключает вибрации, упрощает фоновые эффекты и снижает частоту обновлений сети для активных звонков.",
                en: "This mode disables haptics, reduces heavy visual effects, and lowers call network update frequency."
            ))
            .font(.system(size: 13, weight: .medium))
            .foregroundStyle(QGTheme.Palette.secondary)
        }
        .padding(22)
        .qgCardStyle()
    }

    private var aboutSection: some View {
        VStack(spacing: 12) {
            Button {
                openURL(aboutURL)
                QGHaptics.light()
            } label: {
                HStack(spacing: 10) {
                    Image(systemName: "info.circle.fill")
                        .font(.system(size: 18))
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text(language.text(ru: "Узнать больше о QGramm", en: "Learn more about QGramm"))
                        .font(.system(size: 16, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.ink)
                    Spacer()
                }
                .padding(16)
                .qgCardStyle(cornerRadius: 20)
            }
            .buttonStyle(.plain)

            Button {
                openURL(termsURL)
                QGHaptics.light()
            } label: {
                HStack(spacing: 10) {
                    Image(systemName: "doc.text")
                        .font(.system(size: 18))
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text(language.text(ru: "Пользовательское соглашение", en: "User agreement"))
                        .font(.system(size: 16, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.ink)
                    Spacer()
                }
                .padding(16)
                .qgCardStyle(cornerRadius: 20)
            }
            .buttonStyle(.plain)
        }
    }

    private var logoutSection: some View {
        Button(language.text(ru: "Выйти", en: "Log out")) {
            store.logout()
        }
        .font(.system(size: 18, weight: .bold, design: .rounded))
        .frame(maxWidth: .infinity)
        .padding(.vertical, 18)
        .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.dock))
        .foregroundStyle(.white)
    }

    private var language: AppLanguage {
        store.state.session.language
    }

    private var themeModeBinding: Binding<AppThemeMode> {
        Binding(
            get: { AppThemeMode(rawValue: themeModeRaw) ?? .system },
            set: { themeModeRaw = $0.rawValue }
        )
    }

    private var aboutURL: URL {
        URL(string: "https://qgramm.app")!
    }

    private var termsURL: URL {
        URL(string: "https://qgramm.app/terms")!
    }

    private func showToast(_ text: String) {
        qgAnimate(.spring(response: 0.22, dampingFraction: 0.95)) {
            toastText = text
        }
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(1.5))
            qgAnimate(.spring(response: 0.2, dampingFraction: 1)) {
                toastText = ""
            }
        }
    }

    private func validateProfileInput() -> String? {
        let cleanedFirst = firstName.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedLast = lastName.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedNick = nickname.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedBio = bio.trimmingCharacters(in: .whitespacesAndNewlines)

        guard !cleanedFirst.isEmpty, !cleanedLast.isEmpty, !cleanedNick.isEmpty, !cleanedBio.isEmpty else {
            return language.text(
                ru: "Заполните все поля профиля перед сохранением.",
                en: "Fill in all profile fields before saving."
            )
        }

        guard cleanedNick.hasPrefix("@") else {
            return language.text(
                ru: "Никнейм должен начинаться с @",
                en: "Nickname must start with @"
            )
        }

        return nil
    }
}

private struct PinSetupSheet: View {
    @Environment(\.dismiss) private var dismiss

    let language: AppLanguage
    let onSave: (String) -> Void

    @State private var pin = ""
    @State private var pinConfirm = ""
    @State private var errorText = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(language.text(ru: "Создать PIN", en: "Create PIN"))
                .font(.system(size: 26, weight: .bold, design: .rounded))

            Text(language.text(ru: "Введите и подтвердите PIN из 4 цифр.", en: "Enter and confirm a 4-digit PIN."))
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            SecureField("PIN", text: Binding(
                get: { pin },
                set: { pin = String($0.filter(\.isNumber).prefix(4)) }
            ))
            .keyboardType(.numberPad)
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .qgCardStyle(cornerRadius: 16)

            SecureField(language.text(ru: "Повторите PIN", en: "Repeat PIN"), text: Binding(
                get: { pinConfirm },
                set: { pinConfirm = String($0.filter(\.isNumber).prefix(4)) }
            ))
            .keyboardType(.numberPad)
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .qgCardStyle(cornerRadius: 16)

            if !errorText.isEmpty {
                Text(errorText)
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.destructive)
            }

            Button {
                guard pin.count == 4, pinConfirm.count == 4 else {
                    errorText = language.text(ru: "PIN должен состоять из 4 цифр.", en: "PIN must be exactly 4 digits.")
                    return
                }
                guard pin == pinConfirm else {
                    errorText = language.text(ru: "PIN не совпадает.", en: "PIN codes do not match.")
                    return
                }
                onSave(pin)
                dismiss()
            } label: {
                Text(language.text(ru: "Сохранить", en: "Save"))
                    .font(.system(size: 17, weight: .bold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
                    .background(RoundedRectangle(cornerRadius: 18, style: .continuous).fill(QGTheme.Palette.accent))
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)

            Spacer(minLength: 0)
        }
        .padding(QGTheme.pagePadding)
        .qgScreenBackground()
    }
}
