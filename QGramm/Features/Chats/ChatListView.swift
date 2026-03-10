import PhotosUI
import SwiftUI

struct ChatListView: View {
    @EnvironmentObject private var store: AppStore
    @State private var searchText = ""
    @State private var path: [UUID] = []

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView(showsIndicators: false) {
                VStack(alignment: .leading, spacing: 20) {
                    header
                    SearchField(placeholder: "Найти чат или @nickname", text: $searchText)
                    segmentPicker
                    conversationList
                }
                .padding(.horizontal, QGTheme.pagePadding)
                .padding(.top, 24)
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
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack {
                Spacer()
                WordmarkView(title: "Qgramm", size: 38)
                Spacer()
            }

            if let user = store.currentUser {
                HStack(spacing: 14) {
                    QGAvatarView(user: user, size: 52, showOnlineRing: user.presence == .online)
                    VStack(alignment: .leading, spacing: 4) {
                        Text(user.displayName)
                            .font(.system(size: 24, weight: .bold, design: .rounded))
                            .foregroundStyle(QGTheme.Palette.ink)
                        HStack(spacing: 8) {
                            Text(user.nickname)
                                .font(.system(size: 14, weight: .semibold))
                                .foregroundStyle(QGTheme.Palette.secondary)
                            TrustBadgeView(level: user.trustLevel)
                        }
                    }
                }
            }
        }
    }

    private var segmentPicker: some View {
        HStack(spacing: 10) {
            ForEach(ConversationGroup.allCases) { group in
                Button {
                    store.selectConversationGroup(group)
                } label: {
                    Text(group.title)
                        .font(.system(size: 15, weight: .bold, design: .rounded))
                        .foregroundStyle(store.state.selectedConversationGroup == group ? .white : QGTheme.Palette.secondary)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                        .background(
                            RoundedRectangle(cornerRadius: 18, style: .continuous)
                                .fill(store.state.selectedConversationGroup == group ? QGTheme.Palette.accent : Color.clear)
                        )
                        .qgPillBorder(selected: store.state.selectedConversationGroup == group)
                }
                .buttonStyle(.plain)
            }
        }
    }

    private var conversationList: some View {
        let conversations = store.filteredConversations(query: searchText, group: store.state.selectedConversationGroup)
        let discoverable = store.discoverableUser(for: searchText)

        return VStack(alignment: .leading, spacing: 12) {
            if let discoverable {
                Button {
                    let id = store.startConversation(with: discoverable.id)
                    path.append(id)
                } label: {
                    HStack(spacing: 14) {
                        QGAvatarView(user: discoverable, size: 52, showOnlineRing: discoverable.presence == .online)
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Написать \(discoverable.displayName)")
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
            }

            if conversations.isEmpty {
                EmptyStateView(
                    title: "Пусто",
                    message: "По этому фильтру ничего не найдено. Попробуйте другой сегмент или начните диалог по @nickname.",
                    systemImage: "magnifyingglass.circle"
                )
            } else {
                ForEach(conversations) { conversation in
                    Button {
                        path.append(conversation.id)
                    } label: {
                        ChatRowView(conversation: conversation, searchText: searchText)
                    }
                    .buttonStyle(.plain)

                    Divider()
                        .background(QGTheme.Palette.line)
                        .padding(.leading, 74)
                }
            }
        }
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
        .padding(.vertical, 6)
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

struct ChatRoomView: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var store: AppStore
    @StateObject private var audio = AudioRecorderService()

    let conversationID: UUID

    @State private var draftText = ""
    @State private var selectedMessageID: UUID?
    @State private var replyMessageID: UUID?
    @State private var quoteDraft: DraftQuote?
    @State private var showFileImporter = false
    @State private var videoPickerItem: PhotosPickerItem?
    @State private var reportMessageID: UUID?
    @State private var forwardMessageID: UUID?
    @State private var reportReason: ReportReason = .spam
    @State private var reportNote = ""
    @State private var previewVideoURL: URL?
    @State private var infoMessage = ""
    @State private var showQuoteEditor = false

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
        VStack(spacing: 0) {
            topBar

            Divider()
                .background(QGTheme.Palette.line)

            ScrollViewReader { proxy in
                ScrollView(showsIndicators: false) {
                    LazyVStack(spacing: 16) {
                        ForEach(sortedMessages) { message in
                            messageRow(for: message)
                        }
                    }
                    .padding(.horizontal, QGTheme.pagePadding)
                    .padding(.top, 18)
                    .padding(.bottom, 28)
                }
                .onChange(of: sortedMessages.count) { _, _ in
                    if let last = sortedMessages.last?.id {
                        withAnimation(.spring(response: 0.28, dampingFraction: 0.9)) {
                            proxy.scrollTo(last, anchor: .bottom)
                        }
                    }
                }
            }

            if let reply = replyMessageID, let source = sortedMessages.first(where: { $0.id == reply }) {
                draftBanner(
                    title: "Ответ",
                    text: store.decryptedText(for: source, in: conversation)
                ) {
                    replyMessageID = nil
                }
            }

            if let quoteDraft {
                draftBanner(
                    title: "Цитата",
                    text: quoteDraft.editableExcerpt
                ) {
                    self.quoteDraft = nil
                }
            }

            if audio.isRecording {
                HStack(spacing: 12) {
                    Image(systemName: "waveform.circle.fill")
                        .font(.system(size: 26))
                        .foregroundStyle(QGTheme.Palette.accent)
                    Text("Запись \(audio.recordingDuration.formatted(.number.precision(.fractionLength(1)))) сек")
                        .font(.system(size: 15, weight: .bold))
                        .foregroundStyle(QGTheme.Palette.ink)
                    Spacer()
                }
                .padding(.horizontal, QGTheme.pagePadding)
                .padding(.top, 12)
            }

            composer
        }
        .qgScreenBackground()
        .navigationBarBackButtonHidden(true)
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
                    quotedExcerpt: quoteDraft?.editableExcerpt
                )
                clearDraftActions()
            }
        }
        .task(id: videoPickerItem) {
            guard let videoPickerItem else { return }
            if let data = try? await videoPickerItem.loadTransferable(type: Data.self) {
                let tempURL = FileManager.default.temporaryDirectory
                    .appendingPathComponent(UUID().uuidString)
                    .appendingPathExtension("mov")
                try? data.write(to: tempURL)
                let preview = try? QGMediaTools.makeThumbnail(for: tempURL)
                store.queueAttachmentMessage(
                    from: tempURL,
                    previewURL: preview,
                    kind: .circularVideo,
                    duration: 0,
                    in: conversationID,
                    replyTo: replyMessageID,
                    quotedExcerpt: quoteDraft?.editableExcerpt
                )
                clearDraftActions()
            }
        }
        .sheet(isPresented: $showQuoteEditor) {
            NavigationStack {
                VStack(spacing: 18) {
                    Text("Оставьте только нужный фрагмент. Это и будет цитатой в новом сообщении.")
                        .font(.system(size: 15, weight: .medium))
                        .foregroundStyle(QGTheme.Palette.secondary)
                    TextEditor(
                        text: Binding(
                            get: { quoteDraft?.editableExcerpt ?? quoteDraft?.originalText ?? "" },
                            set: { newValue in
                                quoteDraft?.editableExcerpt = newValue
                            }
                        )
                    )
                    .padding(12)
                    .frame(minHeight: 180)
                    .qgCardStyle(cornerRadius: 24)
                    Spacer()
                }
                .padding(QGTheme.pagePadding)
                .qgScreenBackground()
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button("Отмена") {
                            quoteDraft = nil
                            showQuoteEditor = false
                        }
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Готово") {
                            showQuoteEditor = false
                        }
                    }
                }
            }
            .presentationDetents([.medium])
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
                            }
                            forwardMessageID = nil
                            selectedMessageID = nil
                        }
                    }
                }
                .navigationTitle("Переслать")
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
                    Picker("Причина", selection: $reportReason) {
                        ForEach(ReportReason.allCases) { reason in
                            Text(reason.title).tag(reason)
                        }
                    }
                    .pickerStyle(.segmented)

                    TextField("Комментарий для админов", text: $reportNote, axis: .vertical)
                        .lineLimit(3...5)
                        .padding(16)
                        .qgCardStyle(cornerRadius: 24)

                    Button {
                        if let reportMessageID {
                            store.reportMessage(
                                messageID: reportMessageID,
                                conversationID: conversationID,
                                reason: reportReason,
                                note: reportNote
                            )
                        }
                        reportNote = ""
                        reportMessageID = nil
                        selectedMessageID = nil
                    } label: {
                        Text("Отправить жалобу")
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
                .navigationTitle("Жалоба")
                .navigationBarTitleDisplayMode(.inline)
            }
            .presentationDetents([.medium])
        }
        .sheet(isPresented: Binding(
            get: { previewVideoURL != nil },
            set: { if !$0 { previewVideoURL = nil } }
        )) {
            if let previewVideoURL {
                VideoPreviewSheet(url: previewVideoURL)
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
                    .background(Circle().fill(.white.opacity(0.88)))
            }

            QGAvatarView(user: peerUser, size: 44, showOnlineRing: conversation.onlineUserID != nil)

            VStack(alignment: .leading, spacing: 4) {
                Text(conversation.title)
                    .font(.system(size: 19, weight: .bold, design: .rounded))
                    .foregroundStyle(QGTheme.Palette.ink)
                Text(peerUser?.presence == .typing ? "Печатает..." : "E2E chat")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(peerUser?.presence == .typing ? QGTheme.Palette.online : QGTheme.Palette.secondary)
            }

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
                Button("Инфо о шифровании") {
                    infoMessage = store.state.session.safetyMode == .classic
                        ? "Classic: E2E для сообщений, голосовых и файлов. Метаданные инвайтов и жалоб доступны админам."
                        : "Local: приоритет локальных P2P-сценариев и локального хранения."
                }
            } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 20, weight: .semibold))
                    .foregroundStyle(QGTheme.Palette.secondary)
            }
        }
        .padding(.horizontal, QGTheme.pagePadding)
        .padding(.top, 14)
        .padding(.bottom, 12)
        .alert("Шифрование", isPresented: Binding(
            get: { !infoMessage.isEmpty },
            set: { if !$0 { infoMessage = "" } }
        )) {
            Button("ОК") { infoMessage = "" }
        } message: {
            Text(infoMessage)
        }
    }

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 12) {
            Menu {
                Button("Файл", systemImage: "paperclip") {
                    showFileImporter = true
                }
                PhotosPicker(selection: $videoPickerItem, matching: .videos) {
                    Label("Кружок-видео", systemImage: "video.fill")
                }
            } label: {
                Image("PlusIcon")
                    .resizable()
                    .renderingMode(.template)
                    .scaledToFit()
                    .frame(width: 24, height: 24)
                    .foregroundStyle(QGTheme.Palette.secondary)
                    .frame(width: 44, height: 44)
                    .background(Circle().fill(.white.opacity(0.9)))
            }

            TextField("Сообщение", text: $draftText, axis: .vertical)
                .lineLimit(1...4)
                .padding(.horizontal, 18)
                .padding(.vertical, 14)
                .qgCardStyle(cornerRadius: 24)

            Button {
                if audio.isRecording {
                    if let url = audio.stopRecording() {
                        store.queueAttachmentMessage(
                            from: url,
                            previewURL: nil,
                            kind: .voiceNote,
                            duration: audio.recordingDuration,
                            in: conversationID,
                            replyTo: replyMessageID,
                            quotedExcerpt: quoteDraft?.editableExcerpt
                        )
                        clearDraftActions()
                    }
                } else if !draftText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    store.sendTextMessage(
                        draftText,
                        replyTo: replyMessageID,
                        quotedExcerpt: quoteDraft?.editableExcerpt,
                        in: conversationID
                    )
                    draftText = ""
                    clearDraftActions()
                } else {
                    try? audio.startRecording()
                }
            } label: {
                Image(systemName: actionSymbol)
                    .font(.system(size: 18, weight: .bold))
                    .foregroundStyle(.white)
                    .frame(width: 52, height: 52)
                    .background(Circle().fill(QGTheme.Palette.accent))
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, QGTheme.pagePadding)
        .padding(.top, 12)
        .padding(.bottom, 14)
        .background(.ultraThinMaterial)
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
                    .lineLimit(2)
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
        .padding(.vertical, 10)
        .background(.white.opacity(0.7))
    }

    private func messageActions(for message: MessageRecord) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(["🔥", "👍", "👎", "❤️", "😢"], id: \.self) { emoji in
                    Button(emoji) {
                        store.toggleReaction(emoji, messageID: message.id, conversationID: conversationID)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(QGTheme.Palette.accent.opacity(0.88))
                }

                Button("Ответ") {
                    replyMessageID = message.id
                    selectedMessageID = nil
                }
                .buttonStyle(.bordered)

                Button("Цитата") {
                    let original = store.decryptedText(for: message, in: conversation)
                    quoteDraft = DraftQuote(id: UUID(), sourceMessageID: message.id, originalText: original, editableExcerpt: original)
                    showQuoteEditor = true
                    selectedMessageID = nil
                }
                .buttonStyle(.bordered)

                Button("Переслать") {
                    forwardMessageID = message.id
                }
                .buttonStyle(.bordered)

                Button("Жалоба") {
                    reportMessageID = message.id
                }
                .buttonStyle(.bordered)
                .tint(QGTheme.Palette.destructive)
            }
            .font(.system(size: 13, weight: .bold))
            .padding(.horizontal, 8)
        }
        .padding(.horizontal, 8)
    }

    private var actionSymbol: String {
        if audio.isRecording {
            return "stop.fill"
        }
        return draftText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "mic.fill" : "arrow.up"
    }

    @ViewBuilder
    private func messageRow(for message: MessageRecord) -> some View {
        let replyPreview = message.replyToMessageID.flatMap { id in
            sortedMessages.first(where: { $0.id == id }).map { store.decryptedText(for: $0, in: conversation) }
        }

        MessageBubbleView(
            message: message,
            text: store.decryptedText(for: message, in: conversation),
            quotedText: store.decryptedQuote(for: message, in: conversation),
            replyPreview: replyPreview,
            sender: store.user(id: message.senderID),
            isMine: message.senderID == store.state.session.currentUserID,
            audioService: audio
        )
        .id(message.id)
        .onTapGesture {
            withAnimation(.spring(response: 0.25, dampingFraction: 0.86)) {
                selectedMessageID = selectedMessageID == message.id ? nil : message.id
            }
        }

        if selectedMessageID == message.id {
            messageActions(for: message)
        }
    }

    private func clearDraftActions() {
        replyMessageID = nil
        quoteDraft = nil
        selectedMessageID = nil
    }
}

