import Foundation
import SwiftUI

enum StoreError: LocalizedError {
    case message(String)

    var errorDescription: String? {
        switch self {
        case let .message(message):
            return message
        }
    }
}

@MainActor
final class AppStore: ObservableObject {
    @Published private(set) var state: AppState
    @Published var activeCall: ActiveCallSession?

    private let crypto = QGCryptoService()
    private let persistenceURL: URL
    private let calendar = Calendar.current

    init() {
        let supportDirectory = (try? QGMediaTools.applicationSupportDirectory()) ?? FileManager.default.temporaryDirectory
        persistenceURL = supportDirectory.appendingPathComponent("state.json")

        if
            let data = try? Data(contentsOf: persistenceURL),
            let loaded = try? JSONDecoder().decode(AppState.self, from: data)
        {
            state = loaded
        } else {
            state = Self.makeSeedState(crypto: crypto)
            persist()
        }
    }

    var currentUser: UserProfile? {
        guard let currentUserID = state.session.currentUserID else { return nil }
        return user(id: currentUserID)
    }

    func user(id: UUID) -> UserProfile? {
        state.users.first { $0.id == id }
    }

    func conversation(id: UUID) -> ConversationRecord? {
        state.conversations.first { $0.id == id }
    }

    func decryptedText(for message: MessageRecord, in conversation: ConversationRecord) -> String {
        crypto.decrypt(message.body, using: conversation.sharedKey)
    }

    func decryptedQuote(for message: MessageRecord, in conversation: ConversationRecord) -> String {
        crypto.decrypt(message.quotedBody, using: conversation.sharedKey)
    }

    func conversationSubtitle(_ conversation: ConversationRecord) -> String {
        guard let last = conversation.messages.sorted(by: { $0.sentAt < $1.sentAt }).last else {
            return "Новый чат"
        }

        if let attachment = last.attachment {
            switch attachment.kind {
            case .file: return "Файл: \(attachment.name)"
            case .voiceNote: return "Голосовое сообщение"
            case .circularVideo: return "Круглое видео"
            }
        }

        let text = decryptedText(for: last, in: conversation)
        return text.isEmpty ? "Сообщение зашифровано" : text
    }

    func filteredConversations(query: String, group: ConversationGroup) -> [ConversationRecord] {
        let normalized = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()

        return state.conversations
            .filter { $0.category == group }
            .filter { conversation in
                guard !normalized.isEmpty else { return true }

                let participantMatches = conversation.participantIDs
                    .compactMap(user(id:))
                    .contains { profile in
                        profile.displayName.lowercased().contains(normalized) ||
                        profile.nickname.lowercased().contains(normalized)
                    }

                return conversation.title.lowercased().contains(normalized) || participantMatches
            }
            .sorted { $0.lastActivityAt > $1.lastActivityAt }
    }

    func discoverableUser(for handle: String) -> UserProfile? {
        let normalized = handle.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard normalized.hasPrefix("@"), normalized.count > 1 else { return nil }
        return state.users.first { $0.nickname.lowercased() == normalized && $0.id != state.session.currentUserID }
    }

    @discardableResult
    func startConversation(with userID: UUID) -> UUID {
        if let existing = state.conversations.first(where: {
            $0.category == .chats &&
            Set($0.participantIDs) == Set([userID, state.session.currentUserID].compactMap { $0 })
        }) {
            return existing.id
        }

        let title = user(id: userID)?.displayName ?? "Новый чат"
        let conversation = ConversationRecord(
            id: UUID(),
            category: .chats,
            title: title,
            participantIDs: [state.session.currentUserID, userID].compactMap { $0 },
            sharedKey: crypto.makeSharedKey(),
            lastActivityAt: .now,
            unreadCount: 0,
            onlineUserID: userID,
            messages: []
        )

        mutate {
            $0.conversations.insert(conversation, at: 0)
        }

        return conversation.id
    }

    func acceptInvite(code: String) -> Bool {
        let normalized = code.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        let deviceID = state.session.deviceBindingID

        guard let invite = state.invites.first(where: {
            $0.code == normalized && ($0.isActive || $0.boundDeviceID == deviceID)
        }) else {
            return false
        }

        mutate {
            $0.session.acceptedInviteCode = invite.code
        }

        if invite.boundDeviceID == nil {
            mutate {
                guard let index = $0.invites.firstIndex(where: { $0.id == invite.id }) else { return }
                $0.invites[index].boundDeviceID = deviceID
            }
        }

        return true
    }

