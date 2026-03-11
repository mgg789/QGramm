import SwiftUI

struct WordmarkView: View {
    let title: String
    var size: CGFloat = 34

    var body: some View {
        Text(title)
            .font(.system(size: size, weight: .bold, design: .rounded))
            .foregroundStyle(QGTheme.wordmark)
            .lineLimit(1)
    }
}

struct QGAvatarView: View {
    let user: UserProfile?
    var size: CGFloat = 54
    var showOnlineRing: Bool = false

    var body: some View {
        ZStack {
            Circle()
                .fill(Color.white)
                .frame(width: size + (showOnlineRing ? 8 : 0), height: size + (showOnlineRing ? 8 : 0))

            if let user, let path = user.avatarLocalPath, let image = UIImage(contentsOfFile: path) {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFill()
                    .frame(width: size, height: size)
                    .clipShape(Circle())
            } else if let assetName = user?.avatarAssetName {
                Image(assetName)
                    .resizable()
                    .scaledToFill()
                    .frame(width: size, height: size)
                    .clipShape(Circle())
            } else {
                Circle()
                    .fill(QGTheme.wordmark)
                    .frame(width: size, height: size)
                    .overlay {
                        Text(initials)
                            .font(.system(size: size * 0.36, weight: .bold))
                            .foregroundStyle(.white)
                    }
            }
        }
        .overlay {
            if showOnlineRing {
                Circle()
                    .stroke(QGTheme.Palette.online, lineWidth: 3)
                    .frame(width: size + 8, height: size + 8)
            }
        }
    }

    private var initials: String {
        guard let user else { return "Q" }
        let first = user.firstName.first.map(String.init) ?? ""
        let last = user.lastName.first.map(String.init) ?? ""
        let value = first + last
        return value.isEmpty ? "Q" : value
    }
}

struct SearchField: View {
    let placeholder: String
    @Binding var text: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(QGTheme.Palette.secondary)
            TextField(placeholder, text: $text)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .foregroundStyle(QGTheme.Palette.ink)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .qgCardStyle(cornerRadius: 20, fill: QGTheme.Palette.searchFill)
    }
}

struct TrustBadgeView: View {
    @Environment(\.locale) private var locale
    let level: TrustLevel

    var body: some View {
        Text(level.title(language: appLanguage))
            .font(.system(size: 12, weight: .semibold, design: .rounded))
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(
                Capsule(style: .continuous)
                    .fill(QGTheme.Palette.accent.opacity(0.12))
            )
            .foregroundStyle(QGTheme.Palette.accent)
    }

    private var appLanguage: AppLanguage {
        locale.identifier.hasPrefix("en") ? .english : .russian
    }
}

struct EmptyStateView: View {
    let title: String
    let message: String
    let systemImage: String

    var body: some View {
        VStack(spacing: 14) {
            Image(systemName: systemImage)
                .font(.system(size: 28, weight: .medium))
                .foregroundStyle(QGTheme.Palette.accent)
            Text(title)
                .font(.system(size: 22, weight: .bold, design: .rounded))
                .foregroundStyle(QGTheme.Palette.ink)
                .multilineTextAlignment(.center)
            Text(message)
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(28)
        .frame(maxWidth: .infinity)
        .qgCardStyle()
    }
}

struct InviteQRCodeView: View {
    let payload: String
    var size: CGFloat = 128

    var body: some View {
        Group {
            if let image = QGMediaTools.qrImage(for: payload) {
                Image(uiImage: image)
                    .interpolation(.none)
                    .resizable()
                    .scaledToFit()
            } else {
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .fill(Color.white)
            }
        }
        .frame(width: size, height: size)
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 28, style: .continuous).fill(.white))
    }
}

struct FloatingDock: View {
    @Binding var selectedTab: RootTab
    private let tabs: [RootTab] = [.chats, .network, .calls, .settings]

    var body: some View {
        HStack(spacing: 6) {
            ForEach(tabs, id: \.self) { tab in
                Button {
                    selectedTab = tab
                } label: {
                    Image(systemName: systemName(for: tab))
                        .font(.system(size: 20, weight: .semibold))
                        .foregroundStyle(selectedTab == tab ? .white : Color.white.opacity(0.74))
                        .frame(maxWidth: .infinity, minHeight: 46)
                    .background(
                        RoundedRectangle(cornerRadius: 14, style: .continuous)
                            .fill(selectedTab == tab ? QGTheme.Palette.accent : .clear)
                    )
                }
                .buttonStyle(.plain)
                .contentShape(Rectangle())
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .frame(maxWidth: 250)
        .background(
            RoundedRectangle(cornerRadius: 24, style: .continuous)
                .fill(QGTheme.Palette.dock)
                .shadow(color: .black.opacity(0.35), radius: 24, y: 8)
        )
    }

    private func systemName(for tab: RootTab) -> String {
        switch tab {
        case .chats: return "bubble.left.and.bubble.right.fill"
        case .network: return "point.3.connected.trianglepath.dotted"
        case .calls: return "phone.arrow.up.right.fill"
        case .settings: return "gearshape.fill"
        }
    }
}

struct VideoPreviewBadge: View {
    let imagePath: String?

    var body: some View {
        ZStack {
            if let imagePath, let image = UIImage(contentsOfFile: imagePath) {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFill()
            } else {
                LinearGradient(
                    colors: [QGTheme.Palette.accent, QGTheme.Palette.accentBlue],
                    startPoint: .topLeading,
                    endPoint: .bottomTrailing
                )
            }

            Image(systemName: "play.fill")
                .font(.system(size: 24, weight: .bold))
                .foregroundStyle(.white)
                .shadow(radius: 8)
        }
        .frame(width: 74, height: 74)
        .clipShape(Circle())
    }
}
