import AVFoundation
import AVKit
import PhotosUI
import SwiftUI

struct ChatListView: View {
    @EnvironmentObject private var store: AppStore
    @State private var searchText = ""
    @State private var path: [UUID] = []
    @State private var infoMessage = ""
    @State private var isResolvingNickname = false

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView(showsIndicators: false) {
                LazyVStack(alignment: .leading, spacing: 16, pinnedViews: [.sectionHeaders]) {
                    header
                    SearchField(
                        placeholder: language.text(ru: "Найти чат или @nickname", en: "Find chat or @nickname"),
                        text: $searchText
                    )

                    Section {
                        conversationList
                    } header: {
                        segmentPicker
                            .padding(.vertical, 8)
                            .background {
                                Rectangle()
                                    .fill(QGTheme.Palette.screen.opacity(0.95))
                                    .blur(radius: 8)
                            }
                    }
                }
                .padding(.horizontal, QGTheme.pagePadding)
                .padding(.top, 10)
                .padding(.bottom, QGTheme.floatingBottomInset)
            }
            .navigationDestination(for: UUID.self) { conversationID in
                if let conversation = store.conversation(id: conversationID) {
                    ChatRoomView(conversationID: conversation.id)
                } else {
                    EmptyStateView(
                        title: "Чат не найден",
                        message: "Похоже, диалог был удалён или недоступен.",
                        systemImage: "bubble.left.and.exclamationmark.bubble.right"
                    )
                    .padding(QGTheme.pagePadding)
                    .qgScreenBackground()
                }
            }
            .qgScreenBackground()
        }
        .ignoresSafeArea(edges: .bottom)
        .alert(language.text(ru: "Чаты", en: "Chats"), isPresented: Binding(
            get: { !infoMessage.isEmpty },
            set: { if !$0 { infoMessage = "" } }
        )) {
            Button(language.text(ru: "ОК", en: "OK")) { infoMessage = "" }
        } message: {
            Text(infoMessage)
        }
        .onChange(of: path) { _, newPath in
            DispatchQueue.main.async {
                store.setChatRoomOpen(!newPath.isEmpty)
            }
        }
        .onDisappear {
            store.setChatRoomOpen(false)
        }
    }

    private var header: some View {
        HStack(spacing: 10) {
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
            if let user = store.currentUser {
                TrustBadgeView(level: user.trustLevel)
            }
        }
    }

    private var segmentPicker: some View {
        HStack(spacing: 10) {
            ForEach(ConversationGroup.allCases) { group in
                Button {
                    store.selectConversationGroup(group)
                } label: {
                    Text(group.title(language: language))
                        .font(.system(size: 14, weight: .bold, design: .rounded))
                        .foregroundStyle(store.state.selectedConversationGroup == group ? .white : QGTheme.Palette.secondary)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(
                            RoundedRectangle(cornerRadius: 14, style: .continuous)
                                .fill(store.state.selectedConversationGroup == group ? QGTheme.Palette.accent : Color.clear)
                        )
                        .qgPillBorder(selected: store.state.selectedConversationGroup == group)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.top, 2)
        .animation(
            store.state.session.isPowerSavingEnabled ? nil : .spring(response: 0.24, dampingFraction: 0.9),
            value: store.state.selectedConversationGroup
        )
        .contentShape(Rectangle())
        .gesture(groupSegmentSwipeGesture)
    }

    private var conversationList: some View {
        if store.state.selectedConversationGroup != .chats {
            return AnyView(
                FutureAvailabilityBadge(
                    text: language.text(
                        ru: "Это будет доступно в будущих версиях",
                        en: "This will be available in future versions"
                    )
                )
                .padding(.top, 4)
            )
        }

        let conversations = store.filteredConversations(query: searchText, group: store.state.selectedConversationGroup)
        let discoverable = store.discoverableUser(for: searchText)
        let handleQuery = normalizedHandleQuery

        return AnyView(VStack(alignment: .leading, spacing: 12) {
            if let discoverable {
                Button {
                    let id = store.startConversation(with: discoverable.id)
                    path.append(id)
                } label: {
                    HStack(spacing: 14) {
                        QGAvatarView(user: discoverable, size: 52, showOnlineRing: discoverable.presence == .online)
                        VStack(alignment: .leading, spacing: 4) {
                            Text(language.text(ru: "Написать \(discoverable.displayName)", en: "Message \(discoverable.displayName)"))
                                .font(.system(size: 18, weight: .bold, design: .rounded))
                                .foregroundStyle(QGTheme.Palette.ink)
                            Text(discoverable.nickname)
                                .font(.system(size: 14, weight: .semibold))
                                .foregroundStyle(QGTheme.Palette.accent)
                        }
                        Spacer()
                        Image(systemName: "square.and.pencil")
                            .font(.system(size: 18, weight: .semibold))
                            .foregroundStyle(QGTheme.Palette.accent)
                    }
                    .padding(18)
                    .qgCardStyle(cornerRadius: 26)
                }
                .buttonStyle(.plain)
            } else if let handleQuery {
                Button {
                    guard !isResolvingNickname else { return }
                    isResolvingNickname = true
                    Task { @MainActor in
                        defer { isResolvingNickname = false }
                        switch await store.startConversation(byNickname: handleQuery) {
                        case let .success(conversationID):
                            path.append(conversationID)
                        case let .failure(error):
                            infoMessage = error.localizedDescription
                        }
                    }
                } label: {
                    HStack(spacing: 14) {
                        Circle()
                            .fill(QGTheme.Palette.cardFill)
                            .frame(width: 52, height: 52)
                            .overlay(
                                Image(systemName: "at")
                                    .font(.system(size: 18, weight: .bold))
                                    .foregroundStyle(QGTheme.Palette.accent)
                            )

                        VStack(alignment: .leading, spacing: 4) {
                            Text(language.text(ru: "Написать \(handleQuery)", en: "Message \(handleQuery)"))
                                .font(.system(size: 18, weight: .bold, design: .rounded))
                                .foregroundStyle(QGTheme.Palette.ink)
                            Text(language.text(
                                ru: "Найдём пользователя на сервере и создадим чат",
                                en: "Find user on server and create chat"
                            ))
                            .font(.system(size: 14, weight: .semibold))
                            .foregroundStyle(QGTheme.Palette.secondary)
                        }

                        Spacer()

                        if isResolvingNickname {
                            ProgressView()
                                .controlSize(.small)
                        } else {
                            Image(systemName: "arrow.up.forward.app")
                                .font(.system(size: 18, weight: .semibold))
                                .foregroundStyle(QGTheme.Palette.accent)
                        }
                    }
                    .padding(18)
                    .qgCardStyle(cornerRadius: 26)
                }
                .buttonStyle(.plain)
            }

            if conversations.isEmpty {
                EmptyStateView(
                    title: language.text(ru: "Пусто", en: "Empty"),
                    message: language.text(
                        ru: "По этому фильтру ничего не найдено. Попробуйте другой сегмент или начните диалог по @nickname.",
                        en: "Nothing found for this filter. Try another segment or start chat by @nickname."
                    ),
                    systemImage: "magnifyingglass.circle"
                )
            } else {
                ForEach(conversations) { conversation in
                    Button {
                        path.append(conversation.id)
                    } label: {
                        ChatRowView(conversation: conversation, searchText: searchText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .contentShape(Rectangle())

                    Divider()
                        .background(QGTheme.Palette.line)
                        .padding(.leading, 74)
                }
            }
        })
    }

    private var language: AppLanguage {
        store.state.session.language
    }

    private var normalizedHandleQuery: String? {
        let normalized = searchText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard normalized.hasPrefix("@"), normalized.count > 1 else { return nil }
        return normalized
    }

    private var groupSegmentSwipeGesture: some Gesture {
        DragGesture(minimumDistance: 12, coordinateSpace: .local)
            .onEnded { value in
                let horizontal = value.predictedEndTranslation.width
                let vertical = value.predictedEndTranslation.height
                guard abs(horizontal) > 44 else { return }
                guard abs(horizontal) > abs(vertical) * 1.25 else { return }

                let groups = ConversationGroup.allCases
                guard let currentIndex = groups.firstIndex(of: store.state.selectedConversationGroup) else { return }

                if horizontal < 0, currentIndex < groups.count - 1 {
                    store.selectConversationGroup(groups[currentIndex + 1])
                    QGHaptics.light()
                } else if horizontal > 0, currentIndex > 0 {
                    store.selectConversationGroup(groups[currentIndex - 1])
                    QGHaptics.light()
                }
            }
    }
}

private struct FutureAvailabilityBadge: View {
    let text: String

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "sparkles.rectangle.stack")
                .font(.system(size: 18, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.accent)
            Text(text)
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.ink)
            Spacer()
        }
        .padding(16)
        .qgCardStyle(cornerRadius: 20)
    }
}