    func sendVerificationCode(to contact: String, captchaPassed: Bool) -> String? {
        let normalized = contact.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalized.isEmpty, captchaPassed, state.session.hasBoundInvite else { return nil }

        let code = String(Int.random(in: 111_111...999_999))
        mutate {
            $0.session.pendingContact = normalized
            $0.session.expectedVerificationCode = code
        }

        return code
    }

    func completeRegistration(firstName: String, lastName: String, nickname: String, enteredCode: String, safetyMode: SafetyMode) -> Result<String, StoreError> {
        let cleanedFirstName = firstName.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedLastName = lastName.trimmingCharacters(in: .whitespacesAndNewlines)
        var handle = nickname.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if !handle.hasPrefix("@") {
            handle = "@\(handle)"
        }

        guard !cleanedFirstName.isEmpty, !cleanedLastName.isEmpty else {
            return .failure(.message("Имя и фамилия обязательны."))
        }

        guard enteredCode == state.session.expectedVerificationCode, !enteredCode.isEmpty else {
            return .failure(.message("Код подтверждения не совпадает."))
        }

        guard !state.users.contains(where: { $0.nickname.lowercased() == handle }) else {
            return .failure(.message("Никнейм уже занят."))
        }

        guard let inviteCode = state.session.acceptedInviteCode,
              let invite = state.invites.first(where: { $0.code == inviteCode }) else {
            return .failure(.message("Инвайт не найден."))
        }

        let userID = UUID()
        let recoveryKey = crypto.generateRecoveryKey()
        let newUser = UserProfile(
            id: userID,
            firstName: cleanedFirstName,
            lastName: cleanedLastName,
            nickname: handle,
            emailOrPhone: state.session.pendingContact,
            bio: "Invite-only network member",
            trustLevel: .one,
            presence: .online,
            joinedAt: .now,
            avatarLocalPath: nil,
            avatarAssetName: nil,
            isAdmin: false,
            isBanned: false,
            invitedByUserID: invite.inviterUserID
        )

        let welcomeKey = crypto.makeSharedKey()
        let inviterConversation = ConversationRecord(
            id: UUID(),
            category: .chats,
            title: user(id: invite.inviterUserID)?.displayName ?? "Добро пожаловать",
            participantIDs: [invite.inviterUserID, userID],
            sharedKey: welcomeKey,
            lastActivityAt: .now,
            unreadCount: 1,
            onlineUserID: invite.inviterUserID,
            messages: [
                makeEncryptedMessage(
                    senderID: invite.inviterUserID,
                    text: "Добро пожаловать в Qgramm. Здесь история чатов хранится локально, а сообщения шифруются на устройстве.",
                    sharedKey: welcomeKey
                )
            ]
        )

        mutate {
            $0.users.insert(newUser, at: 0)
            $0.conversations.insert(inviterConversation, at: 0)
            $0.session.currentUserID = userID
            $0.session.recoveryKey = recoveryKey
            $0.session.shouldRevealRecoveryKey = true
            $0.session.expectedVerificationCode = ""
            $0.session.safetyMode = safetyMode

            guard let inviteIndex = $0.invites.firstIndex(where: { $0.id == invite.id }) else { return }
            $0.invites[inviteIndex].redeemedByUserID = userID
        }

        return .success(recoveryKey)
    }

    func restoreLocalAccount() {
        if state.session.currentUserID == nil, let local = state.users.first(where: { !$0.isBanned }) {
            mutate {
                $0.session.currentUserID = local.id
            }
        }
    }

    func updateProfile(firstName: String, lastName: String, nickname: String, bio: String) -> Result<Void, StoreError> {
        guard let currentUserID = state.session.currentUserID,
              let index = state.users.firstIndex(where: { $0.id == currentUserID }) else {
            return .failure(.message("Пользователь не найден."))
        }

        var handle = nickname.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if !handle.hasPrefix("@") {
            handle = "@\(handle)"
        }

        if state.users.contains(where: { $0.nickname.lowercased() == handle && $0.id != currentUserID }) {
            return .failure(.message("Никнейм уже занят."))
        }

        mutate {
            $0.users[index].firstName = firstName.trimmingCharacters(in: .whitespacesAndNewlines)
            $0.users[index].lastName = lastName.trimmingCharacters(in: .whitespacesAndNewlines)
            $0.users[index].nickname = handle
            $0.users[index].bio = bio.trimmingCharacters(in: .whitespacesAndNewlines)
        }

        return .success(())
    }

