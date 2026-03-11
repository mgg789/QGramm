import SwiftUI
import UIKit

struct NetworkView: View {
    @EnvironmentObject private var store: AppStore
    @State private var infoMessage = ""
    @State private var expandedInvite: InviteRecord?

    private var currentUser: UserProfile? { store.currentUser }
    private var invites: [InviteRecord] {
        store.invitesForCurrentUser().filter(\.isActive)
    }

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 22) {
                header
                inviteSection
            }
            .padding(.horizontal, QGTheme.pagePadding)
            .padding(.top, 10)
            .padding(.bottom, QGTheme.floatingBottomInset)
        }
        .qgScreenBackground()
        .alert(language.text(ru: "Сеть", en: "Network"), isPresented: Binding(
            get: { !infoMessage.isEmpty },
            set: { if !$0 { infoMessage = "" } }
        )) {
            Button(language.text(ru: "ОК", en: "OK")) { infoMessage = "" }
        } message: {
            Text(infoMessage)
        }
        .sheet(item: $expandedInvite) { invite in
            InviteQRViewerSheet(payload: invite.deepLink)
                .presentationBackground(.clear)
        }
        .task {
            try? await store.refreshFromServer()
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                WordmarkView(title: "QGramm", size: 38)
                if !store.isOnline {
                    HStack(spacing: 6) {
                        ProgressView()
                            .controlSize(.small)
                        Text(language.text(ru: "Нет сети", en: "Offline"))
                            .font(.system(size: 12, weight: .bold))
                    }
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .qgCardStyle(cornerRadius: 14, fill: QGTheme.Palette.headerSurface)
                }
                Spacer()
                if let currentUser {
                    TrustBadgeView(level: currentUser.trustLevel)
                }
            }

            if let currentUser {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(alignment: .center, spacing: 8) {
                        Text(language.text(ru: "Сеть и Доверие", en: "Network & Trust"))
                            .font(.system(size: 26, weight: .bold, design: .rounded))
                            .foregroundStyle(QGTheme.Palette.ink)
                        Button {
                            infoMessage = language.text(
                                ru: "Уровень доверия влияет на лимиты инвайтов и будущие социальные функции.",
                                en: "Trust level controls invite limits and future social features."
                            )
                        } label: {
                            Image(systemName: "questionmark.circle")
                                .font(.system(size: 18, weight: .semibold))
                                .foregroundStyle(QGTheme.Palette.secondary)
                                .frame(width: 24, height: 24)
                        }
                        .buttonStyle(.plain)
                    }

                    trustProgressBar(for: currentUser.trustLevel)

                    Text(
                        language.text(
                            ru: "Уровень \(currentUser.trustLevel.rawValue) из 8",
                            en: "Level \(currentUser.trustLevel.rawValue) of 8"
                        )
                    )
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.secondary)

                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(trustCapabilities(for: currentUser.trustLevel), id: \.title) { row in
                            HStack(alignment: .top) {
                                Text(row.title)
                                    .font(.system(size: 14, weight: .semibold))
                                    .foregroundStyle(QGTheme.Palette.secondary)
                                Spacer(minLength: 12)
                                Text(row.value)
                                    .font(.system(size: 14, weight: .bold))
                                    .foregroundStyle(QGTheme.Palette.ink)
                            }
                        }
                    }
                }
                .padding(22)
                .qgCardStyle()
            }
        }
    }

    private var inviteSection: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text(language.text(ru: "Инвайты", en: "Invites"))
                    .font(.system(size: 22, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.ink)
                Spacer()
                Button(language.text(ru: "Создать", en: "Create")) {
                    Task { @MainActor in
                        switch await store.generateInvite() {
                        case let .success(invite):
                            infoMessage = language.text(ru: "Создан инвайт \(invite.code)", en: "Invite created \(invite.code)")
                        case let .failure(error):
                            infoMessage = error.localizedDescription
                        }
                    }
                }
                .font(.system(size: 16, weight: .bold))
            }

            if let remaining = store.remainingInvitesThisWeek() {
                HStack(spacing: 8) {
                    Image(systemName: "calendar.badge.clock")
                        .font(.system(size: 14, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text(remaining == .max
                         ? language.text(ru: "До конца недели: без лимита", en: "Until week end: unlimited")
                         : language.text(ru: "До конца недели осталось: \(remaining)", en: "Left until week end: \(remaining)")
                    )
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.secondary)
                    Spacer()
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .qgCardStyle(cornerRadius: 16, fill: QGTheme.Palette.headerSurface)
            }

            if invites.isEmpty {
                EmptyStateView(
                    title: language.text(ru: "Пока нет активных инвайтов", en: "No active invites yet"),
                    message: language.text(
                        ru: "Повышение trust level открывает право приглашать новых пользователей.",
                        en: "Increasing trust level unlocks inviting new users."
                    ),
                    systemImage: "person.badge.plus"
                )
                .frame(maxWidth: .infinity, alignment: .center)
            } else {
                ForEach(invites) { invite in
                    VStack(alignment: .leading, spacing: 14) {
                        HStack(alignment: .top) {
                            Button {
                                expandedInvite = invite
                                QGHaptics.light()
                            } label: {
                                InviteQRCodeView(payload: invite.deepLink)
                            }
                            .buttonStyle(.plain)

                            VStack(alignment: .leading, spacing: 8) {
                                Text(invite.code)
                                    .font(.system(size: 18, weight: .bold, design: .monospaced))
                                    .foregroundStyle(QGTheme.Palette.ink)
                                Text(invite.isActive
                                    ? language.text(
                                        ru: "Активен до \(QGFormatters.dayTitle.string(from: invite.expiresAt))",
                                        en: "Active until \(QGFormatters.dayTitle.string(from: invite.expiresAt))"
                                    )
                                    : language.text(ru: "Использован", en: "Used")
                                )
                                    .font(.system(size: 13, weight: .bold))
                                    .foregroundStyle(invite.isActive ? QGTheme.Palette.online : QGTheme.Palette.secondary)
                            }
                        }

                        HStack(spacing: 12) {
                            Button(language.text(ru: "Копировать код", en: "Copy code")) {
                                UIPasteboard.general.string = invite.code
                                QGHaptics.light()
                                infoMessage = language.text(ru: "Код скопирован.", en: "Code copied.")
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(QGTheme.Palette.accent)

                            Button(language.text(ru: "Копировать ссылку", en: "Copy link")) {
                                UIPasteboard.general.string = invite.deepLink
                                QGHaptics.light()
                                infoMessage = language.text(ru: "Ссылка скопирована.", en: "Link copied.")
                            }
                            .buttonStyle(.bordered)
                        }
                    }
                    .padding(18)
                    .qgCardStyle(cornerRadius: 28)
                }
            }
        }
    }

    private var language: AppLanguage {
        store.state.session.language
    }

    private func trustProgressBar(for level: TrustLevel) -> some View {
        let progress = Double(level.rawValue) / Double(TrustLevel.allCases.count)

        return GeometryReader { proxy in
            ZStack(alignment: .leading) {
                Capsule(style: .continuous)
                    .fill(QGTheme.Palette.line)
                Capsule(style: .continuous)
                    .fill(
                        LinearGradient(
                            colors: [QGTheme.Palette.accentSoft, QGTheme.Palette.accentBlue, QGTheme.Palette.accent],
                            startPoint: .leading,
                            endPoint: .trailing
                        )
                    )
                    .frame(width: max(proxy.size.width * progress, 24))
            }
        }
        .frame(height: 12)
    }

    private func trustCapabilities(for level: TrustLevel) -> [(title: String, value: String)] {
        let yes = language.text(ru: "Да", en: "Yes")
        let no = language.text(ru: "Нет", en: "No")
        let unlimited = language.text(ru: "Без лимита", en: "Unlimited")
        let weeklyInvites = level.weeklyInviteLimit == .max ? unlimited : "\(level.weeklyInviteLimit)"

        return [
            (language.text(ru: "Базовые функции", en: "Core features"), yes),
            (language.text(ru: "Инвайтов в неделю", en: "Invites per week"), weeklyInvites),
            (language.text(ru: "Группы до 35 участников", en: "Groups up to 35"), level.rawValue >= 3 ? yes : no),
            (language.text(ru: "Участие в тредах", en: "Thread participation"), level.rawValue >= 3 ? yes : no),
            (language.text(ru: "Создание частных каналов", en: "Private channels"), level.rawValue >= 4 ? yes : no),
            (language.text(ru: "Создание публичных каналов", en: "Public channels"), level.rawValue >= 6 ? yes : no),
            (language.text(ru: "Создание форум-тредов", en: "Forum thread creation"), level.rawValue >= 7 ? yes : no),
            (language.text(ru: "Безлимитные группы", en: "Unlimited groups"), level == .eight ? yes : no)
        ]
    }
}