private struct ChatRowView: View {
    @EnvironmentObject private var store: AppStore
    let conversation: ConversationRecord
    let searchText: String

    var body: some View {
        HStack(alignment: .top, spacing: 16) {
            let peer = peerUser
            QGAvatarView(
                user: peer,
                size: 54,
                showOnlineRing: conversation.category == .chats && conversation.onlineUserID != nil
            )

            VStack(alignment: .leading, spacing: 6) {
                Text(conversation.title)
                    .font(.system(size: 20, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.ink)
                    .multilineTextAlignment(.leading)

                Text(store.conversationSubtitle(conversation))
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(previewColor)
                    .lineLimit(2)
            }

            Spacer(minLength: 12)

            VStack(alignment: .trailing, spacing: 10) {
                if let last = conversation.messages.sorted(by: { $0.sentAt < $1.sentAt }).last {
                    Text(QGFormatters.messageTime.string(from: last.sentAt))
                        .font(.system(size: 14, weight: .bold))
                        .foregroundStyle(conversation.unreadCount > 0 ? QGTheme.Palette.accent : QGTheme.Palette.secondary)
                }

                if conversation.unreadCount > 0 {
                    Text("\(conversation.unreadCount)")
                        .font(.system(size: 12, weight: .bold))
                        .foregroundStyle(.white)
                        .frame(width: 24, height: 24)
                        .background(Circle().fill(QGTheme.Palette.accent))
                }
            }
        }
        .padding(.vertical, 10)
        .contentShape(Rectangle())
    }

    private var peerUser: UserProfile? {
        conversation.participantIDs
            .filter { $0 != store.state.session.currentUserID }
            .compactMap(store.user(id:))
            .first
    }

    private var previewColor: Color {
        if let peerUser, peerUser.presence == .typing {
            return QGTheme.Palette.online
        }
        return QGTheme.Palette.muted
    }
}

private struct MessageFramePreferenceKey: PreferenceKey {
    static var defaultValue: [UUID: CGRect] = [:]

    static func reduce(value: inout [UUID: CGRect], nextValue: () -> [UUID: CGRect]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

private struct HeaderFramePreferenceKey: PreferenceKey {
    static var defaultValue: CGRect = .zero

    static func reduce(value: inout CGRect, nextValue: () -> CGRect) {
        let candidate = nextValue()
        if candidate != .zero {
            value = candidate
        }
    }
}

struct ChatRoomView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.colorScheme) private var colorScheme
    @EnvironmentObject private var store: AppStore
    @StateObject private var audio = AudioRecorderService()
    @StateObject private var videoRecorder = CircularVideoRecorderService()

    let conversationID: UUID

    private enum CaptureMode {
        case audio
        case video
    }

    @State private var draftText = ""
    @State private var selectedMessageID: UUID?
    @State private var replyMessageID: UUID?
    @State private var showAttachmentOptions = false
    @State private var showMediaPicker = false
    @State private var showFileImporter = false
    @State private var mediaPickerItem: PhotosPickerItem?
    @State private var reportMessageID: UUID?
    @State private var forwardMessageID: UUID?
    @State private var reportReason: ReportReason = .spam
    @State private var reportNote = ""
    @State private var previewVideoURL: URL?
    @State private var infoMessage = ""
    @State private var captureMode: CaptureMode = .audio
    @State private var useFrontCamera = true
    @State private var jumpToMessageID: UUID?
    @State private var isNearBottom = true
    @State private var previewCircularAttachment: MessageAttachment?
    @State private var previewMediaAttachment: MessageAttachment?
    @State private var showPeerProfile = false
    @State private var didTriggerCaptureLongPress = false
    @State private var messageFrames: [UUID: CGRect] = [:]
    @State private var headerFrame: CGRect = .zero
    @State private var forwardingNotice = ""

    private var conversation: ConversationRecord {
        store.conversation(id: conversationID) ?? ConversationRecord(
            id: conversationID,
            category: .chats,
            title: "Чат",
            participantIDs: [],
            sharedKey: "",
            lastActivityAt: .now,
            unreadCount: 0,
            onlineUserID: nil,
            messages: []
        )
    }

    private var peerUser: UserProfile? {
        conversation.participantIDs
            .filter { $0 != store.state.session.currentUserID }
            .compactMap(store.user(id:))
            .first
    }

    private var sortedMessages: [MessageRecord] {
        conversation.messages.sorted(by: { $0.sentAt < $1.sentAt })
    }