    func updateAvatar(path: String) {
        guard let currentUserID = state.session.currentUserID,
              let index = state.users.firstIndex(where: { $0.id == currentUserID }) else {
            return
        }

        mutate {
            $0.users[index].avatarLocalPath = path
            $0.users[index].avatarAssetName = nil
        }
    }

    func setLanguage(_ language: AppLanguage) {
        mutate { $0.session.language = language }
    }

    func setSafetyMode(_ mode: SafetyMode) {
        mutate { $0.session.safetyMode = mode }
    }

    func selectTab(_ tab: RootTab) {
        mutate { $0.selectedTab = tab }
    }

    func selectConversationGroup(_ group: ConversationGroup) {
        mutate { $0.selectedConversationGroup = group }
    }

    func logout() {
        mutate {
            $0.session.currentUserID = nil
            $0.selectedTab = .chats
        }
    }

    func dismissRecoveryReveal() {
        mutate {
            $0.session.shouldRevealRecoveryKey = false
        }
    }

    func sendTextMessage(_ text: String, replyTo replyMessageID: UUID?, quotedExcerpt: String?, in conversationID: UUID) {
        guard let index = state.conversations.firstIndex(where: { $0.id == conversationID }),
              let currentUserID = state.session.currentUserID else {
            return
        }

        let sharedKey = state.conversations[index].sharedKey
        let message = MessageRecord(
            id: UUID(),
            senderID: currentUserID,
            sentAt: .now,
            body: crypto.encrypt(text.trimmingCharacters(in: .whitespacesAndNewlines), using: sharedKey),
            quotedBody: quotedExcerpt.flatMap { crypto.encrypt($0, using: sharedKey) },
            replyToMessageID: replyMessageID,
            forwardedFromTitle: nil,
            attachment: nil,
            reactions: [],
            transfer: .sent,
            reportCount: 0
        )

        mutate {
            $0.conversations[index].messages.append(message)
            $0.conversations[index].lastActivityAt = .now
        }
    }

    func forward(messageID: UUID, from sourceConversationID: UUID, to targetConversationID: UUID) {
        guard
            let sourceConversation = conversation(id: sourceConversationID),
            let sourceMessage = sourceConversation.messages.first(where: { $0.id == messageID }),
            let currentUserID = state.session.currentUserID,
            let targetIndex = state.conversations.firstIndex(where: { $0.id == targetConversationID })
        else {
            return
        }

        let sharedKey = state.conversations[targetIndex].sharedKey
        let sourceText = decryptedText(for: sourceMessage, in: sourceConversation)
        let forwarded = MessageRecord(
            id: UUID(),
            senderID: currentUserID,
            sentAt: .now,
            body: crypto.encrypt(sourceText, using: sharedKey),
            quotedBody: nil,
            replyToMessageID: nil,
            forwardedFromTitle: sourceConversation.title,
            attachment: sourceMessage.attachment,
            reactions: [],
            transfer: .sent,
            reportCount: 0
        )

        mutate {
            $0.conversations[targetIndex].messages.append(forwarded)
            $0.conversations[targetIndex].lastActivityAt = .now
        }
    }

    func toggleReaction(_ emoji: String, messageID: UUID, conversationID: UUID) {
        guard
            let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
            let messageIndex = state.conversations[conversationIndex].messages.firstIndex(where: { $0.id == messageID }),
            let currentUserID = state.session.currentUserID
        else {
            return
        }

        mutate {
            var reactions = $0.conversations[conversationIndex].messages[messageIndex].reactions

            if let reactionIndex = reactions.firstIndex(where: { $0.emoji == emoji }) {
                if reactions[reactionIndex].userIDs.contains(currentUserID) {
                    reactions[reactionIndex].userIDs.removeAll(where: { $0 == currentUserID })
                } else {
                    reactions[reactionIndex].userIDs.append(currentUserID)
                }

                if reactions[reactionIndex].userIDs.isEmpty {
                    reactions.removeAll(where: { $0.emoji == emoji })
                }
            } else {
                reactions.append(MessageReaction(emoji: emoji, userIDs: [currentUserID]))
            }

            $0.conversations[conversationIndex].messages[messageIndex].reactions = reactions
        }
    }

