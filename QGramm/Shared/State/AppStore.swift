import Foundation
import Network
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
    @Published var isChatRoomOpen = false
    @Published private(set) var isOnline = true
    @Published private(set) var connectionPingMs = 42

    private let crypto = QGCryptoService()
    private let backend = QGBackendClient()
    private let persistenceURL: URL
    private let calendar = Calendar.current
    private let pathMonitor = NWPathMonitor()
    private let pathQueue = DispatchQueue(label: "qgramm.path.monitor")
    private var pingTimer: Timer?
    private var syncTimer: Timer?
    private var activeBackendCallID: String?

    init() {
        let supportDirectory = (try? QGMediaTools.applicationSupportDirectory()) ?? FileManager.default.temporaryDirectory
        persistenceURL = supportDirectory.appendingPathComponent("state.json")

        if
            let data = try? Data(contentsOf: persistenceURL),
            let loaded = try? JSONDecoder().decode(AppState.self, from: data)
        {
            state = loaded
            if state.session.accessToken.isEmpty {
                state.session.currentUserID = nil
                state.session.expectedVerificationCode = ""
            }
        } else {
            state = Self.makeEmptyState()
            persist()
        }

        backend.setAccessToken(state.session.accessToken)

        UserDefaults.standard.set(state.session.isPowerSavingEnabled, forKey: QGHaptics.powerSavingKey)
        startPathMonitoring()

        Task { @MainActor in
            await bootstrapRemoteSession()
        }
    }

    deinit {
        pathMonitor.cancel()
        pingTimer?.invalidate()
        syncTimer?.invalidate()
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

    func setBackendBaseURL(_ value: String) {
        backend.setBaseURL(value)
    }

    func bootstrapRemoteSession() async {
        if !state.session.accessToken.isEmpty {
            do {
                try await refreshFromServer()
                return
            } catch {
                mutate {
                    $0.session.accessToken = ""
                    $0.session.accessTokenExpiresAt = nil
                    $0.session.currentUserID = nil
                }
                backend.clearAccessToken()
            }
        }

        do {
            let status = try await backend.checkDeviceActivation(deviceFingerprint: state.session.deviceBindingID)
            if status.allowed, let inviteCode = status.inviteCode {
                mutate { $0.session.acceptedInviteCode = inviteCode }
            }
        } catch {
            // keep UI available even when backend is temporarily unreachable
        }
    }

    func refreshFromServer() async throws {
        let me = try await backend.me()
        upsertUser(from: me)

        let graphNodes = (try? await backend.inviteGraph()) ?? []
        for node in graphNodes {
            upsertUser(from: node)
        }

        let invites = (try? await backend.listInvites()) ?? []
        let chats = try await backend.listChats()
        let calls = (try? await backend.listCalls()) ?? []

        let remoteConversations = try await loadRemoteConversations(chats: chats)
        let remoteInvites = invites.compactMap(mapInvite(_:))
        let remoteCalls = calls.compactMap { mapCall($0, currentUserID: me.id) }

        mutate {
            $0.session.currentUserID = UUID(uuidString: me.id)
            $0.session.authPurpose = .register
            $0.session.expectedVerificationCode = ""
            $0.invites = remoteInvites
            $0.conversations = remoteConversations.sorted { $0.lastActivityAt > $1.lastActivityAt }
            $0.calls = remoteCalls.sorted { $0.startedAt > $1.startedAt }
        }
        startAutoSync()
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
            case .media: return "Фото/видео"
            case .voiceNote: return "Голосовое сообщение"
            case .circularVideo: return "Кружочек"
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

        if let nickname = user(id: userID)?.nickname {
            Task { @MainActor in
                do {
                    _ = try await backend.directByNickname(nickname)
                    try await refreshFromServer()
                } catch {
                    // Keep local fallback.
                }
            }
        }

        return conversation.id
    }

    func startConversation(byNickname nickname: String) async -> Result<UUID, StoreError> {
        var normalized = nickname.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if !normalized.hasPrefix("@") {
            normalized = "@\(normalized)"
        }
        guard normalized.count > 1 else {
            return .failure(.message("Введите корректный @nickname."))
        }

        do {
            let remote = try await backend.directByNickname(normalized)
            try await refreshFromServer()

            if let remoteID = UUID(uuidString: remote.id),
               state.conversations.contains(where: { $0.id == remoteID }) {
                return .success(remoteID)
            }

            if let fallback = state.conversations.first(where: { conversation in
                conversation.participantIDs
                    .filter { $0 != state.session.currentUserID }
                    .compactMap(user(id:))
                    .contains(where: { $0.nickname.lowercased() == normalized })
            }) {
                return .success(fallback.id)
            }

            return .failure(.message("Диалог создан, но не был найден в списке чатов. Обновите экран."))
        } catch {
            if let local = state.users.first(where: {
                $0.nickname.lowercased() == normalized && $0.id != state.session.currentUserID
            }) {
                return .success(startConversation(with: local.id))
            }
            return .failure(.message(error.localizedDescription))
        }
    }

    func acceptInvite(code: String) async -> Bool {
        let normalized = code.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        guard !normalized.isEmpty else {
            return false
        }

        do {
            let status = try await backend.activateInvite(code: normalized, deviceFingerprint: state.session.deviceBindingID)
            guard status.allowed else { return false }
            mutate {
                $0.session.acceptedInviteCode = normalized
                $0.session.authPurpose = .register
            }
            return true
        } catch {
            if let invite = state.invites.first(where: { $0.code == normalized }) {
                mutate { $0.session.acceptedInviteCode = invite.code }
                return true
            }
            return false
        }
    }

    func sendVerificationCode(to contact: String, captchaPassed: Bool, purpose: AuthPurpose) async -> String? {
        let normalized = contact.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalized.isEmpty, captchaPassed else { return nil }
        if purpose == .register, !state.session.hasBoundInvite {
            return nil
        }

        do {
            let response = try await backend.sendCode(
                email: normalized,
                purpose: purpose,
                captchaToken: "pass-captcha",
                deviceFingerprint: state.session.deviceBindingID
            )

            mutate {
                $0.session.pendingContact = normalized
                $0.session.expectedVerificationCode = response.debugCode ?? "sent"
                $0.session.authPurpose = purpose
            }

            return response.debugCode
        } catch {
            return nil
        }
    }

    func completeRegistration(firstName: String, lastName: String, nickname: String, enteredCode: String, safetyMode: SafetyMode) async -> Result<String, StoreError> {
        let cleanedFirstName = firstName.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanedLastName = lastName.trimmingCharacters(in: .whitespacesAndNewlines)
        var handle = nickname.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if !handle.hasPrefix("@") {
            handle = "@\(handle)"
        }

        guard !cleanedFirstName.isEmpty, !cleanedLastName.isEmpty else {
            return .failure(.message("Имя и фамилия обязательны."))
        }

        guard !enteredCode.isEmpty else {
            return .failure(.message("Код подтверждения не совпадает или пустой."))
        }

        let recoveryKey = crypto.generateRecoveryKey()

        do {
            let auth = try await backend.register(
                email: state.session.pendingContact,
                code: enteredCode,
                deviceFingerprint: state.session.deviceBindingID,
                firstName: cleanedFirstName,
                lastName: cleanedLastName,
                nickname: handle,
                recoveryCiphertext: recoveryKey,
                recoveryMeta: ["mode": safetyMode.rawValue]
            )
            backend.setAccessToken(auth.accessToken)
            mutate {
                $0.session.currentUserID = UUID(uuidString: auth.user.id)
                $0.session.accessToken = auth.accessToken
                $0.session.accessTokenExpiresAt = auth.expiresAt
                $0.session.recoveryKey = recoveryKey
                $0.session.shouldRevealRecoveryKey = true
                $0.session.expectedVerificationCode = ""
                $0.session.safetyMode = safetyMode
                $0.session.authPurpose = .register
            }
            try await refreshFromServer()
            return .success(recoveryKey)
        } catch {
            return .failure(.message(error.localizedDescription))
        }
    }

    func login(enteredCode: String) async -> Result<Void, StoreError> {
        guard !state.session.pendingContact.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return .failure(.message("Введите email для входа."))
        }
        guard !enteredCode.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return .failure(.message("Введите код из письма."))
        }

        do {
            let auth = try await backend.login(
                email: state.session.pendingContact,
                code: enteredCode,
                deviceFingerprint: state.session.deviceBindingID
            )
            backend.setAccessToken(auth.accessToken)
            mutate {
                $0.session.currentUserID = UUID(uuidString: auth.user.id)
                $0.session.accessToken = auth.accessToken
                $0.session.accessTokenExpiresAt = auth.expiresAt
                $0.session.expectedVerificationCode = ""
                $0.session.authPurpose = .login
            }
            try await refreshFromServer()
            return .success(())
        } catch {
            return .failure(.message(error.localizedDescription))
        }
    }

    func restoreLocalAccount() {
        if !state.session.accessToken.isEmpty {
            Task { @MainActor in
                try? await refreshFromServer()
            }
            return
        }
        if state.session.currentUserID == nil, let local = state.users.first(where: { !$0.isBanned }) {
            mutate { $0.session.currentUserID = local.id }
        }
    }

    func updateProfile(firstName: String, lastName: String, nickname: String, bio: String) async -> Result<Void, StoreError> {
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

        do {
            _ = try await backend.updateMe(
                firstName: firstName.trimmingCharacters(in: .whitespacesAndNewlines),
                lastName: lastName.trimmingCharacters(in: .whitespacesAndNewlines),
                nickname: handle,
                avatarPath: user(id: currentUserID)?.avatarLocalPath
            )
            try await refreshFromServer()
        } catch {
            return .failure(.message(error.localizedDescription))
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

        Task { @MainActor in
            do {
                _ = try await uploadAvatar(localPath: path)
                try await refreshFromServer()
            } catch {
                // Keep local avatar if backend is temporarily unreachable.
            }
        }
    }

    func setLanguage(_ language: AppLanguage) {
        mutate { $0.session.language = language }
    }

    func setSafetyMode(_ mode: SafetyMode) {
        mutate { $0.session.safetyMode = mode }
    }

    func setAuthPurpose(_ purpose: AuthPurpose) {
        mutate {
            $0.session.authPurpose = purpose
            $0.session.expectedVerificationCode = ""
        }
    }

    func setPowerSavingEnabled(_ isEnabled: Bool) {
        mutate { $0.session.isPowerSavingEnabled = isEnabled }
        UserDefaults.standard.set(isEnabled, forKey: QGHaptics.powerSavingKey)
        if activeCall != nil {
            restartPingTimer()
        }
    }

    func setAppPinCode(_ pin: String) {
        mutate { $0.session.appPinCode = pin }
    }

    func clearAppPinCode() {
        mutate { $0.session.appPinCode = "" }
    }

    func setFaceIDEnabled(_ isEnabled: Bool) {
        mutate { $0.session.isFaceIDEnabled = isEnabled }
    }

    func validatePin(_ pin: String) -> Bool {
        state.session.appPinCode == pin
    }

    var hasPinCodeEnabled: Bool {
        !state.session.appPinCode.isEmpty
    }

    var needsAppUnlock: Bool {
        state.session.isAuthenticated && (hasPinCodeEnabled || state.session.isFaceIDEnabled)
    }

    func selectTab(_ tab: RootTab) {
        mutate { $0.selectedTab = tab }
    }

    func selectConversationGroup(_ group: ConversationGroup) {
        mutate { $0.selectedConversationGroup = group }
    }

    func setChatRoomOpen(_ isOpen: Bool) {
        isChatRoomOpen = isOpen
    }

    func logout() {
        if !state.session.accessToken.isEmpty {
            Task {
                try? await backend.logout()
            }
        }
        backend.clearAccessToken()
        mutate {
            $0.session.currentUserID = nil
            $0.session.accessToken = ""
            $0.session.accessTokenExpiresAt = nil
            $0.session.pendingContact = ""
            $0.session.expectedVerificationCode = ""
            $0.selectedTab = .chats
        }
        activeCall = nil
        activeBackendCallID = nil
        stopPingTimer()
        stopAutoSync()
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

        Task { @MainActor in
            var payload: [String: Any] = [
                "message_type": "text",
                "body_ciphertext": message.body?.ciphertext ?? "",
                "body_nonce": message.body?.nonce ?? "",
                "body_tag": message.body?.tag ?? "",
                "quoted_ciphertext": message.quotedBody?.ciphertext ?? "",
                "quoted_nonce": message.quotedBody?.nonce ?? "",
                "quoted_tag": message.quotedBody?.tag ?? "",
                "metadata": ["client": "ios"]
            ]
            if let replyMessageID {
                payload["reply_to_message_id"] = replyMessageID.uuidString
            }

            do {
                let remote = try await backend.sendMessage(conversationID: conversationID.uuidString, payload: payload)
                if let remoteID = UUID(uuidString: remote.id),
                   let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
                   let messageIndex = state.conversations[conversationIndex].messages.firstIndex(where: { $0.id == message.id }) {
                    mutate {
                        $0.conversations[conversationIndex].messages[messageIndex].id = remoteID
                        $0.conversations[conversationIndex].messages[messageIndex].transfer = .sent
                    }
                }
            } catch {
                if let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
                   let messageIndex = state.conversations[conversationIndex].messages.firstIndex(where: { $0.id == message.id }) {
                    mutate {
                        $0.conversations[conversationIndex].messages[messageIndex].transfer = MessageTransferState(phase: .failed, progress: 0)
                    }
                }
            }
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

        Task { @MainActor in
            var payload: [String: Any] = [
                "message_type": forwarded.attachment == nil ? "text" : backendMessageType(from: forwarded.attachment!.kind),
                "body_ciphertext": forwarded.body?.ciphertext ?? "",
                "body_nonce": forwarded.body?.nonce ?? "",
                "body_tag": forwarded.body?.tag ?? "",
                "forwarded_from_conversation_id": sourceConversationID.uuidString,
                "metadata": ["client": "ios", "forwarded": "true"]
            ]
            if let attachmentID = forwarded.attachment?.id {
                payload["attachment_id"] = attachmentID.uuidString
            }
            do {
                let remote = try await backend.sendMessage(conversationID: targetConversationID.uuidString, payload: payload)
                if let remoteID = UUID(uuidString: remote.id),
                   let cIndex = state.conversations.firstIndex(where: { $0.id == targetConversationID }),
                   let mIndex = state.conversations[cIndex].messages.firstIndex(where: { $0.id == forwarded.id }) {
                    mutate { $0.conversations[cIndex].messages[mIndex].id = remoteID }
                }
            } catch {
                if let cIndex = state.conversations.firstIndex(where: { $0.id == targetConversationID }),
                   let mIndex = state.conversations[cIndex].messages.firstIndex(where: { $0.id == forwarded.id }) {
                    mutate { $0.conversations[cIndex].messages[mIndex].transfer = MessageTransferState(phase: .failed, progress: 0) }
                }
            }
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

        var didRemoveCurrent = false

        mutate {
            var reactions = $0.conversations[conversationIndex].messages[messageIndex].reactions

            if let reactionIndex = reactions.firstIndex(where: { $0.emoji == emoji }) {
                if reactions[reactionIndex].userIDs.contains(currentUserID) {
                    reactions[reactionIndex].userIDs.removeAll(where: { $0 == currentUserID })
                    didRemoveCurrent = true
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

        Task {
            do {
                if didRemoveCurrent {
                    try await backend.removeReaction(conversationID: conversationID.uuidString, messageID: messageID.uuidString, emoji: emoji)
                } else {
                    try await backend.addReaction(conversationID: conversationID.uuidString, messageID: messageID.uuidString, emoji: emoji)
                }
            } catch {
                // UI already updated optimistically.
            }
        }
    }

    func reportMessage(messageID: UUID, conversationID: UUID, reason: ReportReason, note: String) -> Bool {
        guard let currentUserID = state.session.currentUserID else { return false }

        let alreadyReported = state.reports.contains {
            $0.messageID == messageID && $0.reporterUserID == currentUserID
        }
        if alreadyReported {
            return false
        }

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

        Task {
            _ = try? await backend.reportMessage(
                conversationID: conversationID.uuidString,
                messageID: messageID.uuidString,
                reason: reason.rawValue,
                note: note
            )
        }

        return true
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
            do {
                let attachmentID = try await uploadAttachment(
                    from: copiedURL,
                    kind: kind,
                    mimeType: mimeTypeForAttachment(url: copiedURL, kind: kind)
                ) { progress in
                    self.updateTransfer(messageID: messageID, conversationID: conversationID, progress: progress)
                }

                var payload: [String: Any] = [
                    "message_type": backendMessageType(from: kind),
                    "body_ciphertext": message.body?.ciphertext ?? "",
                    "body_nonce": message.body?.nonce ?? "",
                    "body_tag": message.body?.tag ?? "",
                    "quoted_ciphertext": message.quotedBody?.ciphertext ?? "",
                    "quoted_nonce": message.quotedBody?.nonce ?? "",
                    "quoted_tag": message.quotedBody?.tag ?? "",
                    "attachment_id": attachmentID.uuidString,
                    "metadata": [
                        "file_name": attachment.name,
                        "client": "ios",
                        "duration": duration ?? 0
                    ]
                ]
                if let replyMessageID {
                    payload["reply_to_message_id"] = replyMessageID.uuidString
                }
                let remote = try await backend.sendMessage(conversationID: conversationID.uuidString, payload: payload)
                if let remoteID = UUID(uuidString: remote.id),
                   let cIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
                   let mIndex = state.conversations[cIndex].messages.firstIndex(where: { $0.id == messageID }) {
                    mutate {
                        $0.conversations[cIndex].messages[mIndex].id = remoteID
                        $0.conversations[cIndex].messages[mIndex].attachment?.id = attachmentID
                        $0.conversations[cIndex].messages[mIndex].transfer = .sent
                    }
                }
            } catch {
                if let cIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
                   let mIndex = state.conversations[cIndex].messages.firstIndex(where: { $0.id == messageID }) {
                    mutate {
                        $0.conversations[cIndex].messages[mIndex].transfer = MessageTransferState(phase: .failed, progress: 0)
                    }
                }
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

    func generateInvite() async -> Result<InviteRecord, StoreError> {
        guard currentUser != nil else {
            return .failure(.message("Сначала войдите в аккаунт."))
        }

        do {
            let dto = try await backend.createInvite()
            guard let mapped = mapInvite(dto) else {
                return .failure(.message("Не удалось обработать инвайт сервера."))
            }
            mutate {
                $0.invites.removeAll(where: { $0.id == mapped.id })
                $0.invites.insert(mapped, at: 0)
            }
            return .success(mapped)
        } catch {
            return .failure(.message(error.localizedDescription))
        }
    }

    func clearCallHistory() {
        mutate { $0.calls = [] }
    }

    func refreshCallsFromServer() async {
        guard !state.session.accessToken.isEmpty else { return }
        let currentID = state.session.currentUserID?.uuidString ?? ""
        guard let remoteCalls = try? await backend.listCalls() else { return }
        mutate {
            $0.calls = remoteCalls.compactMap { self.mapCall($0, currentUserID: currentID) }
                .sorted { $0.startedAt > $1.startedAt }
        }
    }

    func invitesForCurrentUser() -> [InviteRecord] {
        guard let currentUserID = state.session.currentUserID else { return [] }
        return state.invites
            .filter { $0.inviterUserID == currentUserID }
            .sorted { $0.createdAt > $1.createdAt }
    }

    func remainingInvitesThisWeek() -> Int? {
        guard let currentUser else { return nil }
        let weeklyLimit = currentUser.trustLevel.weeklyInviteLimit
        guard weeklyLimit != .max else { return .max }
        return max(weeklyLimit - invitesUsedThisWeek(for: currentUser.id), 0)
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

        Task { @MainActor in
            _ = try? await backend.adminSetTrust(userID: userID.uuidString, trustLevel: next)
            try? await refreshFromServer()
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

        Task { @MainActor in
            _ = try? await backend.adminBlockUser(userID: userID.uuidString, block: true)
            try? await refreshFromServer()
        }
    }

    func blockUser(_ userID: UUID) {
        mutate {
            guard let userIndex = $0.users.firstIndex(where: { $0.id == userID }) else { return }
            $0.users[userIndex].isBanned = true
            $0.users[userIndex].presence = .offline
        }

        Task { @MainActor in
            _ = try? await backend.adminBlockUser(userID: userID.uuidString, block: true)
            try? await refreshFromServer()
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
        QGHaptics.light()
        restartPingTimer()
        refreshPing()

        Task { @MainActor in
            do {
                let remote = try await backend.startCall(calleeUserID: userID.uuidString, kind: kind.rawValue)
                activeBackendCallID = remote.id
            } catch {
                activeBackendCallID = nil
            }
        }
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

        let backendCallID = activeBackendCallID
        self.activeCall = nil
        activeBackendCallID = nil
        stopPingTimer()
        QGHaptics.light()

        if let backendCallID {
            Task { @MainActor in
                _ = try? await backend.endCall(callID: backendCallID, status: "ended", durationSeconds: Int(duration))
                if let remoteCalls = try? await backend.listCalls() {
                    let currentID = state.session.currentUserID?.uuidString ?? ""
                    mutate {
                        $0.calls = remoteCalls.compactMap { self.mapCall($0, currentUserID: currentID) }
                            .sorted { $0.startedAt > $1.startedAt }
                    }
                }
            }
        }
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

    private func loadRemoteConversations(chats: [QGConversationDTO]) async throws -> [ConversationRecord] {
        var mapped: [ConversationRecord] = []
        for chat in chats {
            guard let conversationID = UUID(uuidString: chat.id) else { continue }
            let existingKey = state.conversations.first(where: { $0.id == conversationID })?.sharedKey
            let sharedKey = existingKey ?? crypto.makeSharedKey()

            for participantRaw in chat.participantIDs {
                guard let participantID = UUID(uuidString: participantRaw) else { continue }
                if user(id: participantID) == nil {
                    let placeholder = UserProfile(
                        id: participantID,
                        firstName: "User",
                        lastName: participantRaw.prefix(6).uppercased(),
                        nickname: "@\(participantRaw.prefix(8))",
                        emailOrPhone: "",
                        bio: "",
                        trustLevel: .one,
                        presence: .offline,
                        joinedAt: .now,
                        avatarLocalPath: nil,
                        avatarAssetName: nil,
                        isAdmin: false,
                        isBanned: false,
                        invitedByUserID: nil
                    )
                    mutate { $0.users.append(placeholder) }
                }
            }

            let messages = (try? await backend.listMessages(conversationID: chat.id, limit: 200)) ?? []
            let localMessages = messages.compactMap { mapMessage($0, sharedKey: sharedKey) }
            let category: ConversationGroup = {
                switch chat.kind {
                case "group": return .groups
                case "channel": return .channels
                case "thread": return .threads
                default: return .chats
                }
            }()

            let onlinePeerID: UUID? = {
                let participants = chat.participantIDs.compactMap(UUID.init(uuidString:))
                guard let currentUserID = state.session.currentUserID else { return participants.first }
                return participants.first(where: { $0 != currentUserID })
            }()

            mapped.append(ConversationRecord(
                id: conversationID,
                category: category,
                title: chat.title,
                participantIDs: chat.participantIDs.compactMap(UUID.init(uuidString:)),
                sharedKey: sharedKey,
                lastActivityAt: chat.updatedAt,
                unreadCount: chat.unreadCount,
                onlineUserID: onlinePeerID,
                messages: localMessages.sorted { $0.sentAt < $1.sentAt }
            ))
        }
        return mapped
    }

    private func upsertUser(from dto: QGAuthUserDTO) {
        guard let id = UUID(uuidString: dto.id) else { return }
        let profile = UserProfile(
            id: id,
            firstName: dto.firstName,
            lastName: dto.lastName,
            nickname: dto.nickname,
            emailOrPhone: dto.email,
            bio: "",
            trustLevel: trustLevel(from: dto.trustLevel),
            presence: .online,
            joinedAt: dto.createdAt,
            avatarLocalPath: dto.avatarURL,
            avatarAssetName: nil,
            isAdmin: dto.isRoot,
            isBanned: dto.isBanned,
            invitedByUserID: dto.invitedByUserID.flatMap(UUID.init(uuidString:))
        )
        upsertUser(profile)
    }

    private func upsertUser(from node: QGInviteGraphNodeDTO) {
        guard let id = UUID(uuidString: node.userID) else { return }
        if let index = state.users.firstIndex(where: { $0.id == id }) {
            mutate {
                $0.users[index].nickname = node.nickname.hasPrefix("@") ? node.nickname : "@\(node.nickname)"
                $0.users[index].trustLevel = trustLevel(from: node.trustLevel)
                $0.users[index].isBanned = node.isBanned
                $0.users[index].invitedByUserID = node.invitedByUserID.flatMap(UUID.init(uuidString:))
            }
            return
        }
        let nickname = node.nickname.hasPrefix("@") ? node.nickname : "@\(node.nickname)"
        let profile = UserProfile(
            id: id,
            firstName: nickname.replacingOccurrences(of: "@", with: ""),
            lastName: "",
            nickname: nickname,
            emailOrPhone: node.uid,
            bio: "",
            trustLevel: trustLevel(from: node.trustLevel),
            presence: .offline,
            joinedAt: node.createdAt,
            avatarLocalPath: nil,
            avatarAssetName: nil,
            isAdmin: false,
            isBanned: node.isBanned,
            invitedByUserID: node.invitedByUserID.flatMap(UUID.init(uuidString:))
        )
        upsertUser(profile)
    }

    private func upsertUser(_ profile: UserProfile) {
        if let index = state.users.firstIndex(where: { $0.id == profile.id }) {
            mutate { $0.users[index] = profile }
        } else {
            mutate { $0.users.append(profile) }
        }
    }

    private func mapInvite(_ dto: QGInviteDTO) -> InviteRecord? {
        guard let id = UUID(uuidString: dto.id), let inviterID = UUID(uuidString: dto.inviterUserID) else {
            return nil
        }
        return InviteRecord(
            id: id,
            code: dto.code,
            linkToken: dto.id.lowercased(),
            createdAt: dto.createdAt,
            expiresAt: dto.expiresAt,
            inviterUserID: inviterID,
            boundDeviceID: nil,
            redeemedByUserID: nil
        )
    }

    private func mapMessage(_ dto: QGMessageDTO, sharedKey: String) -> MessageRecord? {
        guard let id = UUID(uuidString: dto.id) else { return nil }
        let senderID = UUID(uuidString: dto.senderUserID ?? "") ?? state.session.currentUserID ?? UUID()
        let envelope: SecureTextEnvelope? = {
            guard let bodyCiphertext = dto.bodyCiphertext, !bodyCiphertext.isEmpty else { return nil }
            return SecureTextEnvelope(
                nonce: dto.bodyNonce ?? "",
                ciphertext: bodyCiphertext,
                tag: dto.bodyTag ?? ""
            )
        }()
        let quotedEnvelope: SecureTextEnvelope? = {
            guard let quotedCiphertext = dto.quotedCiphertext, !quotedCiphertext.isEmpty else { return nil }
            return SecureTextEnvelope(
                nonce: dto.quotedNonce ?? "",
                ciphertext: quotedCiphertext,
                tag: dto.quotedTag ?? ""
            )
        }()
        let attachment: MessageAttachment? = {
            guard let attachmentRaw = dto.attachmentID, let attachmentID = UUID(uuidString: attachmentRaw) else { return nil }
            let kind = attachmentKind(from: dto.messageType) ?? .file
            let name = (dto.metadata?["file_name"]?.value.base as? String) ?? "Attachment"
            return MessageAttachment(
                id: attachmentID,
                kind: kind,
                name: name,
                localPath: nil,
                previewPath: nil,
                fileSizeBytes: 0,
                duration: nil
            )
        }()
        let reactions: [MessageReaction] = (dto.reactions ?? []).map { reaction in
            MessageReaction(
                emoji: reaction.emoji,
                userIDs: reaction.userIDs.compactMap(UUID.init(uuidString:))
            )
        }

        return MessageRecord(
            id: id,
            senderID: senderID,
            sentAt: dto.createdAt,
            body: envelope,
            quotedBody: quotedEnvelope,
            replyToMessageID: dto.replyToMessageID.flatMap(UUID.init(uuidString:)),
            forwardedFromTitle: dto.forwardedFromConversationID,
            attachment: attachment,
            reactions: reactions,
            transfer: .sent,
            reportCount: dto.reportCount
        )
    }

    private func mapCall(_ dto: QGCallDTO, currentUserID: String) -> CallRecord? {
        guard
            let callID = UUID(uuidString: dto.id),
            let callerID = UUID(uuidString: dto.callerUserID),
            let calleeID = UUID(uuidString: dto.calleeUserID)
        else {
            return nil
        }

        let isOutgoing = dto.callerUserID == currentUserID
        let peerID = isOutgoing ? calleeID : callerID
        let peerProfile = user(id: peerID)
        let peerTitle: String
        if let peerProfile, !peerProfile.displayName.trimmingCharacters(in: .whitespaces).isEmpty {
            peerTitle = peerProfile.displayName
        } else {
            peerTitle = peerProfile?.nickname ?? "Peer"
        }
        return CallRecord(
            id: callID,
            peerUserID: peerID,
            title: peerTitle,
            startedAt: dto.startedAt,
            duration: TimeInterval(dto.durationSeconds ?? 0),
            direction: isOutgoing ? .outgoing : .incoming,
            kind: CallKind(rawValue: dto.kind) ?? .audio
        )
    }

    private func trustLevel(from raw: Int) -> TrustLevel {
        TrustLevel(rawValue: min(max(raw, 1), 8)) ?? .one
    }

    private func backendMessageType(from kind: AttachmentKind) -> String {
        switch kind {
        case .file: return "file"
        case .media: return "media"
        case .voiceNote: return "voice_note"
        case .circularVideo: return "circular_video"
        }
    }

    private func attachmentKind(from messageType: String) -> AttachmentKind? {
        switch messageType {
        case "file": return .file
        case "media": return .media
        case "voice_note": return .voiceNote
        case "circular_video": return .circularVideo
        default: return nil
        }
    }

    private func backendUploadKind(from kind: AttachmentKind) -> String {
        switch kind {
        case .file: return "file"
        case .media: return "media"
        case .voiceNote: return "voice_note"
        case .circularVideo: return "circular_video"
        }
    }

    private func mimeTypeForAttachment(url: URL, kind: AttachmentKind) -> String {
        switch kind {
        case .voiceNote:
            return "audio/m4a"
        case .circularVideo:
            return "video/quicktime"
        case .media:
            return "image/jpeg"
        case .file:
            return "application/octet-stream"
        }
    }

    private func uploadAttachment(from url: URL, kind: AttachmentKind, mimeType: String, progress: @escaping (Double) -> Void) async throws -> UUID {
        let fileSize = QGMediaTools.fileSize(for: url)
        let chunkSize = 512 * 1024
        let upload = try await backend.createUpload(
            kind: backendUploadKind(from: kind),
            fileName: url.lastPathComponent,
            mimeType: mimeType,
            totalSize: fileSize,
            chunkSize: chunkSize
        )

        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }

        var index = 0
        var uploaded: Int64 = 0
        while true {
            let chunk = try handle.read(upToCount: chunkSize) ?? Data()
            if chunk.isEmpty { break }
            _ = try await backend.uploadChunk(uploadID: upload.id, chunkIndex: index, chunkData: chunk)
            uploaded += Int64(chunk.count)
            progress(min(Double(uploaded) / max(Double(fileSize), 1), 0.95))
            index += 1
        }

        let completed = try await backend.completeUpload(uploadID: upload.id)
        progress(1)
        guard let attachmentRaw = completed.finalizedAttachmentID, let attachmentID = UUID(uuidString: attachmentRaw) else {
            throw StoreError.message("Файл не был финализирован на сервере.")
        }
        return attachmentID
    }

    private func uploadAvatar(localPath: String) async throws -> UUID {
        let url = URL(fileURLWithPath: localPath)
        let fileSize = QGMediaTools.fileSize(for: url)
        let chunkSize = 256 * 1024
        let upload = try await backend.createUpload(
            kind: "avatar",
            fileName: url.lastPathComponent,
            mimeType: "image/jpeg",
            totalSize: fileSize,
            chunkSize: chunkSize
        )

        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var index = 0
        while true {
            let chunk = try handle.read(upToCount: chunkSize) ?? Data()
            if chunk.isEmpty { break }
            _ = try await backend.uploadChunk(uploadID: upload.id, chunkIndex: index, chunkData: chunk)
            index += 1
        }

        let completed = try await backend.completeUpload(uploadID: upload.id)
        guard let attachmentRaw = completed.finalizedAttachmentID, let attachmentID = UUID(uuidString: attachmentRaw) else {
            throw StoreError.message("Аватар не был сохранён на сервере.")
        }
        return attachmentID
    }

    private func startPathMonitoring() {
        pathMonitor.pathUpdateHandler = { [weak self] path in
            let online = path.status == .satisfied
            Task { @MainActor in
                guard let self else { return }
                self.isOnline = online
                self.refreshPing()
                if online, self.state.session.isAuthenticated {
                    try? await self.refreshFromServer()
                }
            }
        }
        pathMonitor.start(queue: pathQueue)
    }

    private func restartPingTimer() {
        stopPingTimer()
        let interval = state.session.isPowerSavingEnabled ? 5.0 : 2.0
        pingTimer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refreshPing()
            }
        }
    }

    private func invitesUsedThisWeek(for userID: UUID) -> Int {
        let start = startOfCurrentWeek()
        return state.invites.filter { $0.inviterUserID == userID && $0.createdAt >= start }.count
    }

    private func startOfCurrentWeek() -> Date {
        let components = calendar.dateComponents([.yearForWeekOfYear, .weekOfYear], from: .now)
        return calendar.date(from: components) ?? calendar.startOfDay(for: .now)
    }

    private func stopPingTimer() {
        pingTimer?.invalidate()
        pingTimer = nil
    }

    private func startAutoSync() {
        stopAutoSync()
        guard state.session.isAuthenticated, !state.session.accessToken.isEmpty else { return }
        syncTimer = Timer.scheduledTimer(withTimeInterval: 8, repeats: true) { [weak self] _ in
            Task { @MainActor in
                guard let self else { return }
                if self.state.session.isAuthenticated {
                    try? await self.refreshFromServer()
                }
            }
        }
    }

    private func stopAutoSync() {
        syncTimer?.invalidate()
        syncTimer = nil
    }

    private func refreshPing() {
        guard isOnline else {
            connectionPingMs = 999
            return
        }

        if activeCall == nil {
            connectionPingMs = Int.random(in: 28...70)
            return
        }

        connectionPingMs = state.session.isPowerSavingEnabled
            ? Int.random(in: 45...190)
            : Int.random(in: 28...150)
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

    private static func makeEmptyState() -> AppState {
        let session = SessionState(
            deviceBindingID: UUID().uuidString,
            acceptedInviteCode: nil,
            pendingContact: "",
            expectedVerificationCode: "",
            currentUserID: nil,
            accessToken: "",
            accessTokenExpiresAt: nil,
            authPurpose: .register,
            recoveryKey: "",
            shouldRevealRecoveryKey: false,
            language: .russian,
            safetyMode: .classic,
            appPinCode: "",
            isFaceIDEnabled: false,
            isPowerSavingEnabled: false
        )

        return AppState(
            session: session,
            selectedTab: .chats,
            selectedConversationGroup: .chats,
            users: [],
            conversations: [],
            invites: [],
            calls: [],
            reports: [],
            cacheSizeBytes: 0
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
                firstName: "QGramm",
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
                title: "QGramm Updates",
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
            accessToken: "",
            accessTokenExpiresAt: nil,
            authPurpose: .register,
            recoveryKey: "",
            shouldRevealRecoveryKey: false,
            language: .russian,
            safetyMode: .classic,
            appPinCode: "",
            isFaceIDEnabled: false,
            isPowerSavingEnabled: false
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