    var body: some View {
        ZStack {
            chatBackground

            VStack(spacing: 0) {
                topBar

                ScrollViewReader { proxy in
                    ScrollView(showsIndicators: false) {
                        LazyVStack(spacing: 16) {
                            ForEach(sortedMessages) { message in
                                messageRow(for: message)
                                    .onAppear {
                                        if message.id == sortedMessages.last?.id {
                                            isNearBottom = true
                                        }
                                    }
                                    .onDisappear {
                                        if message.id == sortedMessages.last?.id {
                                            isNearBottom = false
                                        }
                                    }
                            }
                        }
                        .padding(.horizontal, QGTheme.pagePadding)
                        .padding(.top, 18)
                        .padding(.bottom, selectedMessageID == nil ? 28 : 128)
                    }
                    .onChange(of: sortedMessages.count) { _, _ in
                        if let last = sortedMessages.last?.id {
                            qgAnimate(.spring(response: 0.28, dampingFraction: 0.9)) {
                                proxy.scrollTo(last, anchor: .bottom)
                            }
                        }
                    }
                    .onChange(of: jumpToMessageID) { _, target in
                        guard let target else { return }
                        qgAnimate(.spring(response: 0.34, dampingFraction: 0.9)) {
                            let anchor: UnitPoint = target == sortedMessages.last?.id ? .bottom : .center
                            proxy.scrollTo(target, anchor: anchor)
                        }
                        jumpToMessageID = nil
                    }
                    .simultaneousGesture(
                        TapGesture()
                            .onEnded {
                                if selectedMessageID != nil {
                                    selectedMessageID = nil
                                }
                            }
                    )
                }

                if let reply = replyMessageID, let source = sortedMessages.first(where: { $0.id == reply }) {
                    draftBanner(
                        title: store.user(id: source.senderID)?.firstName ?? language.text(ru: "Ответ", en: "Reply"),
                        text: previewText(for: source)
                    ) {
                        replyMessageID = nil
                    }
                }

                composer
            }
        }
        .contentShape(Rectangle())
        .onTapGesture {
            if selectedMessageID != nil {
                selectedMessageID = nil
            }
        }
        .overlay {
            if videoRecorder.isRecording {
                circularRecordingOverlay
                    .allowsHitTesting(false)
                    .transition(.opacity)
            }
        }
        .overlay(alignment: .bottom) {
            if audio.isRecording || videoRecorder.isRecording {
                recordingStatusBar
                    .padding(.horizontal, QGTheme.pagePadding)
                    .padding(.bottom, 72)
                    .zIndex(40)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .overlay(alignment: .bottomTrailing) {
            if !isNearBottom {
                Button {
                    if let last = sortedMessages.last?.id {
                        qgAnimate(.spring(response: 0.32, dampingFraction: 0.88)) {
                            jumpToMessageID = last
                        }
                    }
                } label: {
                    Image(systemName: "arrow.down")
                        .font(.system(size: 15, weight: .bold))
                        .foregroundStyle(.white)
                        .frame(width: 36, height: 36)
                        .background(Circle().fill(QGTheme.Palette.accent))
                        .shadow(color: .black.opacity(0.22), radius: 10, y: 4)
                }
                .buttonStyle(.plain)
                .padding(.trailing, QGTheme.pagePadding)
                .padding(.bottom, QGTheme.floatingBottomInset + 12)
                .transition(.scale.combined(with: .opacity))
            }
        }
        .overlay(alignment: .top) {
            if !forwardingNotice.isEmpty {
                Text(forwardingNotice)
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.ink)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(.ultraThinMaterial, in: Capsule())
                    .overlay(
                        Capsule()
                            .stroke(QGTheme.Palette.line, lineWidth: 1)
                    )
                    .padding(.top, 88)
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
        }
        .qgScreenBackground()
        .navigationBarBackButtonHidden(true)
        .simultaneousGesture(chatDismissGesture)
        .onPreferenceChange(MessageFramePreferenceKey.self) { frames in
            messageFrames = frames
        }
        .onPreferenceChange(HeaderFramePreferenceKey.self) { frame in
            headerFrame = frame
        }
        .photosPicker(
            isPresented: $showMediaPicker,
            selection: $mediaPickerItem,
            matching: .any(of: [.images, .videos]),
            preferredItemEncoding: .automatic
        )
        .confirmationDialog(
            language.text(ru: "Добавить вложение", en: "Add attachment"),
            isPresented: $showAttachmentOptions,
            titleVisibility: .visible
        ) {
            Button(language.text(ru: "Фото / Видео", en: "Photo / Video")) {
                showMediaPicker = true
            }
            Button(language.text(ru: "Файл", en: "File")) {
                showFileImporter = true
            }
            Button(language.text(ru: "Отмена", en: "Cancel"), role: .cancel) {}
        }
        .fileImporter(
            isPresented: $showFileImporter,
            allowedContentTypes: [.data, .content, .item],
            allowsMultipleSelection: false
        ) { result in
            if case let .success(urls) = result, let url = urls.first {
                store.queueAttachmentMessage(
                    from: url,
                    previewURL: nil,
                    kind: .file,
                    duration: nil,
                    in: conversationID,
                    replyTo: replyMessageID,
                    quotedExcerpt: nil
                )
                clearDraftActions()
            }
        }
        .task(id: mediaPickerItem) {
            guard let mediaPickerItem else { return }
            defer { self.mediaPickerItem = nil }
            guard let data = try? await mediaPickerItem.loadTransferable(type: Data.self) else { return }

            let isVideo = mediaPickerItem.supportedContentTypes.contains { $0.conforms(to: .movie) }
            let tempURL = FileManager.default.temporaryDirectory
                .appendingPathComponent(UUID().uuidString)
                .appendingPathExtension(isVideo ? "mov" : "jpg")
            try? data.write(to: tempURL)

            let previewURL: URL?
            if isVideo {
                previewURL = try? await QGMediaTools.makeThumbnail(for: tempURL)
            } else {
                previewURL = nil
            }

            store.queueAttachmentMessage(
                from: tempURL,
                previewURL: previewURL,
                kind: .media,
                duration: nil,
                in: conversationID,
                replyTo: replyMessageID,
                quotedExcerpt: nil
            )
            clearDraftActions()
        }
        .sheet(isPresented: Binding(
            get: { forwardMessageID != nil },
            set: { if !$0 { forwardMessageID = nil } }
        )) {
            NavigationStack {
                List {
                    ForEach(store.state.conversations.filter { $0.id != conversationID }) { target in
                        Button(target.title) {
                            if let forwardMessageID {
                                store.forward(messageID: forwardMessageID, from: conversationID, to: target.id)
                                QGHaptics.medium()
                                showForwardNotice(targetTitle: target.title)
                            }
                            forwardMessageID = nil
                            selectedMessageID = nil
                        }
                    }
                }
                .navigationTitle(language.text(ru: "Переслать", en: "Forward"))
                .navigationBarTitleDisplayMode(.inline)
            }
            .presentationDetents([.medium, .large])
        }
        .sheet(isPresented: Binding(
            get: { reportMessageID != nil },
            set: { if !$0 { reportMessageID = nil } }
        )) {
            NavigationStack {
                VStack(alignment: .leading, spacing: 18) {
                    Text(language.text(ru: "Причина", en: "Reason"))
                        .font(.system(size: 16, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.secondary)

                    VStack(spacing: 8) {
                        ForEach(ReportReason.allCases) { reason in
                            Button {
                                reportReason = reason
                            } label: {
                                HStack(spacing: 10) {
                                    Image(systemName: reportReason == reason ? "checkmark.circle.fill" : "circle")
                                        .font(.system(size: 18, weight: .semibold))
                                        .foregroundStyle(reportReason == reason ? QGTheme.Palette.accent : QGTheme.Palette.secondary)
                                    Text(reason.title)
                                        .font(.system(size: 16, weight: .semibold))
                                        .foregroundStyle(QGTheme.Palette.ink)
                                    Spacer()
                                }
                                .padding(.horizontal, 14)
                                .padding(.vertical, 10)
                                .qgCardStyle(cornerRadius: 16)
                            }
                            .buttonStyle(.plain)
                        }
                    }

                    TextField(language.text(ru: "Комментарий для админов", en: "Comment for admins"), text: $reportNote, axis: .vertical)
                        .lineLimit(3...5)
                        .padding(16)
                        .qgCardStyle(cornerRadius: 24)

                    Button {
                        if let reportMessageID {
                            let didSend = store.reportMessage(
                                messageID: reportMessageID,
                                conversationID: conversationID,
                                reason: reportReason,
                                note: reportNote
                            )
                            if !didSend {
                                infoMessage = language.text(
                                    ru: "Жалоба на это сообщение уже отправлена.",
                                    en: "You already reported this message."
                                )
                            } else {
                                infoMessage = language.text(
                                    ru: "Жалоба отправлена.",
                                    en: "Report sent."
                                )
                            }
                        }
                        reportReason = .spam
                        reportNote = ""
                        reportMessageID = nil
                        selectedMessageID = nil
                    } label: {
                        Text(language.text(ru: "Отправить жалобу", en: "Send report"))
                            .font(.system(size: 17, weight: .bold))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 16)
                            .background(RoundedRectangle(cornerRadius: 22, style: .continuous).fill(QGTheme.Palette.destructiveSurface))
                            .foregroundStyle(QGTheme.Palette.destructive)
                    }
                    .buttonStyle(.plain)

                    Spacer()
                }
                .padding(QGTheme.pagePadding)
                .navigationTitle(language.text(ru: "Жалоба", en: "Report"))
                .navigationBarTitleDisplayMode(.inline)
            }
            .presentationDetents([.medium, .large])
        }
        .sheet(isPresented: Binding(
            get: { previewVideoURL != nil },
            set: { if !$0 { previewVideoURL = nil } }
        )) {
            if let previewVideoURL {
                VideoPreviewSheet(url: previewVideoURL)
            }
        }
        .sheet(item: $previewCircularAttachment) { attachment in
            CircularVideoViewerSheet(attachment: attachment)
                .presentationBackground(.clear)
        }
        .sheet(item: $previewMediaAttachment) { attachment in
            MediaViewerSheet(attachment: attachment)
                .presentationBackground(.clear)
        }
        .sheet(isPresented: $showPeerProfile) {
            if let peerUser {
                ChatPeerProfileSheet(
                    user: peerUser,
                    language: language,
                    onReport: {
                        infoMessage = language.text(
                            ru: "Жалоба на пользователя отправлена.",
                            en: "User report sent."
                        )
                    },
                    onBlock: {
                        store.blockUser(peerUser.id)
                        dismiss()
                    }
                )
            }
        }
        .onAppear {
            store.openConversation(conversationID)
        }
        .onDisappear {
            store.closeConversation(conversationID)
            if audio.isRecording {
                cancelVoiceRecording()
            }
            if videoRecorder.isRecording {
                Task { @MainActor in
                    await cancelVideoRecording()
                }
            }
        }
        .onChange(of: audio.isRecording) { _, isRecording in
            if isRecording {
                QGHaptics.forceHeavy()
            }
        }
    }

    private var topBar: some View {
        HStack(spacing: 14) {
            Button {
                dismiss()
            } label: {
                Image(systemName: "chevron.left")
                    .font(.system(size: 18, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.ink)
                    .frame(width: 42, height: 42)
                    .background(Circle().fill(QGTheme.Palette.cardFill))
            }

            Button {
                showPeerProfile = true
                QGHaptics.light()
            } label: {
                HStack(spacing: 12) {
                    QGAvatarView(user: peerUser, size: 44, showOnlineRing: false)

                    VStack(alignment: .leading, spacing: 3) {
                        Text(headerDisplayName)
                            .font(.system(size: 19, weight: .bold, design: .rounded))
                            .foregroundStyle(QGTheme.Palette.ink)
                            .lineLimit(1)
                            .truncationMode(.tail)
                        Text(headerPresenceText)
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(headerPresenceColor)
                    }
                }
            }
            .buttonStyle(.plain)

            Spacer()

            Button {
                if let peer = peerUser {
                    store.startCall(with: peer.id, kind: .audio)
                }
            } label: {
                Image(systemName: "phone.arrow.up.right.fill")
                    .font(.system(size: 17, weight: .bold))
                    .foregroundStyle(.white)
                    .frame(width: 42, height: 42)
                    .background(Circle().fill(QGTheme.Palette.accent))
            }

            Menu {
                Button(language.text(ru: "Инфо о шифровании", en: "Encryption info")) {
                    infoMessage = store.state.session.safetyMode == .classic
                        ? language.text(
                            ru: "Classic: E2E для сообщений, голосовых и файлов. Метаданные инвайтов и жалоб доступны админам.",
                            en: "Classic: E2E for messages, voice and files. Invite/report metadata is visible to admins."
                        )
                        : language.text(
                            ru: "Local: приоритет локальных P2P-сценариев и локального хранения.",
                            en: "Local: prioritizes local P2P scenarios and local storage."
                        )
                }
            } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 20, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.secondary)
            }
        }
        .padding(.horizontal, QGTheme.pagePadding)
        .padding(.top, 10)
        .padding(.bottom, 10)
        .background(
            RoundedRectangle(cornerRadius: 24, style: .continuous)
                .fill(QGTheme.Palette.headerSurface)
                .overlay(
                    RoundedRectangle(cornerRadius: 24, style: .continuous)
                        .stroke(QGTheme.Palette.line, lineWidth: 1)
                )
        )
        .padding(.horizontal, 8)
        .padding(.top, 2)
        .background(
            GeometryReader { geometry in
                Color.clear
                    .preference(key: HeaderFramePreferenceKey.self, value: geometry.frame(in: .global))
            }
        )
        .alert(language.text(ru: "Шифрование", en: "Encryption"), isPresented: Binding(
            get: { !infoMessage.isEmpty },
            set: { if !$0 { infoMessage = "" } }
        )) {
            Button(language.text(ru: "ОК", en: "OK")) { infoMessage = "" }
        } message: {
            Text(infoMessage)
        }
    }

    private var composer: some View {
        HStack(alignment: .center, spacing: 10) {
            Button {
                showAttachmentOptions = true
            } label: {
                Image(systemName: "plus")
                    .font(.system(size: 18, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.accent)
                    .frame(width: 42, height: 42)
                    .background(Circle().fill(QGTheme.Palette.cardFill))
            }
            .buttonStyle(.plain)

            if audio.isRecording || videoRecorder.isRecording {
                Button {
                    if audio.isRecording {
                        cancelVoiceRecording()
                    } else {
                        Task { @MainActor in
                            await cancelVideoRecording()
                        }
                    }
                } label: {
                    Text(language.text(ru: "Отменить", en: "Cancel"))
                        .font(.system(size: 16, weight: .bold, design: .rounded))
                        .foregroundStyle(QGTheme.Palette.destructive)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                        .qgCardStyle(cornerRadius: 20, fill: QGTheme.Palette.cardFill)
                }
                .buttonStyle(.plain)
            } else {
                TextField(language.text(ru: "Сообщение", en: "Message"), text: $draftText, axis: .vertical)
                    .lineLimit(1...3)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 10)
                    .qgCardStyle(cornerRadius: 20)
            }

            Button {
                if didTriggerCaptureLongPress {
                    didTriggerCaptureLongPress = false
                    return
                }

                if audio.isRecording {
                    sendVoiceRecording()
                } else if videoRecorder.isRecording {
                    Task { @MainActor in
                        await sendCircularVideo()
                    }
                } else if !draftText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    store.sendTextMessage(
                        draftText,
                        replyTo: replyMessageID,
                        quotedExcerpt: nil,
                        in: conversationID
                    )
                    draftText = ""
                    clearDraftActions()
                } else {
                    qgAnimate(.spring(response: 0.2, dampingFraction: 0.95)) {
                        captureMode = captureMode == .audio ? .video : .audio
                    }
                    QGHaptics.light()
                }
            } label: {
                Image(systemName: actionSymbol)
                    .font(.system(size: 18, weight: .bold))
                    .foregroundStyle(.white)
                    .frame(width: 48, height: 48)
                    .background(Circle().fill(QGTheme.Palette.accent))
            }
            .buttonStyle(.plain)
            .highPriorityGesture(
                LongPressGesture(minimumDuration: 0.35, maximumDistance: 34)
                    .onEnded { _ in
                        didTriggerCaptureLongPress = startCaptureRecording()
                    }
            )
        }
        .padding(.horizontal, QGTheme.pagePadding)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .background(
            Rectangle()
                .fill(QGTheme.Palette.headerSurface.opacity(0.92))
                .ignoresSafeArea(edges: .bottom)
        )
        .overlay(alignment: .top) {
            Rectangle()
                .fill(QGTheme.Palette.line)
                .frame(height: 1)
        }
    }