    func reportMessage(messageID: UUID, conversationID: UUID, reason: ReportReason, note: String) {
        guard let currentUserID = state.session.currentUserID else { return }

        mutate {
            $0.reports.insert(
                MessageReportRecord(
                    id: UUID(),
                    messageID: messageID,
                    conversationID: conversationID,
                    reporterUserID: currentUserID,
                    reason: reason,
                    note: note,
                    createdAt: .now
                ),
                at: 0
            )

            guard
                let conversationIndex = $0.conversations.firstIndex(where: { $0.id == conversationID }),
                let messageIndex = $0.conversations[conversationIndex].messages.firstIndex(where: { $0.id == messageID })
            else { return }
            $0.conversations[conversationIndex].messages[messageIndex].reportCount += 1
        }
    }

    func queueAttachmentMessage(from url: URL, previewURL: URL?, kind: AttachmentKind, duration: TimeInterval?, in conversationID: UUID, replyTo replyMessageID: UUID?, quotedExcerpt: String?) {
        guard
            let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
            let currentUserID = state.session.currentUserID
        else {
            return
        }

        let copiedURL = (try? QGMediaTools.copyItemIntoAppSupport(from: url, folder: "attachments")) ?? url
        let previewPath = previewURL?.path
        let attachment = MessageAttachment(
            id: UUID(),
            kind: kind,
            name: copiedURL.lastPathComponent,
            localPath: copiedURL.path,
            previewPath: previewPath,
            fileSizeBytes: QGMediaTools.fileSize(for: copiedURL),
            duration: duration
        )

        let sharedKey = state.conversations[conversationIndex].sharedKey
        let messageID = UUID()

        let message = MessageRecord(
            id: messageID,
            senderID: currentUserID,
            sentAt: .now,
            body: crypto.encrypt(attachment.name, using: sharedKey),
            quotedBody: quotedExcerpt.flatMap { crypto.encrypt($0, using: sharedKey) },
            replyToMessageID: replyMessageID,
            forwardedFromTitle: nil,
            attachment: attachment,
            reactions: [],
            transfer: MessageTransferState(phase: .sending, progress: 0.05),
            reportCount: 0
        )

        mutate {
            $0.conversations[conversationIndex].messages.append(message)
            $0.conversations[conversationIndex].lastActivityAt = .now
            $0.cacheSizeBytes += attachment.fileSizeBytes
        }

        Task { @MainActor in
            for step in 1...8 {
                try? await Task.sleep(for: .milliseconds(180))
                updateTransfer(messageID: messageID, conversationID: conversationID, progress: Double(step) / 8)
            }
        }
    }

    func updateTransfer(messageID: UUID, conversationID: UUID, progress: Double) {
        guard
            let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
            let messageIndex = state.conversations[conversationIndex].messages.firstIndex(where: { $0.id == messageID })
        else {
            return
        }

        mutate {
            $0.conversations[conversationIndex].messages[messageIndex].transfer = MessageTransferState(
                phase: progress >= 1 ? .sent : .sending,
                progress: progress
            )
        }
    }

    func generateInvite() -> Result<InviteRecord, StoreError> {
        guard let currentUser = currentUser else {
            return .failure(.message("Сначала войдите в аккаунт."))
        }

        let activeCount = state.invites.filter { $0.inviterUserID == currentUser.id && $0.createdAt >= calendar.date(byAdding: .day, value: -7, to: .now)! }.count
        let weeklyLimit = currentUser.trustLevel.weeklyInviteLimit

        guard weeklyLimit > 0 else {
            return .failure(.message("Для инвайтов нужен минимум 2 уровень доверия."))
        }

        guard activeCount < weeklyLimit || weeklyLimit == .max else {
            return .failure(.message("Еженедельный лимит инвайтов исчерпан."))
        }

        let code = String((0..<3).map { _ in String.randomToken(length: 4) }.joined(separator: "-"))
        let invite = InviteRecord(
            id: UUID(),
            code: code,
            linkToken: UUID().uuidString.lowercased(),
            createdAt: .now,
            expiresAt: calendar.date(byAdding: .day, value: 4, to: .now) ?? .now,
            inviterUserID: currentUser.id,
            boundDeviceID: nil,
            redeemedByUserID: nil
        )

        mutate {
            $0.invites.insert(invite, at: 0)
        }

        return .success(invite)
    }

