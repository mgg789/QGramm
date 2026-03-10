import SwiftUI
import UIKit

struct NetworkView: View {
    @EnvironmentObject private var store: AppStore
    @State private var infoMessage = ""

    private var currentUser: UserProfile? { store.currentUser }
    private var invites: [InviteRecord] { store.invitesForCurrentUser() }

    var body: some View {
        ScrollView(showsIndicators: false) {
            VStack(alignment: .leading, spacing: 22) {
                header
                inviteSection
                reportsSection

                if currentUser?.isAdmin == true {
                    adminGraphSection
                }
            }
            .padding(.horizontal, QGTheme.pagePadding)
            .padding(.top, 24)
            .padding(.bottom, QGTheme.floatingBottomInset)
        }
        .qgScreenBackground()
        .alert("Сеть", isPresented: Binding(
            get: { !infoMessage.isEmpty },
            set: { if !$0 { infoMessage = "" } }
        )) {
            Button("ОК") { infoMessage = "" }
        } message: {
            Text(infoMessage)
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                WordmarkView(title: "Qgramm", size: 38)
                Spacer()
                if let currentUser {
                    TrustBadgeView(level: currentUser.trustLevel)
                }
            }

            if let currentUser {
                VStack(alignment: .leading, spacing: 12) {
                    Text("Сеть и доверие")
                        .font(.system(size: 26, weight: .bold, design: .rounded))
                        .foregroundStyle(QGTheme.Palette.ink)
                    Text("Инвайтов в неделю: \(currentUser.trustLevel.weeklyInviteLimit == .max ? "без лимита" : "\(currentUser.trustLevel.weeklyInviteLimit)")")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.secondary)
                    Text("Админы видят граф приглашений и могут забанить всю ветку, если пользователь нарушает правила.")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }
                .padding(22)
                .qgCardStyle()
            }
        }
    }

    private var inviteSection: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Инвайты")
                    .font(.system(size: 22, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.ink)
                Spacer()
                Button("Создать") {
                    switch store.generateInvite() {
                    case let .success(invite):
                        infoMessage = "Создан инвайт \(invite.code)"
                    case let .failure(error):
                        infoMessage = error.localizedDescription
                    }
                }
                .font(.system(size: 16, weight: .bold))
            }

            if invites.isEmpty {
                EmptyStateView(
                    title: "Пока нет активных инвайтов",
                    message: "Повышение trust level открывает право приглашать новых пользователей.",
                    systemImage: "person.badge.plus"
                )
            } else {
                ForEach(invites) { invite in
                    VStack(alignment: .leading, spacing: 14) {
                        HStack(alignment: .top) {
                            InviteQRCodeView(payload: invite.deepLink)

                            VStack(alignment: .leading, spacing: 8) {
                                Text(invite.code)
                                    .font(.system(size: 18, weight: .bold, design: .monospaced))
                                    .foregroundStyle(QGTheme.Palette.ink)
                                Text("Ссылка: \(invite.deepLink)")
                                    .font(.system(size: 13, weight: .medium))
                                    .foregroundStyle(QGTheme.Palette.secondary)
                                Text(invite.isActive ? "Активен до \(QGFormatters.dayTitle.string(from: invite.expiresAt))" : "Использован")
                                    .font(.system(size: 13, weight: .bold))
                                    .foregroundStyle(invite.isActive ? QGTheme.Palette.online : QGTheme.Palette.secondary)
                            }
                        }

                        HStack(spacing: 12) {
                            Button("Копировать код") {
                                UIPasteboard.general.string = invite.code
                                infoMessage = "Код скопирован."
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(QGTheme.Palette.accent)

                            Button("Копировать ссылку") {
                                UIPasteboard.general.string = invite.deepLink
                                infoMessage = "Ссылка скопирована."
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

    private var reportsSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Жалобы")
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            if store.state.reports.isEmpty {
                EmptyStateView(
                    title: "Жалоб пока нет",
                    message: "Сообщения, на которые пожаловались пользователи, будут появляться здесь для админов.",
                    systemImage: "checkmark.shield"
                )
            } else {
                ForEach(store.state.reports) { report in
                    VStack(alignment: .leading, spacing: 8) {
                        Text(report.reason.title)
                            .font(.system(size: 17, weight: .bold))
                            .foregroundStyle(QGTheme.Palette.ink)
                        Text(report.note.isEmpty ? "Без комментария" : report.note)
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(QGTheme.Palette.secondary)
                        Text(QGFormatters.dayTitle.string(from: report.createdAt))
                            .font(.system(size: 12, weight: .bold))
                            .foregroundStyle(QGTheme.Palette.accent)
                    }
                    .padding(18)
                    .qgCardStyle(cornerRadius: 24)
                }
            }
        }
    }

    private var adminGraphSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Граф инвайтов")
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)

            ForEach(store.inviteTreeRoots()) { root in
                InviteBranchView(user: root)
            }
        }
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
                Button("+1 trust") { store.updateTrust(for: user.id, delta: 1) }
                    .buttonStyle(.bordered)
                Button("-1 trust") { store.updateTrust(for: user.id, delta: -1) }
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