    private func draftBanner(title: String, text: String, close: @escaping () -> Void) -> some View {
        HStack(spacing: 12) {
            Rectangle()
                .fill(QGTheme.Palette.accent)
                .frame(width: 3)
                .clipShape(Capsule())
            VStack(alignment: .leading, spacing: 4) {
                Text(title)
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.accent)
                Text(text)
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.ink)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            Spacer()
            Button(action: close) {
                Image(systemName: "xmark")
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.secondary)
                    .frame(width: 28, height: 28)
                    .background(Circle().fill(.white.opacity(0.85)))
            }
        }
        .padding(.horizontal, QGTheme.pagePadding)
        .padding(.vertical, 8)
        .frame(height: 58)
        .background(QGTheme.Palette.cardFill.opacity(0.96))
    }

    private func messageActions(for message: MessageRecord) -> some View {
        HStack(spacing: 8) {
            Button(language.text(ru: "Ответ", en: "Reply")) {
                replyMessageID = message.id
                selectedMessageID = nil
            }
            .buttonStyle(.borderedProminent)
            .tint(QGTheme.Palette.accent)

            Button(language.text(ru: "Переслать", en: "Forward")) {
                forwardMessageID = message.id
                selectedMessageID = nil
            }
            .buttonStyle(.bordered)

            Button(language.text(ru: "Жалоба", en: "Report")) {
                reportMessageID = message.id
                selectedMessageID = nil
            }
            .buttonStyle(.bordered)
            .tint(QGTheme.Palette.destructive)
        }
        .font(.system(size: 13, weight: .bold))
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 22, style: .continuous)
                .stroke(QGTheme.Palette.line, lineWidth: 1)
        )
    }

    private var actionSymbol: String {
        if audio.isRecording || videoRecorder.isRecording {
            return "arrow.up"
        }
        if !draftText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "arrow.up"
        }
        return captureMode == .audio ? "mic.fill" : "video.fill"
    }

    @ViewBuilder
    private func messageRow(for message: MessageRecord) -> some View {
        let replyPreview = message.replyToMessageID.flatMap { id in
            sortedMessages.first(where: { $0.id == id }).map { source in
                ReplyPreviewPayload(
                    id: source.id,
                    senderName: store.user(id: source.senderID)?.firstName ?? language.text(ru: "Пользователь", en: "User"),
                    text: previewText(for: source)
                )
            }
        }
        let shouldRenderActions = selectedMessageID == message.id
        let placeActionsBelowMessage = shouldShowActionsBelow(for: message)

        if shouldRenderActions, !placeActionsBelowMessage {
            messageActions(for: message)
                .padding(.bottom, 6)
        }

        MessageBubbleView(
            message: message,
            text: store.decryptedText(for: message, in: conversation),
            replyPreview: replyPreview,
            sender: store.user(id: message.senderID),
            isMine: message.senderID == store.state.session.currentUserID,
            showSenderName: conversation.category != .chats,
            audioService: audio,
            onReplyPreviewTap: { target in
                qgAnimate(.spring(response: 0.32, dampingFraction: 0.9)) {
                    jumpToMessageID = target
                }
            },
            onOpenMedia: { attachment in
                previewMediaAttachment = attachment
            },
            onOpenCircularVideo: { attachment in
                previewCircularAttachment = attachment
            }
        )
        .id(message.id)
        .contentShape(Rectangle())
        .background(
            GeometryReader { geometry in
                Color.clear.preference(
                    key: MessageFramePreferenceKey.self,
                    value: [message.id: geometry.frame(in: .global)]
                )
            }
        )
        .simultaneousGesture(
            LongPressGesture(minimumDuration: 0.35)
                .onEnded { _ in
                    toggleMessageActions(for: message.id)
                }
        )
        .simultaneousGesture(
            TapGesture(count: 2)
                .onEnded {
                    forwardMessageID = message.id
                    selectedMessageID = nil
                    QGHaptics.medium()
                }
        )
        .simultaneousGesture(
            DragGesture(minimumDistance: 22)
                .onEnded { value in
                    let horizontal = value.predictedEndTranslation.width
                    let vertical = value.predictedEndTranslation.height
                    guard horizontal < -52 else { return }
                    guard abs(horizontal) > abs(vertical) * 1.2 else { return }
                    replyMessageID = message.id
                    selectedMessageID = nil
                    QGHaptics.light()
                }
        )

        if shouldRenderActions, placeActionsBelowMessage {
            messageActions(for: message)
                .padding(.top, 6)
        }
    }

    private func clearDraftActions() {
        replyMessageID = nil
        selectedMessageID = nil
    }

    private func showForwardNotice(targetTitle: String) {
        forwardingNotice = language.text(
            ru: "Сообщение переслано \(targetTitle)",
            en: "Message forwarded to \(targetTitle)"
        )

        DispatchQueue.main.asyncAfter(deadline: .now() + 1.8) {
            if forwardingNotice.contains(targetTitle) {
                qgAnimate(.easeOut(duration: 0.2)) {
                    forwardingNotice = ""
                }
            }
        }
    }

    private func toggleMessageActions(for messageID: UUID) {
        qgAnimate(.spring(response: 0.25, dampingFraction: 0.86)) {
            selectedMessageID = selectedMessageID == messageID ? nil : messageID
        }
        QGHaptics.medium()
    }

    private func shouldShowActionsBelow(for message: MessageRecord) -> Bool {
        guard let frame = messageFrames[message.id] else { return false }
        guard headerFrame != .zero else { return false }

        let requiredTopSpace: CGFloat = 60
        return frame.minY - requiredTopSpace < headerFrame.maxY
    }

    private var chatDismissGesture: some Gesture {
        DragGesture(minimumDistance: 22)
            .onEnded { value in
                let horizontal = value.predictedEndTranslation.width
                let vertical = value.predictedEndTranslation.height
                guard horizontal > 84 else { return }
                guard abs(horizontal) > abs(vertical) * 1.25 else { return }
                QGHaptics.light()
                dismiss()
            }
    }

    private var headerDisplayName: String {
        let first = peerUser?.firstName.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !first.isEmpty {
            return first
        }
        return conversation.title
            .split(separator: " ")
            .first
            .map(String.init) ?? conversation.title
    }

    private var headerPresenceText: String {
        (peerUser?.presence ?? .offline).title(language: language)
    }

    private var headerPresenceColor: Color {
        switch peerUser?.presence ?? .offline {
        case .online: return QGTheme.Palette.online
        case .typing: return QGTheme.Palette.accent
        case .offline: return QGTheme.Palette.secondary
        }
    }

    private var language: AppLanguage {
        store.state.session.language
    }

    private var chatBackground: some View {
        GeometryReader { geometry in
            ZStack {
                Image(colorScheme == .dark ? "ChatBackgroundDark" : "ChatBackgroundLight")
                    .resizable()
                    .scaledToFill()
                    .frame(width: geometry.size.width, height: geometry.size.height)
                    .clipped()
                    .opacity(colorScheme == .dark ? 0.42 : 0.32)

                LinearGradient(
                    colors: [
                        QGTheme.Palette.screen.opacity(colorScheme == .dark ? 0.28 : 0.22),
                        QGTheme.Palette.screen.opacity(colorScheme == .dark ? 0.52 : 0.46),
                        QGTheme.Palette.screen.opacity(colorScheme == .dark ? 0.82 : 0.78)
                    ],
                    startPoint: .top,
                    endPoint: .bottom
                )
            }
            .ignoresSafeArea()
        }
    }

    private var recordingStatusBar: some View {
        HStack(spacing: 12) {
            Image(systemName: captureMode == .audio ? "waveform.circle.fill" : "video.circle.fill")
                .font(.system(size: 24, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.accent)

            Text(captureMode == .audio
                 ? language.text(
                    ru: "Запись \(audio.recordingDuration.formatted(.number.precision(.fractionLength(1)))) сек",
                    en: "Recording \(audio.recordingDuration.formatted(.number.precision(.fractionLength(1)))) sec"
                 )
                 : language.text(
                    ru: "Видео \(videoRecorder.recordingDuration.formatted(.number.precision(.fractionLength(1)))) сек",
                    en: "Video \(videoRecorder.recordingDuration.formatted(.number.precision(.fractionLength(1)))) sec"
                 )
            )
            .font(.system(size: 14, weight: .bold))
            .foregroundStyle(QGTheme.Palette.ink)

            Spacer()

            if videoRecorder.isRecording {
                Button {
                    useFrontCamera.toggle()
                    QGHaptics.medium()
                } label: {
                    Image(systemName: "arrow.triangle.2.circlepath.camera")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(QGTheme.Palette.accent)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .qgCardStyle(cornerRadius: 16, fill: QGTheme.Palette.cardFill)
    }

    private var circularRecordingOverlay: some View {
        GeometryReader { geometry in
            ZStack {
                Color.black.opacity(0.38)
                    .ignoresSafeArea()

                VStack {
                    Spacer(minLength: 70)
                    ZStack {
                        CircularCameraPreview(session: videoRecorder.previewSession)
                            .clipShape(Circle())
                            .overlay(
                                Circle()
                                    .fill(.black.opacity(0.08))
                            )

                        Circle()
                            .stroke(.white.opacity(0.92), lineWidth: 3)
                    }
                    .frame(
                        width: min(geometry.size.width * 0.72, 300),
                        height: min(geometry.size.width * 0.72, 300)
                    )
                    .clipShape(Circle())
                    .shadow(color: .black.opacity(0.32), radius: 18, y: 8)

                    Spacer()
                }
                .padding(.horizontal, 18)
                .padding(.bottom, 130)
            }
        }
    }

    private func previewText(for message: MessageRecord) -> String {
        if let attachment = message.attachment {
            switch attachment.kind {
            case .voiceNote:
                return language.text(ru: "Голосовое сообщение", en: "Voice message")
            case .circularVideo:
                return language.text(ru: "Видео-кружок", en: "Round video")
            case .media:
                return language.text(ru: "Фото/видео", en: "Photo/video")
            case .file:
                return language.text(ru: "Файл", en: "File")
            }
        }
        return store.decryptedText(for: message, in: conversation)
    }

    @discardableResult
    private func startCaptureRecording() -> Bool {
        guard !audio.isRecording, !videoRecorder.isRecording, draftText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return false
        }

        if captureMode == .audio {
            try? audio.startRecording()
        } else {
            Task { @MainActor in
                do {
                    try await videoRecorder.startRecording(useFrontCamera: useFrontCamera)
                    QGHaptics.heavy()
                } catch {
                    infoMessage = language.text(
                        ru: "Не удалось начать запись видео. Проверьте доступ к камере и микрофону.",
                        en: "Unable to start video recording. Check camera and microphone permissions."
                    )
                }
            }
        }
        return true
    }

    private func cancelVoiceRecording() {
        if let url = audio.stopRecording() {
            try? FileManager.default.removeItem(at: url)
        }
    }

    private func sendVoiceRecording() {
        guard let url = audio.stopRecording() else { return }
        store.queueAttachmentMessage(
            from: url,
            previewURL: nil,
            kind: .voiceNote,
            duration: audio.recordingDuration,
            in: conversationID,
            replyTo: replyMessageID,
            quotedExcerpt: nil
        )
        clearDraftActions()
    }

    private func cancelVideoRecording() async {
        await videoRecorder.cancelRecording()
    }

    private func sendCircularVideo() async {
        guard let videoURL = await videoRecorder.stopRecording() else {
            return
        }
        let preview = try? await QGMediaTools.makeThumbnail(for: videoURL)

        store.queueAttachmentMessage(
            from: videoURL,
            previewURL: preview,
            kind: .circularVideo,
            duration: videoRecorder.recordingDuration,
            in: conversationID,
            replyTo: replyMessageID,
            quotedExcerpt: nil
        )
        clearDraftActions()
    }
}

private struct ReplyPreviewPayload {
    let id: UUID
    let senderName: String
    let text: String
}

private struct MessageBubbleView: View {
    let message: MessageRecord
    let text: String
    let replyPreview: ReplyPreviewPayload?
    let sender: UserProfile?
    let isMine: Bool
    let showSenderName: Bool
    @ObservedObject var audioService: AudioRecorderService
    let onReplyPreviewTap: (UUID) -> Void
    let onOpenMedia: (MessageAttachment) -> Void
    let onOpenCircularVideo: (MessageAttachment) -> Void

    private var isCircularMessageStyle: Bool {
        message.attachment?.kind == .circularVideo && replyPreview == nil && message.forwardedFromTitle == nil
    }

    var body: some View {
        VStack(alignment: isMine ? .trailing : .leading, spacing: 8) {
            if !isMine, showSenderName {
                Text(sender?.displayName ?? "Контакт")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.secondary)
            }

            VStack(alignment: .leading, spacing: 10) {
                if let replyPreview {
                    Button {
                        onReplyPreviewTap(replyPreview.id)
                    } label: {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(replyPreview.senderName)
                                .font(.system(size: 11, weight: .bold))
                                .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.accent)
                            Text(replyPreview.text)
                                .font(.system(size: 13, weight: .medium))
                                .foregroundStyle(isMine ? .white.opacity(0.9) : QGTheme.Palette.secondary)
                                .lineLimit(1)
                                .truncationMode(.tail)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                        .background(.white.opacity(isMine ? 0.14 : 0.65))
                        .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                    }
                    .buttonStyle(.plain)
                }

                if let attachment = message.attachment {
                    attachmentView(attachment)
                }

                if !text.isEmpty,
                   message.attachment?.kind != .file,
                   message.attachment?.kind != .media,
                   message.attachment?.kind != .voiceNote,
                   message.attachment?.kind != .circularVideo {
                    Text(text)
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(isMine ? .white : QGTheme.Palette.ink)
                        .multilineTextAlignment(.leading)
                }

                if let forwardedFromTitle = message.forwardedFromTitle {
                    Text("переслано от \(forwardedFromTitle)")
                        .font(.system(size: 12, weight: .bold))
                        .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.accent)
                }

                HStack(spacing: 8) {
                    if message.attachment?.kind != .voiceNote {
                        Text(QGFormatters.messageTime.string(from: message.sentAt))
                    }
                    if message.transfer.phase == .sending {
                        Image(systemName: "clock")
                            .font(.system(size: 10, weight: .bold))
                        if message.attachment != nil {
                            Text("\(max(Int(message.transfer.progress * 100), 1))%")
                        }
                    } else if message.transfer.phase == .failed {
                        Image(systemName: "exclamationmark.circle.fill")
                            .font(.system(size: 10, weight: .bold))
                    }
                    if message.reportCount > 0 {
                        Text("жалоб: \(message.reportCount)")
                    }
                }
                .font(.system(size: 11, weight: .bold))
                .foregroundStyle(isMine ? .white.opacity(0.78) : QGTheme.Palette.secondary)
            }
            .padding(isCircularMessageStyle ? 0 : 14)
            .background {
                if !isCircularMessageStyle {
                    RoundedRectangle(cornerRadius: 24, style: .continuous)
                        .fill(isMine ? QGTheme.Palette.bubbleOutgoing : QGTheme.Palette.bubbleIncoming)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: isMine ? .trailing : .leading)
    }

    @ViewBuilder
    private func attachmentView(_ attachment: MessageAttachment) -> some View {
        switch attachment.kind {
        case .file:
            VStack(alignment: .leading, spacing: 10) {
                Label(attachment.name, systemImage: "doc.fill")
                    .font(.system(size: 15, weight: .bold))
                    .foregroundStyle(isMine ? .white : QGTheme.Palette.ink)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(QGFormatters.storage.string(fromByteCount: attachment.fileSizeBytes))
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.secondary)
                Button {
                    if let path = attachment.localPath {
                        _ = try? QGMediaTools.saveFileToDownloads(from: path)
                    }
                } label: {
                    Label("Скачать", systemImage: "arrow.down.circle")
                        .font(.system(size: 13, weight: .bold))
                        .foregroundStyle(isMine ? .white : QGTheme.Palette.accent)
                }
                .buttonStyle(.plain)
            }
            .padding(12)
            .background(.white.opacity(isMine ? 0.12 : 0.9))
            .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
        case .media:
            MediaAttachmentView(localPath: attachment.localPath, previewPath: attachment.previewPath) {
                onOpenMedia(attachment)
            }
        case .voiceNote:
            let voiceURL = attachment.localPath.map { URL(fileURLWithPath: $0) }
            let isPlaying = voiceURL.map { audioService.playingURL == $0 } ?? false
            Button {
                if let voiceURL {
                    audioService.togglePlayback(for: voiceURL)
                }
            } label: {
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 10) {
                        Image(systemName: isPlaying ? "pause.fill" : "play.fill")
                            .font(.system(size: 14, weight: .bold))
                        Text("Голосовое сообщение")
                            .font(.system(size: 16, weight: .bold))
                        Spacer()
                        Text(QGFormatters.messageTime.string(from: message.sentAt))
                            .font(.system(size: 12, weight: .bold))
                    }
                    Text(attachment.duration?.formatted(.number.precision(.fractionLength(1))) ?? "0.0")
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(isMine ? .white.opacity(0.9) : QGTheme.Palette.secondary)
                }
                .foregroundStyle(isMine ? .white : QGTheme.Palette.ink)
                .padding(.horizontal, 12)
                .padding(.vertical, 11)
                .background(.white.opacity(isMine ? 0.12 : 0.9))
                .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
            }
            .buttonStyle(.plain)
        case .circularVideo:
            Button {
                onOpenCircularVideo(attachment)
            } label: {
                CircularVideoAttachmentView(previewPath: attachment.previewPath)
            }
            .buttonStyle(.plain)
        }
    }
}