private struct InviteBranchView: View {
    @EnvironmentObject private var store: AppStore
    let user: UserProfile

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                QGAvatarView(user: user, size: 38)
                VStack(alignment: .leading, spacing: 2) {
                    Text(user.displayName)
                        .font(.system(size: 17, weight: .bold))
                    Text(user.nickname)
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
                Spacer()
                TrustBadgeView(level: user.trustLevel)
            }

            HStack(spacing: 10) {
                Button("+1 Trust") { store.updateTrust(for: user.id, delta: 1) }
                    .buttonStyle(.bordered)
                Button("-1 Trust") { store.updateTrust(for: user.id, delta: -1) }
                    .buttonStyle(.bordered)
                Button("Бан ветки", role: .destructive) { store.banBranch(startingAt: user.id) }
                    .buttonStyle(.bordered)
            }
            .font(.system(size: 12, weight: .bold))

            let children = store.invitedChildren(of: user.id)
            if !children.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(children) { child in
                        HStack(alignment: .top, spacing: 10) {
                            Rectangle()
                                .fill(QGTheme.Palette.line)
                                .frame(width: 2)
                                .padding(.top, 6)
                            InviteBranchView(user: child)
                        }
                        .padding(.leading, 18)
                    }
                }
            }
        }
        .padding(18)
        .qgCardStyle(cornerRadius: 24)
    }
}

private struct InviteQRViewerSheet: View {
    @Environment(\.dismiss) private var dismiss
    let payload: String

    var body: some View {
        ZStack {
            Rectangle()
                .fill(.ultraThinMaterial)
                .ignoresSafeArea()

            VStack(spacing: 0) {
                HStack {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "chevron.left")
                            .font(.system(size: 16, weight: .bold))
                            .foregroundStyle(QGTheme.Palette.ink)
                            .frame(width: 36, height: 36)
                            .background(Circle().fill(.ultraThinMaterial))
                    }
                    .buttonStyle(.plain)
                    Spacer()
                }
                .padding(.bottom, 18)

                Spacer(minLength: 0)

                InviteQRCodeView(payload: payload, size: 260)
                    .frame(width: 330, height: 330)
                    .padding(10)
                    .background(
                        RoundedRectangle(cornerRadius: 32, style: .continuous)
                            .fill(.ultraThinMaterial)
                    )
                    .overlay(
                        RoundedRectangle(cornerRadius: 32, style: .continuous)
                            .stroke(QGTheme.Palette.line, lineWidth: 1)
                    )

                Spacer(minLength: 0)
            }
            .padding(QGTheme.pagePadding)
        }
    }
}
