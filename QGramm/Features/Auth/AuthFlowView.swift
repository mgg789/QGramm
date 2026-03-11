import SwiftUI

struct AuthFlowView: View {
    @Environment(\.openURL) private var openURL
    @EnvironmentObject private var store: AppStore

    @State private var inviteCode = ""
    @State private var contact = ""
    @State private var verificationCode = ""
    @State private var firstName = ""
    @State private var lastName = ""
    @State private var nickname = ""
    @State private var captchaPassed = false
    @State private var selectedSafetyMode: SafetyMode = .classic
    @State private var acceptedTerms = false
    @State private var infoMessage = ""
    @State private var errorMessage = ""
    @State private var isLoading = false

    private enum AuthMode: String, CaseIterable, Identifiable {
        case register
        case login
        var id: String { rawValue }
    }

    private enum Step {
        case invite
        case contact
        case profile
        case loginCode
    }

    @State private var authMode: AuthMode = .register

    private var step: Step {
        if authMode == .register && !store.state.session.hasBoundInvite {
            return .invite
        }
        if store.state.session.expectedVerificationCode.isEmpty {
            return .contact
        }
        return authMode == .register ? .profile : .loginCode
    }

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 22) {
                Spacer(minLength: 36)

                WordmarkView(title: "QGramm", size: 48)
                    .frame(maxWidth: .infinity, alignment: .center)

                Text("Приватный invite-only мессенджер для личных разговоров без перегруженного интерфейса.")
                    .font(.system(size: 18, weight: .medium, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.muted)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 10)

                Picker("Mode", selection: $authMode) {
                    Text("Регистрация").tag(AuthMode.register)
                    Text("Вход").tag(AuthMode.login)
                }
                .pickerStyle(.segmented)
                .disabled(isLoading)
                .onChange(of: authMode) { _, mode in
                    errorMessage = ""
                    infoMessage = ""
                    verificationCode = ""
                    if mode == .login {
                        store.setAuthPurpose(.login)
                    } else {
                        store.setAuthPurpose(.register)
                    }
                }

                Group {
                    switch step {
                    case .invite:
                        inviteStep
                    case .contact:
                        contactStep
                    case .profile:
                        profileStep
                    case .loginCode:
                        loginStep
                    }
                }

                if !errorMessage.isEmpty {
                    Text(errorMessage)
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.destructive)
                        .padding(.horizontal, 4)
                }

                if !infoMessage.isEmpty {
                    Text(infoMessage)
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.accent)
                        .padding(.horizontal, 4)
                }
            }
            .padding(.horizontal, QGTheme.pagePadding)
            .padding(.bottom, 40)
        }
        .qgScreenBackground()
        .onAppear {
            selectedSafetyMode = store.state.session.safetyMode
        }
    }

    private var inviteStep: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("Вход")
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("Для регистрации нужен инвайт от существующего участника сети. После успешного ввода устройство будет привязано к приглашению.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            TextField("Введите invite code", text: $inviteCode)
                .textInputAutocapitalization(.characters)
                .autocorrectionDisabled()
                .font(.system(size: 18, weight: .semibold, design: .monospaced))
                .padding(.horizontal, 18)
                .padding(.vertical, 18)
                .qgCardStyle(cornerRadius: 20)

            Button {
                isLoading = true
                Task { @MainActor in
                    let accepted = await store.acceptInvite(code: inviteCode)
                    isLoading = false
                    errorMessage = accepted ? "" : "Инвайт не найден или срок его действия истёк."
                    infoMessage = accepted ? "Устройство привязано к приглашению. Теперь можно подтвердить email." : ""
                }
            } label: {
                actionTitle("Активировать приглашение")
            }
            .buttonStyle(.plain)
            .disabled(isLoading)
        }
        .padding(24)
        .qgCardStyle()
    }

    private var contactStep: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text(authMode == .register ? "Регистрация" : "Вход")
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text(authMode == .register
                 ? "Подтвердите email. Сервер отправит код на почту после проверки captcha."
                 : "Введите email аккаунта. Код входа будет отправлен на почту после captcha.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            TextField("email", text: $contact)
                .keyboardType(.emailAddress)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 18)
                .padding(.vertical, 18)
                .qgCardStyle(cornerRadius: 20)

            Toggle(isOn: $captchaPassed) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Captcha")
                        .font(.system(size: 17, weight: .bold))
                    Text("Проверка перед отправкой кода")
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
            }
            .toggleStyle(.switch)
            .padding(18)
            .qgCardStyle(cornerRadius: 20)

            Button {
                isLoading = true
                Task { @MainActor in
                    let result = await store.sendVerificationCode(
                        to: contact,
                        captchaPassed: captchaPassed,
                        purpose: authMode == .register ? .register : .login
                    )
                    isLoading = false
                    switch result {
                    case let .success(debugCode):
                        if store.state.session.expectedVerificationCode.isEmpty {
                            errorMessage = "Не удалось отправить код."
                            infoMessage = ""
                            return
                        }
                        errorMessage = ""
                        verificationCode = debugCode ?? ""
                        infoMessage = debugCode == nil
                            ? "Код отправлен на email. Введите его на следующем шаге."
                            : "Код отправлен. Debug-code: \(debugCode ?? "")"
                    case let .failure(error):
                        errorMessage = error.localizedDescription
                        infoMessage = ""
                    }
                }
            } label: {
                actionTitle("Отправить код")
            }
            .buttonStyle(.plain)
            .disabled(isLoading)
        }
        .padding(24)
        .qgCardStyle()
    }

    private var profileStep: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Профиль")
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("Введите код подтверждения, имя и уникальный никнейм. Recovery key будет показан сразу после входа.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            Group {
                TextField("Код подтверждения", text: $verificationCode)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                TextField("Имя", text: $firstName)
                TextField("Фамилия", text: $lastName)
                TextField("@nickname", text: $nickname)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 16)
            .qgCardStyle(cornerRadius: 20)

            VStack(alignment: .leading, spacing: 12) {
                Text("Режим безопасности")
                    .font(.system(size: 16, weight: .bold))

                Picker("Режим безопасности", selection: $selectedSafetyMode) {
                    ForEach(SafetyMode.allCases) { mode in
                        Text(mode.title).tag(mode)
                    }
                }
                .pickerStyle(.segmented)
            }
            .padding(18)
            .qgCardStyle(cornerRadius: 20)

            Toggle(isOn: $acceptedTerms) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Принять пользовательское соглашение")
                        .font(.system(size: 15, weight: .bold))
                    Button("Открыть соглашение") {
                        if let url = URL(string: "https://qgramm.app/terms") {
                            openURL(url)
                        }
                    }
                    .font(.system(size: 13, weight: .semibold))
                    .buttonStyle(.plain)
                    .foregroundStyle(QGTheme.Palette.accent)
                }
            }
            .toggleStyle(.switch)
            .padding(18)
            .qgCardStyle(cornerRadius: 20)

            Button {
                guard acceptedTerms else {
                    errorMessage = "Подтвердите пользовательское соглашение."
                    infoMessage = ""
                    return
                }

                isLoading = true
                Task { @MainActor in
                    let result = await store.completeRegistration(
                        firstName: firstName,
                        lastName: lastName,
                        nickname: nickname,
                        enteredCode: verificationCode,
                        safetyMode: selectedSafetyMode
                    )
                    isLoading = false
                    switch result {
                    case .success:
                        errorMessage = ""
                        infoMessage = "Профиль создан. Открываю мессенджер."
                    case let .failure(error):
                        errorMessage = error.localizedDescription
                        infoMessage = ""
                    }
                }
            } label: {
                actionTitle("Создать аккаунт")
            }
            .buttonStyle(.plain)
            .disabled(isLoading)
        }
        .padding(24)
        .qgCardStyle()
    }

    private var loginStep: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Код входа")
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("Введите код, отправленный на email.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            TextField("Код подтверждения", text: $verificationCode)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 18)
                .padding(.vertical, 16)
                .qgCardStyle(cornerRadius: 20)

            Button {
                isLoading = true
                Task { @MainActor in
                    let result = await store.login(enteredCode: verificationCode)
                    isLoading = false
                    switch result {
                    case .success:
                        errorMessage = ""
                        infoMessage = "Вход выполнен."
                    case let .failure(error):
                        errorMessage = error.localizedDescription
                        infoMessage = ""
                    }
                }
            } label: {
                actionTitle("Войти")
            }
            .buttonStyle(.plain)
            .disabled(isLoading)
        }
        .padding(24)
        .qgCardStyle()
    }

    private func actionTitle(_ title: String) -> some View {
        HStack(spacing: 10) {
            if isLoading {
                ProgressView()
                    .tint(.white)
            }
            Text(title)
                .font(.system(size: 18, weight: .bold, design: .rounded))
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 18)
        .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.accent))
        .foregroundStyle(.white)
    }
}