private struct MediaAttachmentView: View {
    let localPath: String?
    let previewPath: String?
    let onTap: () -> Void

    var body: some View {
        let resolvedImagePath: String? = {
            if let localPath, UIImage(contentsOfFile: localPath) != nil {
                return localPath
            }
            return previewPath
        }()
        let isVideo = localPath?.lowercased().hasSuffix(".mov") == true || localPath?.lowercased().hasSuffix(".mp4") == true

        Button(action: onTap) {
            ZStack {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .fill(Color.black.opacity(isVideo ? 0.34 : 0.08))

                if let resolvedImagePath, let image = UIImage(contentsOfFile: resolvedImagePath) {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: 258, maxHeight: 320)
                } else {
                    RoundedRectangle(cornerRadius: 16, style: .continuous)
                        .fill(QGTheme.Palette.bubbleOutgoingSoft)
                        .frame(width: 240, height: 220)
                }

                if isVideo {
                    Image(systemName: "play.fill")
                        .font(.system(size: 24, weight: .bold))
                        .foregroundStyle(.white)
                        .shadow(radius: 8)
                }
            }
            .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QGTheme.Palette.accent.opacity(0.45), lineWidth: 1)
            )
        }
        .buttonStyle(.plain)
    }
}

private struct CircularCameraPreview: UIViewRepresentable {
    let session: AVCaptureSession