    func clearCallHistory() {
        mutate { $0.calls = [] }
    }

    func invitesForCurrentUser() -> [InviteRecord] {
        guard let currentUserID = state.session.currentUserID else { return [] }
        return state.invites
            .filter { $0.inviterUserID == currentUserID }
            .sorted { $0.createdAt > $1.createdAt }
    }

    func inviteTreeRoots() -> [UserProfile] {
        state.users
            .filter { $0.invitedByUserID == nil }
            .sorted { $0.joinedAt < $1.joinedAt }
    }

    func invitedChildren(of userID: UUID) -> [UserProfile] {
        state.users
            .filter { $0.invitedByUserID == userID }
            .sorted { $0.joinedAt < $1.joinedAt }
    }

    func updateTrust(for userID: UUID, delta: Int) {
        guard let index = state.users.firstIndex(where: { $0.id == userID }) else { return }
        let current = state.users[index].trustLevel.rawValue
        let next = min(max(current + delta, 1), 8)

        guard let level = TrustLevel(rawValue: next) else { return }
        mutate {
            $0.users[index].trustLevel = level
        }
    }

    func banBranch(startingAt userID: UUID) {
        var toBan = Set([userID])
        var queue = [userID]

        while let current = queue.first {
            queue.removeFirst()
            let children = invitedChildren(of: current).map(\.id)
            queue.append(contentsOf: children)
            toBan.formUnion(children)
        }

        mutate {
            for index in $0.users.indices where toBan.contains($0.users[index].id) {
                $0.users[index].isBanned = true
                $0.users[index].presence = .offline
            }
        }
    }

    func startCall(with userID: UUID, kind: CallKind) {
        let title = user(id: userID)?.displayName ?? "Звонок"
        activeCall = ActiveCallSession(
            id: UUID(),
            userID: userID,
            title: title,
            kind: kind,
            startedAt: .now,
            isMuted: false,
            isSpeakerEnabled: kind != .audio
        )
    }

    func endActiveCall() {
        guard let activeCall else { return }

        let duration = Date().timeIntervalSince(activeCall.startedAt)
        let record = CallRecord(
            id: UUID(),
            peerUserID: activeCall.userID,
            title: activeCall.title,
            startedAt: activeCall.startedAt,
            duration: duration,
            direction: .outgoing,
            kind: activeCall.kind
        )

        mutate {
            $0.calls.insert(record, at: 0)
        }

        self.activeCall = nil
    }

    func toggleMute() {
        guard var activeCall else { return }
        activeCall.isMuted.toggle()
        self.activeCall = activeCall
    }

    func toggleSpeaker() {
        guard var activeCall else { return }
        activeCall.isSpeakerEnabled.toggle()
        self.activeCall = activeCall
    }

    func clearCache() {
        mutate {
            $0.cacheSizeBytes = 0
            for conversationIndex in $0.conversations.indices {
                for messageIndex in $0.conversations[conversationIndex].messages.indices {
                    if $0.conversations[conversationIndex].messages[messageIndex].attachment != nil {
                        $0.conversations[conversationIndex].messages[messageIndex].attachment?.previewPath = nil
                    }
                }
            }
        }
    }

    private func mutate(_ transform: (inout AppState) -> Void) {
        var copy = state
        transform(&copy)
        state = copy
        persist()
    }

    private func persist() {
        if let data = try? JSONEncoder().encode(state) {
            try? data.write(to: persistenceURL, options: [.atomic])
        }
    }

    private func makeEncryptedMessage(senderID: UUID, text: String, sharedKey: String) -> MessageRecord {
        MessageRecord(
            id: UUID(),
            senderID: senderID,
            sentAt: .now,
            body: crypto.encrypt(text, using: sharedKey),
            quotedBody: nil,
            replyToMessageID: nil,
            forwardedFromTitle: nil,
            attachment: nil,
            reactions: [],
            transfer: .sent,
            reportCount: 0
        )
    }