private struct MessageBubbleView: View {
    let message: MessageRecord
    let text: String
    let quotedText: String
    let replyPreview: String?
    let sender: UserProfile?
    let isMine: Bool
    @ObservedObject var audioService: AudioRecorderService

    var body: some View {
        VStack(alignment: isMine ? .trailing : .leading, spacing: 8) {
            if !isMine {
                Text(sender?.displayName ?? "Контакт")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(QGTheme.Palette.secondary)
            }

            VStack(alignment: .leading, spacing: 10) {
                if let replyPreview {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Ответ")
                            .font(.system(size: 11, weight: .bold))
                            .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.accent)
                        Text(replyPreview)
                            .font(.system(size: 13, weight: .medium))
                            .foregroundStyle(isMine ? .white.opacity(0.9) : QGTheme.Palette.secondary)
                            .lineLimit(2)
                    }
                    .padding(10)
                    .background(.white.opacity(isMine ? 0.14 : 0.65))
                    .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                }

                if !quotedText.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Цитата")
                            .font(.system(size: 11, weight: .bold))
                            .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.accent)
                        Text(quotedText)
                            .font(.system(size: 13, weight: .medium))
                            .foregroundStyle(isMine ? .white.opacity(0.95) : QGTheme.Palette.ink)
                            .lineLimit(4)
                    }
                    .padding(10)
                    .background(.white.opacity(isMine ? 0.16 : 0.84))
                    .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                }

                if let attachment = message.attachment {
                    attachmentView(attachment)
                }

                if !text.isEmpty, message.attachment?.kind != .file {
                    Text(text)
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(isMine ? .white : QGTheme.Palette.ink)
                        .multilineTextAlignment(.leading)
                }

                if let forwardedFromTitle = message.forwardedFromTitle {
                    Text("Переслано из \(forwardedFromTitle)")
                        .font(.system(size: 12, weight: .bold))
                        .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.accent)
                }

                HStack(spacing: 8) {
                    Text(QGFormatters.messageTime.string(from: message.sentAt))
                    if message.transfer.phase == .sending {
                        Text("\(Int(message.transfer.progress * 100))%")
                    }
                    if message.reportCount > 0 {
                        Text("жалоб: \(message.reportCount)")
                    }
                }
                .font(.system(size: 11, weight: .bold))
                .foregroundStyle(isMine ? .white.opacity(0.78) : QGTheme.Palette.secondary)
            }
            .padding(14)
            .background(
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .fill(isMine ? QGTheme.Palette.bubbleOutgoing : QGTheme.Palette.bubbleIncoming)
            )

            if !message.reactions.isEmpty {
                HStack(spacing: 8) {
                    ForEach(message.reactions) { reaction in
                        Text("\(reaction.emoji) \(reaction.userIDs.count)")
                            .font(.system(size: 12, weight: .bold))
                            .padding(.horizontal, 10)
                            .padding(.vertical, 6)
                            .background(Capsule().fill(.white.opacity(0.75)))
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: isMine ? .trailing : .leading)
    }

    @ViewBuilder
    private func attachmentView(_ attachment: MessageAttachment) -> some View {
        switch attachment.kind {
        case .file:
            VStack(alignment: .leading, spacing: 8) {
                Label(attachment.name, systemImage: "doc.fill")
                    .font(.system(size: 15, weight: .bold))
                    .foregroundStyle(isMine ? .white : QGTheme.Palette.ink)
                Text(QGFormatters.storage.string(fromByteCount: attachment.fileSizeBytes))
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(isMine ? .white.opacity(0.82) : QGTheme.Palette.secondary)
            }
            .padding(12)
            .background(.white.opacity(isMine ? 0.12 : 0.9))
            .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
        case .voiceNote:
            Button {
                if let path = attachment.localPath {
                    audioService.togglePlayback(for: URL(fileURLWithPath: path))
                }
            } label: {
                HStack(spacing: 12) {
                    Image(systemName: "play.fill")
                        .font(.system(size: 16, weight: .bold))
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Голосовое сообщение")
                            .font(.system(size: 15, weight: .bold))
                        Text(attachment.duration?.formatted(.number.precision(.fractionLength(1))) ?? "0.0")
                            .font(.system(size: 12, weight: .semibold))
                    }
                    Spacer()
                }
                .foregroundStyle(isMine ? .white : QGTheme.Palette.ink)
                .padding(12)
                .background(.white.opacity(isMine ? 0.12 : 0.9))
                .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
            }
            .buttonStyle(.plain)
        case .circularVideo:
            if let path = attachment.previewPath {
                VideoPreviewBadge(imagePath: path)
            } else {
                VideoPreviewBadge(imagePath: nil)
            }
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