    func makeUIView(context: Context) -> CircularCameraPreviewView {
        let view = CircularCameraPreviewView()
        view.updateSession(session)
        return view
    }

    func updateUIView(_ uiView: CircularCameraPreviewView, context: Context) {
        uiView.updateSession(session)
    }
}

private final class CircularCameraPreviewView: UIView {
    override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }

    private var previewLayer: AVCaptureVideoPreviewLayer {
        guard let layer = self.layer as? AVCaptureVideoPreviewLayer else {
            fatalError("Expected AVCaptureVideoPreviewLayer")
        }
        return layer
    }

    func updateSession(_ session: AVCaptureSession) {
        if previewLayer.session !== session {
            previewLayer.session = session
        }
        previewLayer.videoGravity = .resizeAspectFill
        previewLayer.connection?.videoOrientation = .portrait
    }
}

private struct CircularVideoPlayer: UIViewRepresentable {
    let player: AVPlayer

    func makeUIView(context: Context) -> CircularVideoPlayerView {
        let view = CircularVideoPlayerView()
        view.updatePlayer(player)
        return view
    }

    func updateUIView(_ uiView: CircularVideoPlayerView, context: Context) {
        uiView.updatePlayer(player)
    }
}

private final class CircularVideoPlayerView: UIView {
    override class var layerClass: AnyClass { AVPlayerLayer.self }

