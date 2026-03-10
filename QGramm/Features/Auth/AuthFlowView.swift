import SwiftUI

struct AuthFlowView: View {
    @EnvironmentObject private var store: AppStore

    @State private var inviteCode = ""
    @State private var contact = ""
    @State private var verificationCode = ""
    @State private var firstName = ""
    @State private var lastName = ""
    @State private var nickname = ""
    @State private var captchaPassed = false
    @State private var selectedSafetyMode: SafetyMode = .classic
    @State private var infoMessage = ""
    @State private var errorMessage = ""

    private enum Step {
        case invite
        case contact
        case profile
    }

    private var step: Step {
        if !store.state.session.hasBoundInvite {
            return .invite
        }
        if store.state.session.expectedVerificationCode.isEmpty {
            return .contact
        }
        return .profile
    }

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 22) {
                Spacer(minLength: 36)

                WordmarkView(title: "Qgramm", size: 48)
                    .frame(maxWidth: .infinity, alignment: .center)

                Text("Приватный invite-only мессенджер для личных разговоров без перегруженного интерфейса.")
                    .font(.system(size: 18, weight: .medium, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.muted)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 10)

                Group {
                    switch step {
                    case .invite:
                        inviteStep
                    case .contact:
                        contactStep
                    case .profile:
                        profileStep
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

                if !store.state.users.isEmpty {
                    Button {
                        store.restoreLocalAccount()
                    } label: {
                        Text("Войти в локальный аккаунт")
                            .font(.system(size: 16, weight: .bold, design: .rounded))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 16)
                            .qgCardStyle(cornerRadius: 22, fill: .white.opacity(0.55))
                    }
                    .buttonStyle(.plain)
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
                let accepted = store.acceptInvite(code: inviteCode)
                errorMessage = accepted ? "" : "Инвайт не найден или срок его действия истёк."
                infoMessage = accepted ? "Устройство привязано к приглашению. Теперь можно подтвердить контакт." : ""
            } label: {
                Text("Активировать приглашение")
                    .font(.system(size: 18, weight: .bold, design: .rounded))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 18)
                    .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.accent))
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
        }
        .padding(24)
        .qgCardStyle()
    }

    private var contactStep: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("Регистрация")
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("Подтвердите email или телефон. Для локального V1 код показывается внутри приложения, чтобы поток можно было проверить без бэкенда.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)

            TextField("email или телефон", text: $contact)
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
                    Text("Имитация успешной проверки перед отправкой кода")
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
            }
            .toggleStyle(.switch)
            .padding(18)
            .qgCardStyle(cornerRadius: 20)

            Button {
                if let code = store.sendVerificationCode(to: contact, captchaPassed: captchaPassed) {
                    infoMessage = "Демо-код подтверждения: \(code)"
                    errorMessage = ""
                    verificationCode = code
                } else {
                    errorMessage = "Введите контакт и пройдите captcha."
                    infoMessage = ""
                }
            } label: {
                Text("Отправить код")
                    .font(.system(size: 18, weight: .bold, design: .rounded))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 18)
                    .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.accent))
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
        }
        .padding(24)
        .qgCardStyle()
    }

    private var profileStep: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Профиль")
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            Text("Введите код подтверждения, имя и уникальный никнейм. Recovery key будет показан сразу после входа и сохранится в настройках шифрования.")
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

            Button {
                let result = store.completeRegistration(
                    firstName: firstName,
                    lastName: lastName,
                    nickname: nickname,
                    enteredCode: verificationCode,
                    safetyMode: selectedSafetyMode
                )

                switch result {
                case .success:
                    errorMessage = ""
                    infoMessage = "Профиль создан. Открываю мессенджер."
                case let .failure(error):
                    errorMessage = error.localizedDescription
                    infoMessage = ""
                }
            } label: {
                Text("Создать аккаунт")
                    .font(.system(size: 18, weight: .bold, design: .rounded))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 18)
                    .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.accent))
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
        }
        .padding(24)
        .qgCardStyle()
    }
}
