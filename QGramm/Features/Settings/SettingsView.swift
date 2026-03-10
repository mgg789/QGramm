import PhotosUI
import SwiftUI

struct SettingsView: View {
    @EnvironmentObject private var store: AppStore

    @State private var firstName = ""
    @State private var lastName = ""
    @State private var nickname = ""
    @State private var bio = ""
    @State private var avatarItem: PhotosPickerItem?
    @State private var profileError = ""

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 22) {
                WordmarkView(title: "Qgramm", size: 38)
                    .frame(maxWidth: .infinity)

                if let user = store.currentUser {
                    profileSection(user: user)
                    encryptionSection(user: user)
                    cacheSection
                    languageSection
                    logoutSection
                }
            }
            .padding(.horizontal, QGTheme.pagePadding)
            .padding(.top, 24)
            .padding(.bottom, QGTheme.floatingBottomInset)
        }
        .qgScreenBackground()
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
    }

    private func profileSection(user: UserProfile) -> some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(spacing: 16) {
                ZStack(alignment: .bottomTrailing) {
                    QGAvatarView(user: user, size: 84, showOnlineRing: true)
                    PhotosPicker(selection: $avatarItem, matching: .images) {
                        Image("PencilIcon")
                            .resizable()
                            .renderingMode(.template)
                            .scaledToFit()
                            .frame(width: 16, height: 16)
                            .foregroundStyle(.white)
                            .frame(width: 32, height: 32)
                            .background(Circle().fill(QGTheme.Palette.accent))
                    }
                }

                VStack(alignment: .leading, spacing: 6) {
                    Text("Профиль")
                        .font(.system(size: 24, weight: .bold, design: .rounded))
                    Text("Имя, никнейм и аватар используются в личных чатах, звонках и invite-graph.")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
            }

            Group {
                TextField("Имя", text: $firstName)
                TextField("Фамилия", text: $lastName)
                TextField("@nickname", text: $nickname)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                TextField("О себе", text: $bio, axis: .vertical)
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

            Button("Сохранить профиль") {
                switch store.updateProfile(firstName: firstName, lastName: lastName, nickname: nickname, bio: bio) {
                case .success:
                    profileError = ""
                case let .failure(error):
                    profileError = error.localizedDescription
                }
            }
            .buttonStyle(.borderedProminent)
            .tint(QGTheme.Palette.accent)
        }
        .padding(22)
        .qgCardStyle()
    }

    private func encryptionSection(user: UserProfile) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Шифрование")
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("E2E: личные сообщения, голосовые, видео-кружки и файлы. Не E2E: граф инвайтов, trust telemetry, жалобы и системные события.")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            Text("Recovery key")
                .font(.system(size: 14, weight: .bold))
                .foregroundStyle(QGTheme.Palette.secondary)

            Text(store.state.session.recoveryKey.isEmpty ? "Будет создан после регистрации." : store.state.session.recoveryKey)
                .font(.system(size: 18, weight: .bold, design: .monospaced))
                .foregroundStyle(QGTheme.Palette.ink)
                .padding(16)
                .qgCardStyle(cornerRadius: 20)

            Picker("Режим безопасности", selection: Binding(
                get: { store.state.session.safetyMode },
                set: store.setSafetyMode(_:)
            )) {
                ForEach(SafetyMode.allCases) { mode in
                    Text(mode.title).tag(mode)
                }
            }
            .pickerStyle(.segmented)

            Text("Контакт: \(user.emailOrPhone)")
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.secondary)
        }
        .padding(22)
        .qgCardStyle()
    }

    private var cacheSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Кэш и база")
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)
            Text("Занято памяти")
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.secondary)
            Text(QGFormatters.storage.string(fromByteCount: store.state.cacheSizeBytes))
                .font(.system(size: 28, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)
            Text("История чатов и звонков хранится локально. Очистка кэша удалит превью и временные файлы, но не сотрёт саму переписку.")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            Button("Очистить кэш") {
                store.clearCache()
            }
            .buttonStyle(.borderedProminent)
            .tint(QGTheme.Palette.destructiveSurface)
            .foregroundStyle(QGTheme.Palette.destructive)
        }
        .padding(22)
        .qgCardStyle()
    }

    private var languageSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Язык")
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
        }
        .padding(22)
        .qgCardStyle()
    }

    private var logoutSection: some View {
        Button("Выйти") {
            store.logout()
        }
        .font(.system(size: 18, weight: .bold, design: .rounded))
        .frame(maxWidth: .infinity)
        .padding(.vertical, 18)
        .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.ink))
        .foregroundStyle(.white)
    }
}