    private var playerLayer: AVPlayerLayer {
        guard let layer = self.layer as? AVPlayerLayer else {
            fatalError("Expected AVPlayerLayer")
        }
        return layer
    }

    func updatePlayer(_ player: AVPlayer) {
        if playerLayer.player !== player {
            playerLayer.player = player
        }
        playerLayer.videoGravity = .resizeAspectFill
    }
}

private struct CircularVideoAttachmentView: View {
    let previewPath: String?

    var body: some View {
        ZStack {
            Circle().fill(QGTheme.Palette.accent.opacity(0.95))

            Group {
                if let previewPath, let image = UIImage(contentsOfFile: previewPath) {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFill()
                } else {
                    Circle().fill(QGTheme.Palette.bubbleOutgoingSoft)
                }
            }
            .clipShape(Circle())
            .padding(2.4)

            Image(systemName: "play.fill")
                .font(.system(size: 28, weight: .bold))
                .foregroundStyle(.white)
                .shadow(radius: 10)
        }
        .frame(width: 214, height: 214)
    }
}

private struct CircularVideoViewerSheet: View {
    @Environment(\.dismiss) private var dismiss
    let attachment: MessageAttachment
    @State private var player: AVPlayer?

    private var previewImage: UIImage? {
        guard let previewPath = attachment.previewPath else { return nil }
        return UIImage(contentsOfFile: previewPath)
    }