    private static func makeSeedState(crypto: QGCryptoService) -> AppState {
        let adminID = UUID(uuidString: "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA")!
        let mikeID = UUID(uuidString: "BBBBBBBB-BBBB-BBBB-BBBB-BBBBBBBBBBBB")!
        let ilyaID = UUID(uuidString: "CCCCCCCC-CCCC-CCCC-CCCC-CCCCCCCCCCCC")!
        let evgenyID = UUID(uuidString: "DDDDDDDD-DDDD-DDDD-DDDD-DDDDDDDDDDDD")!
        let newsID = UUID(uuidString: "EEEEEEEE-EEEE-EEEE-EEEE-EEEEEEEEEEEE")!

        let users: [UserProfile] = [
            UserProfile(
                id: adminID,
                firstName: "Root",
                lastName: "Admin",
                nickname: "@root",
                emailOrPhone: "root@qgramm.local",
                bio: "Invitation graph administrator",
                trustLevel: .eight,
                presence: .online,
                joinedAt: .distantPast,
                avatarLocalPath: nil,
                avatarAssetName: nil,
                isAdmin: true,
                isBanned: false,
                invitedByUserID: nil
            ),
            UserProfile(
                id: mikeID,
                firstName: "Михаил",
                lastName: "Гуменюк",
                nickname: "@mike",
                emailOrPhone: "mike@qgramm.app",
                bio: "Invite-only product owner",
                trustLevel: .five,
                presence: .online,
                joinedAt: .now.addingTimeInterval(-86_400 * 24),
                avatarLocalPath: nil,
                avatarAssetName: nil,
                isAdmin: false,
                isBanned: false,
                invitedByUserID: adminID
            ),
            UserProfile(
                id: ilyaID,
                firstName: "Илья",
                lastName: "Бурмоличенко",
                nickname: "@ilya",
                emailOrPhone: "ilya@qgramm.app",
                bio: "Core chat contact",
                trustLevel: .three,
                presence: .online,
                joinedAt: .now.addingTimeInterval(-86_400 * 14),
                avatarLocalPath: nil,
                avatarAssetName: nil,
                isAdmin: false,
                isBanned: false,
                invitedByUserID: mikeID
            ),
            UserProfile(
                id: evgenyID,
                firstName: "Евгений",
                lastName: "Шегай",
                nickname: "@evgeny",
                emailOrPhone: "evgeny@qgramm.app",
                bio: "Thread participant",
                trustLevel: .two,
                presence: .typing,
                joinedAt: .now.addingTimeInterval(-86_400 * 7),
                avatarLocalPath: nil,
                avatarAssetName: nil,
                isAdmin: false,
                isBanned: false,
                invitedByUserID: mikeID
            ),
            UserProfile(
                id: newsID,
                firstName: "Qgramm",
                lastName: "News",
                nickname: "@updates",
                emailOrPhone: "updates@qgramm.app",
                bio: "Service channel",
                trustLevel: .eight,
                presence: .online,
                joinedAt: .now.addingTimeInterval(-86_400 * 30),
                avatarLocalPath: nil,
                avatarAssetName: nil,
                isAdmin: false,
                isBanned: false,
                invitedByUserID: adminID
            )
        ]

        func message(_ text: String, from sender: UUID, key: String, minutesAgo: Double) -> MessageRecord {
            MessageRecord(
                id: UUID(),
                senderID: sender,
                sentAt: .now.addingTimeInterval(-minutesAgo * 60),
                body: crypto.encrypt(text, using: key),
                quotedBody: nil,
                replyToMessageID: nil,
                forwardedFromTitle: nil,
                attachment: nil,
                reactions: [],
                transfer: .sent,
                reportCount: 0
            )
        }

        let chatKey = crypto.makeSharedKey()
        let threadKey = crypto.makeSharedKey()
        let channelKey = crypto.makeSharedKey()
        let groupKey = crypto.makeSharedKey()

        let chats = [
            ConversationRecord(
                id: UUID(),
                category: .chats,
                title: "Илья Бурмоличенко",
                participantIDs: [mikeID, ilyaID],
                sharedKey: chatKey,
                lastActivityAt: .now.addingTimeInterval(-160),
                unreadCount: 1,
                onlineUserID: ilyaID,
                messages: [
                    message("Спасибо за ответ! Я напишу через 10 минут.", from: ilyaID, key: chatKey, minutesAgo: 3),
                    message("Жду. Если что, отправь голосовым.", from: mikeID, key: chatKey, minutesAgo: 2.5)
                ]
            ),
            ConversationRecord(
                id: UUID(),
                category: .groups,
                title: "Core Team V1",
                participantIDs: [mikeID, ilyaID, evgenyID],
                sharedKey: groupKey,
                lastActivityAt: .now.addingTimeInterval(-2_100),
                unreadCount: 0,
                onlineUserID: nil,
                messages: [
                    message("Файловую отправку уже проверили на chunk progress.", from: ilyaID, key: groupKey, minutesAgo: 40)
                ]
            ),
            ConversationRecord(
                id: UUID(),
                category: .channels,
                title: "Qgramm Updates",
                participantIDs: [mikeID, newsID],
                sharedKey: channelKey,
                lastActivityAt: .now.addingTimeInterval(-7_200),
                unreadCount: 2,
                onlineUserID: nil,
                messages: [
                    message("В V1 включены личные чаты, звонки и invite-graph.", from: newsID, key: channelKey, minutesAgo: 120)
                ]
            ),
            ConversationRecord(
                id: UUID(),
                category: .threads,
                title: "Trust Engine",
                participantIDs: [mikeID, evgenyID],
                sharedKey: threadKey,
                lastActivityAt: .now.addingTimeInterval(-4_200),
                unreadCount: 0,
                onlineUserID: evgenyID,
                messages: [
                    message("Сначала считаем возраст аккаунта и жалобы, потом уже поведение ветки инвайтов.", from: evgenyID, key: threadKey, minutesAgo: 78)
                ]
            )
        ]

        let calls = [
            CallRecord(id: UUID(), peerUserID: ilyaID, title: "Илья Бурмоличенко", startedAt: .now.addingTimeInterval(-6_000), duration: 15 * 60 + 34, direction: .outgoing, kind: .audio),
            CallRecord(id: UUID(), peerUserID: mikeID, title: "Михаил Гуменюк", startedAt: .now.addingTimeInterval(-10_000), duration: 12 * 60 + 5, direction: .incoming, kind: .audio),
            CallRecord(id: UUID(), peerUserID: ilyaID, title: "Илья Бурмоличенко", startedAt: .now.addingTimeInterval(-90_000), duration: 22 * 60, direction: .outgoing, kind: .video)
        ]

        let invites = [
            InviteRecord(
                id: UUID(),
                code: "QGRA-MMV1-2026",
                linkToken: UUID().uuidString.lowercased(),
                createdAt: .now.addingTimeInterval(-12_000),
                expiresAt: .now.addingTimeInterval(86_400 * 3),
                inviterUserID: mikeID,
                boundDeviceID: nil,
                redeemedByUserID: nil
            ),
            InviteRecord(
                id: UUID(),
                code: "ROOT-NET-8888",
                linkToken: UUID().uuidString.lowercased(),
                createdAt: .now.addingTimeInterval(-86_400),
                expiresAt: .now.addingTimeInterval(86_400 * 2),
                inviterUserID: adminID,
                boundDeviceID: nil,
                redeemedByUserID: mikeID
            )
        ]

        let session = SessionState(
            deviceBindingID: UUID().uuidString,
            acceptedInviteCode: nil,
            pendingContact: "",
            expectedVerificationCode: "",
            currentUserID: nil,
            recoveryKey: "",
            shouldRevealRecoveryKey: false,
            language: .russian,
            safetyMode: .classic
        )

        return AppState(
            session: session,
            selectedTab: .chats,
            selectedConversationGroup: .chats,
            users: users,
            conversations: chats,
            invites: invites,
            calls: calls,
            reports: [],
            cacheSizeBytes: 2_200_000_000
        )
    }
}

private extension String {
    static func randomToken(length: Int) -> String {
        let alphabet = Array("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")
        return String((0..<length).compactMap { _ in alphabet.randomElement() })
    }
}