    private var playableURL: URL? {
        guard let localPath = attachment.localPath else { return nil }
        let url = URL(fileURLWithPath: localPath)
        let exists = FileManager.default.fileExists(atPath: localPath)
        let size = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
        return exists && size > 0 ? url : nil
    }

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
                        Image(systemName: "chevron.down")
                            .font(.system(size: 16, weight: .bold))
                            .foregroundStyle(QGTheme.Palette.ink)
                            .frame(width: 36, height: 36)
                            .background(Circle().fill(.ultraThinMaterial))
                    }
                    .buttonStyle(.plain)
                    Spacer()
                }
                .padding(.bottom, 20)

                Spacer(minLength: 0)

                if let player {
                    CircularVideoPlayer(player: player)
                        .clipShape(Circle())
                        .frame(width: 300, height: 300)
                        .overlay(Circle().stroke(QGTheme.Palette.accent.opacity(0.6), lineWidth: 2))
                } else {
                    ZStack {
                        if let previewImage {
                            Image(uiImage: previewImage)
                                .resizable()
                                .scaledToFill()
                        } else {
                            Circle().fill(QGTheme.Palette.bubbleOutgoingSoft)
                        }
                        Image(systemName: "play.fill")
                            .font(.system(size: 34, weight: .bold))
                            .foregroundStyle(.white)
                            .shadow(radius: 8)
                    }
                    .frame(width: 300, height: 300)
                    .clipShape(Circle())
                    .overlay(Circle().stroke(QGTheme.Palette.accent.opacity(0.6), lineWidth: 2))
                }

                Spacer(minLength: 0)
            }
            .padding(QGTheme.pagePadding)
            .padding(.top, 4)
        }
        .task {
            guard let playableURL else { return }
            player = AVPlayer(url: playableURL)
            player?.play()
        }
        .onDisappear {
            player?.pause()
            player = nil
        }
    }
}

private struct MediaViewerSheet: View {
    @Environment(\.dismiss) private var dismiss
    let attachment: MessageAttachment

    @State private var zoomScale: CGFloat = 1
    @State private var infoMessage = ""

    private var image: UIImage? {
        if let localPath = attachment.localPath, let image = UIImage(contentsOfFile: localPath) {
            return image
        }
        if let previewPath = attachment.previewPath, let image = UIImage(contentsOfFile: previewPath) {
            return image
        }
        return nil
    }

    private var videoURL: URL? {
        guard let localPath = attachment.localPath else { return nil }
        let lowered = localPath.lowercased()
        guard lowered.hasSuffix(".mov") || lowered.hasSuffix(".mp4") else { return nil }
        let url = URL(fileURLWithPath: localPath)
        return FileManager.default.fileExists(atPath: url.path) ? url : nil
    }

    var body: some View {
        ZStack {
            Rectangle()
                .fill(.ultraThinMaterial)
                .ignoresSafeArea()

            VStack(spacing: 14) {
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

                    Button {
                        guard let localPath = attachment.localPath else { return }
                        _ = try? QGMediaTools.saveFileToDownloads(from: localPath)
                        infoMessage = "Скачано"
                    } label: {
                        Image(systemName: "arrow.down")
                            .font(.system(size: 16, weight: .bold))
                            .foregroundStyle(QGTheme.Palette.ink)
                            .frame(width: 36, height: 36)
                            .background(Circle().fill(.ultraThinMaterial))
                    }
                    .buttonStyle(.plain)
                }

                Spacer(minLength: 0)

                if let videoURL {
                    VideoPlayer(player: AVPlayer(url: videoURL))
                        .frame(maxWidth: .infinity, maxHeight: 420)
                        .clipShape(RoundedRectangle(cornerRadius: 24, style: .continuous))
                } else if let image {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .scaleEffect(zoomScale)
                        .gesture(
                            MagnificationGesture()
                                .onChanged { value in
                                    zoomScale = min(max(value, 1), 4)
                                }
                                .onEnded { _ in
                                    if zoomScale < 1.01 {
                                        zoomScale = 1
                                    }
                                }
                        )
                        .frame(maxWidth: .infinity, maxHeight: 520)
                        .clipShape(RoundedRectangle(cornerRadius: 24, style: .continuous))
                } else {
                    RoundedRectangle(cornerRadius: 24, style: .continuous)
                        .fill(QGTheme.Palette.cardFill)
                        .frame(maxWidth: .infinity, maxHeight: 320)
                        .overlay(
                            Image(systemName: "photo")
                                .font(.system(size: 34, weight: .bold))
                                .foregroundStyle(QGTheme.Palette.secondary)
                        )
                }

                if !infoMessage.isEmpty {
                    Text(infoMessage)
                        .font(.system(size: 13, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.secondary)
                }

                Spacer(minLength: 0)
            }
            .padding(QGTheme.pagePadding)
        }
    }
}

private struct ChatPeerProfileSheet: View {
    @Environment(\.dismiss) private var dismiss

    let user: UserProfile
    let language: AppLanguage
    let onReport: () -> Void
    let onBlock: () -> Void

    var body: some View {
        NavigationStack {
            ScrollView(showsIndicators: false) {
                VStack(spacing: 18) {
                    QGAvatarView(user: user, size: 104, showOnlineRing: false)
                        .padding(.top, 8)

                    Text(user.displayName)
                        .font(.system(size: 28, weight: .bold, design: .rounded))
                        .foregroundStyle(QGTheme.Palette.ink)
                        .multilineTextAlignment(.center)

                    VStack(alignment: .leading, spacing: 10) {
                        profileRow(title: language.text(ru: "Ник", en: "Nickname"), value: user.nickname)
                        profileRow(title: language.text(ru: "Описание", en: "Bio"), value: user.bio.isEmpty ? "—" : user.bio)
                        profileRow(title: language.text(ru: "Дата регистрации", en: "Joined"), value: QGFormatters.dayTitle.string(from: user.joinedAt))
                    }
                    .padding(18)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .qgCardStyle(cornerRadius: 22)

                    Button {
                        onReport()
                        QGHaptics.medium()
                        dismiss()
                    } label: {
                        Text(language.text(ru: "Пожаловаться", en: "Report"))
                            .font(.system(size: 17, weight: .bold))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 14)
                            .background(RoundedRectangle(cornerRadius: 20, style: .continuous).fill(QGTheme.Palette.destructiveSurface))
                            .foregroundStyle(QGTheme.Palette.destructive)
                    }
                    .buttonStyle(.plain)

                    Button {
                        onBlock()
                        QGHaptics.medium()
                        dismiss()
                    } label: {
                        Text(language.text(ru: "Заблокировать", en: "Block"))
                            .font(.system(size: 17, weight: .bold))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 14)
                            .background(RoundedRectangle(cornerRadius: 20, style: .continuous).fill(QGTheme.Palette.cardFill))
                            .foregroundStyle(QGTheme.Palette.ink)
                    }
                    .buttonStyle(.plain)
                }
                .padding(QGTheme.pagePadding)
            }
            .qgScreenBackground()
            .navigationTitle(language.text(ru: "Профиль", en: "Profile"))
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    private func profileRow(title: String, value: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
                .font(.system(size: 12, weight: .bold))
                .foregroundStyle(QGTheme.Palette.secondary)
            Text(value)
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(QGTheme.Palette.ink)
        }
    }
}

private struct VideoPreviewSheet: View {
    let url: URL

    var body: some View {
        VStack(spacing: 18) {
            VideoPreviewBadge(imagePath: nil)
            Text(url.lastPathComponent)
                .font(.system(size: 18, weight: .bold))
            Text("Локальный файл сохранён в sandbox приложения и может быть отправлен чанками без блокировки интерфейса.")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(QGTheme.Palette.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(QGTheme.pagePadding)
        .qgScreenBackground()
    }
}
