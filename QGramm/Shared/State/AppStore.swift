import Foundation
import Network
import SwiftUI
import CryptoKit
import AVFoundation

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
    private var realtimeTask: URLSessionWebSocketTask?
    private var realtimeReconnectTask: Task<Void, Never>?
    private var activeConversationID: UUID?
    private var attachmentHydrationTasks: [UUID: Task<Void, Never>] = [:]

    private static let legacyE2ESharedKeyMetadataField = "e2e_shared_key_v1"
    private static let clientMessageIDMetadataField = "client_message_id"
    private static let e2ePrivateKeyStorageKey = "qgramm.e2e.identity.private.v1"
    private static let e2ePublicKeyStorageKey = "qgramm.e2e.identity.public.v1"

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
        realtimeTask?.cancel(with: .goingAway, reason: nil)
        realtimeReconnectTask?.cancel()
        attachmentHydrationTasks.values.forEach { $0.cancel() }
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
        stopRealtime()
        ensureRealtimeConnected()
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
        var me = try await backend.me()
        upsertUser(from: me)
        me = await ensureE2EIdentityPublishedIfNeeded(remoteUser: me)
        upsertUser(from: me)

        let graphNodes = (try? await backend.inviteGraph()) ?? []
        for node in graphNodes {
            upsertUser(from: node)
        }

        let invites = try? await backend.listInvites()
        let chats = try? await backend.listChats()
        let calls = try? await backend.listCalls()

        let remoteConversations: [ConversationRecord]?
        if let chats {
            remoteConversations = try await loadRemoteConversations(chats: chats)
        } else {
            remoteConversations = nil
        }

        let remoteInvites = invites?.compactMap(mapInvite(_:)) ?? []
        let remoteCalls = calls?.compactMap { mapCall($0, currentUserID: me.id) } ?? []

        mutate {
            $0.session.currentUserID = UUID(uuidString: me.id)
            $0.session.authPurpose = .register
            $0.session.expectedVerificationCode = ""
            if invites != nil {
                $0.invites = mergeInvites(existing: $0.invites, remote: remoteInvites)
            }
            if let remoteConversations {
                $0.conversations = remoteConversations.sorted { $0.lastActivityAt > $1.lastActivityAt }
            }
            if calls != nil {
                $0.calls = remoteCalls.sorted { $0.startedAt > $1.startedAt }
            }
        }
        if let activeConversationID {
            let lastReadMessageID = markConversationReadLocally(activeConversationID)
            Task { @MainActor in
                await markConversationReadOnServer(activeConversationID, lastReadMessageID: lastReadMessageID)
            }
        }
        startAutoSync()
        ensureRealtimeConnected()
        if let activeConversationID {
            preloadAttachments(for: activeConversationID)
        }
    }

    func decryptedText(for message: MessageRecord, in conversation: ConversationRecord) -> String {
        if let envelope = message.body,
           let systemText = friendlySystemText(ciphertext: envelope.ciphertext) {
            return systemText
        }
        let key = message.encryptionKeyHint ?? conversation.sharedKey
        return crypto.decrypt(message.body, using: key)
    }

    func decryptedQuote(for message: MessageRecord, in conversation: ConversationRecord) -> String {
        let key = message.encryptionKeyHint ?? conversation.sharedKey
        return crypto.decrypt(message.quotedBody, using: key)
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
        let participantIDs = [state.session.currentUserID, userID].compactMap { $0 }
        let conversationID = UUID()
        let conversation = ConversationRecord(
            id: conversationID,
            category: .chats,
            title: title,
            participantIDs: participantIDs,
            sharedKey: resolvedConversationSharedKey(conversationID: conversationID, participantIDs: participantIDs, existingKey: nil),
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

    func sendVerificationCode(to contact: String, captchaPassed: Bool, purpose: AuthPurpose) async -> Result<String?, StoreError> {
        let normalized = contact.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalized.isEmpty else {
            return .failure(.message("Введите email."))
        }
        guard captchaPassed else {
            return .failure(.message("Подтвердите captcha."))
        }
        if purpose == .register, !state.session.hasBoundInvite {
            return .failure(.message("Сначала активируйте инвайт."))
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

            return .success(response.debugCode)
        } catch {
            return .failure(.message(error.localizedDescription))
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
                $0.users = []
                $0.conversations = []
                $0.invites = []
                $0.calls = []
                $0.reports = []
            }
            upsertUser(from: auth.user)
            try? await refreshFromServer()
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
                $0.users = []
                $0.conversations = []
                $0.invites = []
                $0.calls = []
                $0.reports = []
            }
            upsertUser(from: auth.user)
            try? await refreshFromServer()
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
        if state.session.isAuthenticated {
            startAutoSync()
        }
    }

    func openConversation(_ conversationID: UUID) {
        activeConversationID = conversationID
        let lastReadMessageID = markConversationReadLocally(conversationID)
        preloadAttachments(for: conversationID)
        Task { @MainActor in
            await markConversationReadOnServer(conversationID, lastReadMessageID: lastReadMessageID)
        }
    }

    func closeConversation(_ conversationID: UUID) {
        if activeConversationID == conversationID {
            activeConversationID = nil
        }
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
        activeConversationID = nil
        stopPingTimer()
        stopAutoSync()
        stopRealtime()
        attachmentHydrationTasks.values.forEach { $0.cancel() }
        attachmentHydrationTasks.removeAll()
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
            encryptionKeyHint: sharedKey,
            replyToMessageID: replyMessageID,
            forwardedFromTitle: nil,
            attachment: nil,
            reactions: [],
            transfer: MessageTransferState(phase: .sending, progress: 0.05),
            reportCount: 0
        )

        mutate {
            $0.conversations[index].messages.append(message)
            $0.conversations[index].lastActivityAt = .now
        }

        Task { @MainActor in
            let metadata = makeMessageMetadata(
                base: ["client": "ios"],
                clientMessageID: message.id
            )
            var payload: [String: Any] = [
                "message_type": "text",
                "body_ciphertext": message.body?.ciphertext ?? "",
                "body_nonce": message.body?.nonce ?? "",
                "body_tag": message.body?.tag ?? "",
                "quoted_ciphertext": message.quotedBody?.ciphertext ?? "",
                "quoted_nonce": message.quotedBody?.nonce ?? "",
                "quoted_tag": message.quotedBody?.tag ?? "",
                "metadata": metadata
            ]
            if let replyMessageID {
                payload["reply_to_message_id"] = replyMessageID.uuidString
            }

            do {
                let (remote, resolvedConversationID) = try await sendMessageWithConversationRecovery(
                    conversationID: conversationID,
                    payload: payload
                )
                reconcileLocalConversationID(oldID: conversationID, newID: resolvedConversationID)
                if let remoteID = UUID(uuidString: remote.id),
                   let (conversationIndex, messageIndex) = locateMessage(
                    id: message.id,
                    preferredConversationID: resolvedConversationID,
                    fallbackConversationID: conversationID
                   ) {
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
            encryptionKeyHint: sharedKey,
            replyToMessageID: nil,
            forwardedFromTitle: sourceConversation.title,
            attachment: sourceMessage.attachment,
            reactions: [],
            transfer: MessageTransferState(phase: .sending, progress: 0.05),
            reportCount: 0
        )

        mutate {
            $0.conversations[targetIndex].messages.append(forwarded)
            $0.conversations[targetIndex].lastActivityAt = .now
        }

        Task { @MainActor in
            let metadata = makeMessageMetadata(
                base: ["client": "ios", "forwarded": "true"],
                clientMessageID: forwarded.id
            )
            var payload: [String: Any] = [
                "message_type": forwarded.attachment == nil ? "text" : backendMessageType(from: forwarded.attachment!.kind),
                "body_ciphertext": forwarded.body?.ciphertext ?? "",
                "body_nonce": forwarded.body?.nonce ?? "",
                "body_tag": forwarded.body?.tag ?? "",
                "forwarded_from_conversation_id": sourceConversationID.uuidString,
                "metadata": metadata
            ]
            if let attachmentID = forwarded.attachment?.id {
                payload["attachment_id"] = attachmentID.uuidString
            }
            do {
                let (remote, resolvedConversationID) = try await sendMessageWithConversationRecovery(
                    conversationID: targetConversationID,
                    payload: payload
                )
                reconcileLocalConversationID(oldID: targetConversationID, newID: resolvedConversationID)
                if let remoteID = UUID(uuidString: remote.id),
                   let (cIndex, mIndex) = locateMessage(
                    id: forwarded.id,
                    preferredConversationID: resolvedConversationID,
                    fallbackConversationID: targetConversationID
                   ) {
                    mutate {
                        $0.conversations[cIndex].messages[mIndex].id = remoteID
                        $0.conversations[cIndex].messages[mIndex].transfer = .sent
                    }
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
            encryptionKeyHint: sharedKey,
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
                    "metadata": makeMessageMetadata(base: [
                        "file_name": attachment.name,
                        "client": "ios",
                        "duration": duration ?? 0,
                        "duration_seconds": Int((duration ?? 0).rounded()),
                        "attachment_size_bytes": attachment.fileSizeBytes
                    ], clientMessageID: messageID)
                ]
                if let replyMessageID {
                    payload["reply_to_message_id"] = replyMessageID.uuidString
                }
                let (remote, resolvedConversationID) = try await sendMessageWithConversationRecovery(
                    conversationID: conversationID,
                    payload: payload
                )
                reconcileLocalConversationID(oldID: conversationID, newID: resolvedConversationID)
                if let remoteID = UUID(uuidString: remote.id),
                   let (cIndex, mIndex) = locateMessage(
                    id: messageID,
                    preferredConversationID: resolvedConversationID,
                    fallbackConversationID: conversationID
                   ) {
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

    private func sendMessageWithConversationRecovery(
        conversationID: UUID,
        payload: [String: Any]
    ) async throws -> (QGMessageDTO, UUID) {
        do {
            let remote = try await backend.sendMessage(conversationID: conversationID.uuidString, payload: payload)
            return (remote, conversationID)
        } catch {
            guard shouldRecoverConversationAndRetry(error),
                  let recoveredConversationID = await recoverConversationIDForRetry(conversationID),
                  recoveredConversationID != conversationID else {
                throw error
            }

            let remote = try await backend.sendMessage(
                conversationID: recoveredConversationID.uuidString,
                payload: payload
            )
            return (remote, recoveredConversationID)
        }
    }

    private func shouldRecoverConversationAndRetry(_ error: Error) -> Bool {
        guard case let QGBackendError.server(message) = error else { return false }
        let normalized = message.lowercased()
        return normalized.contains("conversation access denied")
            || normalized.contains("invalid conversation_id")
            || normalized.contains("not found")
    }

    private func recoverConversationIDForRetry(_ conversationID: UUID) async -> UUID? {
        guard
            let conversation = state.conversations.first(where: { $0.id == conversationID }),
            conversation.category == .chats,
            let currentUserID = state.session.currentUserID,
            let peerID = conversation.participantIDs.first(where: { $0 != currentUserID }),
            let nickname = user(id: peerID)?.nickname,
            !nickname.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        else {
            return nil
        }

        do {
            let remoteConversation = try await backend.directByNickname(nickname)
            return UUID(uuidString: remoteConversation.id)
        } catch {
            return nil
        }
    }

    private func reconcileLocalConversationID(oldID: UUID, newID: UUID) {
        guard oldID != newID else { return }
        guard let oldIndex = state.conversations.firstIndex(where: { $0.id == oldID }) else { return }
        guard state.conversations.firstIndex(where: { $0.id == newID }) == nil else { return }

        mutate {
            $0.conversations[oldIndex].id = newID
        }
        if activeConversationID == oldID {
            activeConversationID = newID
        }
    }

    private func locateMessage(
        id messageID: UUID,
        preferredConversationID: UUID,
        fallbackConversationID: UUID
    ) -> (conversationIndex: Int, messageIndex: Int)? {
        let candidates = preferredConversationID == fallbackConversationID
            ? [preferredConversationID]
            : [preferredConversationID, fallbackConversationID]

        for conversationID in candidates {
            guard let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
                  let messageIndex = state.conversations[conversationIndex].messages.firstIndex(where: { $0.id == messageID }) else {
                continue
            }
            return (conversationIndex, messageIndex)
        }
        return nil
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
            if let refreshed = try? await backend.listInvites() {
                let mappedRefreshed = refreshed.compactMap(mapInvite(_:))
                mutate {
                    $0.invites = mergeInvites(existing: $0.invites, remote: mappedRefreshed)
                }
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
        return state.invites
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
            let participantIDs = chat.participantIDs.compactMap(UUID.init(uuidString:))
            let existingKey = state.conversations.first(where: { $0.id == conversationID })?.sharedKey
            let sharedKey = resolvedConversationSharedKey(
                conversationID: conversationID,
                participantIDs: participantIDs,
                existingKey: existingKey
            )

            for participantRaw in chat.participantIDs {
                guard let participantID = UUID(uuidString: participantRaw) else { continue }
                if user(id: participantID) == nil {
                    let placeholder = UserProfile(
                        id: participantID,
                        firstName: "User",
                        lastName: participantRaw.prefix(6).uppercased(),
                        nickname: "@\(participantRaw.prefix(8))",
                        e2ePublicKey: nil,
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

            let existingMessages = state.conversations.first(where: { $0.id == conversationID })?.messages ?? []
            let localMessages: [MessageRecord]
            if let messages = try? await backend.listMessages(conversationID: chat.id, limit: 200) {
                localMessages = mergeRemoteMessages(messages, existingMessages: existingMessages, sharedKey: sharedKey)
            } else {
                localMessages = existingMessages.sorted { $0.sentAt < $1.sentAt }
            }
            let category: ConversationGroup = {
                switch chat.kind {
                case "group": return .groups
                case "channel": return .channels
                case "thread": return .threads
                default: return .chats
                }
            }()

            let onlinePeerID: UUID? = {
                let participants = participantIDs
                guard let currentUserID = state.session.currentUserID else { return participants.first }
                return participants.first(where: { $0 != currentUserID })
            }()

            mapped.append(ConversationRecord(
                id: conversationID,
                category: category,
                title: chat.title,
                participantIDs: participantIDs,
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
            e2ePublicKey: sanitizedE2EPublicKey(dto.e2ePublicKey),
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
            e2ePublicKey: sanitizedE2EPublicKey(node.e2ePublicKey),
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
            redeemedByUserID: nil,
            maxUses: dto.maxUses,
            usedCount: dto.usedCount,
            isRevoked: dto.isRevoked,
            serverIsActive: dto.isActive
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
            let name = metadataString(dto.metadata, key: "file_name")
                ?? metadataString(dto.metadata, key: "name")
                ?? "Attachment"
            let size = metadataInt64(dto.metadata, keys: ["attachment_size_bytes", "size_bytes"]) ?? 0
            let duration = metadataTimeInterval(dto.metadata, keys: ["duration_seconds", "duration"])
            return MessageAttachment(
                id: attachmentID,
                kind: kind,
                name: name,
                localPath: nil,
                previewPath: nil,
                fileSizeBytes: size,
                duration: duration
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
            encryptionKeyHint: sharedKeyFromMetadata(dto.metadata),
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

    private func mergeRemoteMessages(_ remoteMessages: [QGMessageDTO], existingMessages: [MessageRecord], sharedKey: String) -> [MessageRecord] {
        let existingByID = Dictionary(uniqueKeysWithValues: existingMessages.map { ($0.id, $0) })
        var acknowledgedPendingIDs = Set<UUID>()
        var merged: [MessageRecord] = []

        for dto in remoteMessages {
            guard var remote = mapMessage(dto, sharedKey: sharedKey) else { continue }

            if let clientMessageID = clientMessageIDFromMetadata(dto.metadata),
               let pending = existingByID[clientMessageID] {
                acknowledgedPendingIDs.insert(clientMessageID)
                remote = mergeRemoteMessage(local: pending, remote: remote)
            } else if let local = existingByID[remote.id] {
                remote = mergeRemoteMessage(local: local, remote: remote)
            }

            merged.append(remote)
        }

        for local in existingMessages {
            if acknowledgedPendingIDs.contains(local.id) {
                continue
            }
            if merged.contains(where: { $0.id == local.id }) {
                continue
            }
            if local.transfer.phase != .sent {
                merged.append(local)
            }
        }

        return merged.sorted { $0.sentAt < $1.sentAt }
    }

    private func mergeRemoteMessage(local: MessageRecord, remote: MessageRecord) -> MessageRecord {
        var merged = remote
        if let localAttachment = local.attachment {
            merged.attachment = mergeRemoteAttachment(local: localAttachment, remote: remote.attachment)
        }
        merged.transfer = .sent
        return merged
    }

    private func mergeRemoteAttachment(local: MessageAttachment, remote: MessageAttachment?) -> MessageAttachment {
        guard var merged = remote else {
            return local
        }

        if merged.name == "Attachment", !local.name.isEmpty {
            merged.name = local.name
        }

        if (merged.localPath == nil || !isLocalFileAvailable(merged.localPath)),
           isLocalFileAvailable(local.localPath) {
            merged.localPath = local.localPath
        }

        if (merged.previewPath == nil || !isLocalFileAvailable(merged.previewPath)),
           isLocalFileAvailable(local.previewPath) {
            merged.previewPath = local.previewPath
        }

        if merged.fileSizeBytes <= 0, local.fileSizeBytes > 0 {
            merged.fileSizeBytes = local.fileSizeBytes
        }

        if (merged.duration == nil || (merged.duration ?? 0) <= 0), let localDuration = local.duration, localDuration > 0 {
            merged.duration = localDuration
        }

        return merged
    }

    private func preloadAttachments(for conversationID: UUID) {
        guard let conversation = conversation(id: conversationID) else { return }
        for message in conversation.messages {
            queueAttachmentHydrationIfNeeded(conversationID: conversationID, messageID: message.id)
        }
    }

    private func queueAttachmentHydrationIfNeeded(conversationID: UUID, messageID: UUID) {
        guard
            let conversation = conversation(id: conversationID),
            let message = conversation.messages.first(where: { $0.id == messageID }),
            let attachment = message.attachment
        else {
            return
        }

        guard attachment.kind == .voiceNote || attachment.kind == .circularVideo else {
            return
        }

        let needsLocalFile = !isLocalFileAvailable(attachment.localPath)
        let needsPreview = attachment.kind == .circularVideo && !isLocalFileAvailable(attachment.previewPath)
        let needsDuration = attachment.duration == nil || (attachment.duration ?? 0) <= 0
        let needsSize = attachment.fileSizeBytes <= 0

        guard needsLocalFile || needsPreview || needsDuration || needsSize else {
            return
        }

        guard attachmentHydrationTasks[attachment.id] == nil else {
            return
        }

        let attachmentID = attachment.id
        attachmentHydrationTasks[attachmentID] = Task { @MainActor [weak self] in
            guard let self else { return }
            defer { self.attachmentHydrationTasks[attachmentID] = nil }
            await self.hydrateAttachment(attachmentID: attachmentID)
        }
    }

    private func hydrateAttachment(attachmentID: UUID) async {
        do {
            let meta = try await backend.attachmentMeta(attachmentID: attachmentID.uuidString)
            let existing = findAttachmentInState(attachmentID: attachmentID)

            var resolvedLocalPath = isLocalFileAvailable(existing?.localPath) ? existing?.localPath : nil
            var resolvedPreviewPath = isLocalFileAvailable(existing?.previewPath) ? existing?.previewPath : nil
            var resolvedDuration = existing?.duration
            var resolvedSize = max(existing?.fileSizeBytes ?? 0, meta.sizeBytes)

            if let durationSeconds = meta.durationSeconds, durationSeconds > 0 {
                resolvedDuration = max(resolvedDuration ?? 0, TimeInterval(durationSeconds))
            }

            if resolvedLocalPath == nil {
                let payload = try await backend.attachmentDownload(attachmentID: attachmentID.uuidString)
                let fileName = payload.suggestedFileName ?? meta.fileName
                let savedURL = try QGMediaTools.writeDataIntoAppSupport(
                    payload.data,
                    folder: "attachments",
                    fileName: fileName,
                    mimeType: payload.mimeType ?? meta.mimeType
                )
                resolvedLocalPath = savedURL.path
                resolvedSize = max(resolvedSize, QGMediaTools.fileSize(for: savedURL))

                if (resolvedDuration == nil || (resolvedDuration ?? 0) <= 0) {
                    resolvedDuration = await QGMediaTools.mediaDuration(for: savedURL)
                }

                if meta.kind == "circular_video", resolvedPreviewPath == nil {
                    resolvedPreviewPath = try? await QGMediaTools.makeThumbnail(for: savedURL).path
                }
            } else if
                (resolvedDuration == nil || (resolvedDuration ?? 0) <= 0),
                let localPath = resolvedLocalPath
            {
                resolvedDuration = await QGMediaTools.mediaDuration(for: URL(fileURLWithPath: localPath))
            }

            if meta.kind == "circular_video",
               resolvedPreviewPath == nil,
               let localPath = resolvedLocalPath,
               isLocalFileAvailable(localPath) {
                resolvedPreviewPath = try? await QGMediaTools.makeThumbnail(for: URL(fileURLWithPath: localPath)).path
            }

            applyHydratedAttachment(
                attachmentID: attachmentID,
                fileName: meta.fileName,
                localPath: resolvedLocalPath,
                previewPath: resolvedPreviewPath,
                fileSizeBytes: resolvedSize,
                duration: resolvedDuration
            )
        } catch {
            // Keep placeholder attachment state; hydrate again on the next sync/open.
        }
    }

    private func findAttachmentInState(attachmentID: UUID) -> MessageAttachment? {
        for conversation in state.conversations {
            if let attachment = conversation.messages.compactMap(\.attachment).first(where: { $0.id == attachmentID }) {
                return attachment
            }
        }
        return nil
    }

    private func applyHydratedAttachment(
        attachmentID: UUID,
        fileName: String,
        localPath: String?,
        previewPath: String?,
        fileSizeBytes: Int64,
        duration: TimeInterval?
    ) {
        mutate { state in
            for conversationIndex in state.conversations.indices {
                for messageIndex in state.conversations[conversationIndex].messages.indices {
                    guard var attachment = state.conversations[conversationIndex].messages[messageIndex].attachment,
                          attachment.id == attachmentID else {
                        continue
                    }

                    if attachment.name == "Attachment" || attachment.name.isEmpty {
                        attachment.name = fileName
                    }
                    if let localPath, !localPath.isEmpty {
                        attachment.localPath = localPath
                    }
                    if let previewPath, !previewPath.isEmpty {
                        attachment.previewPath = previewPath
                    }
                    if fileSizeBytes > 0 {
                        attachment.fileSizeBytes = fileSizeBytes
                    }
                    if let duration, duration > 0 {
                        attachment.duration = duration
                    }
                    state.conversations[conversationIndex].messages[messageIndex].attachment = attachment
                }
            }
        }
    }

    private func isLocalFileAvailable(_ path: String?) -> Bool {
        guard let path, !path.isEmpty else { return false }
        return FileManager.default.fileExists(atPath: path)
    }

    private func friendlySystemText(ciphertext: String) -> String? {
        switch ciphertext {
        case "WELCOME_QGRAMM":
            return "Добро пожаловать в QGramm"
        case "WELCOME_BY_INVITER":
            return "Вы приглашены в сеть QGramm"
        default:
            return nil
        }
    }

    private func makeMessageMetadata(base: [String: Any], clientMessageID: UUID) -> [String: Any] {
        var metadata = base
        metadata[Self.clientMessageIDMetadataField] = clientMessageID.uuidString
        return metadata
    }

    private func sharedKeyFromMetadata(_ metadata: [String: QGJSONValue]?) -> String? {
        guard let raw = metadata?[Self.legacyE2ESharedKeyMetadataField]?.value.base as? String else {
            return nil
        }
        let key = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !key.isEmpty, Data(base64Encoded: key) != nil else {
            return nil
        }
        return key
    }

    private func clientMessageIDFromMetadata(_ metadata: [String: QGJSONValue]?) -> UUID? {
        guard let raw = metadata?[Self.clientMessageIDMetadataField]?.value.base as? String else {
            return nil
        }
        return UUID(uuidString: raw)
    }

    private func metadataString(_ metadata: [String: QGJSONValue]?, key: String) -> String? {
        guard let raw = metadata?[key]?.value.base else { return nil }
        if let value = raw as? String {
            let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? nil : trimmed
        }
        if let value = raw as? CustomStringConvertible {
            let text = value.description.trimmingCharacters(in: .whitespacesAndNewlines)
            return text.isEmpty ? nil : text
        }
        return nil
    }

    private func metadataInt64(_ metadata: [String: QGJSONValue]?, keys: [String]) -> Int64? {
        for key in keys {
            guard let raw = metadata?[key]?.value.base else { continue }
            if let value = toInt64(raw), value > 0 {
                return value
            }
        }
        return nil
    }

    private func metadataTimeInterval(_ metadata: [String: QGJSONValue]?, keys: [String]) -> TimeInterval? {
        for key in keys {
            guard let raw = metadata?[key]?.value.base else { continue }
            if let seconds = toTimeInterval(raw), seconds > 0 {
                return seconds
            }
        }
        return nil
    }

    private func toInt64(_ raw: Any) -> Int64? {
        switch raw {
        case let value as Int:
            return Int64(value)
        case let value as Int8:
            return Int64(value)
        case let value as Int16:
            return Int64(value)
        case let value as Int32:
            return Int64(value)
        case let value as Int64:
            return value
        case let value as UInt:
            return Int64(value)
        case let value as UInt8:
            return Int64(value)
        case let value as UInt16:
            return Int64(value)
        case let value as UInt32:
            return Int64(value)
        case let value as UInt64:
            guard value <= UInt64(Int64.max) else { return nil }
            return Int64(value)
        case let value as Double:
            return Int64(value.rounded())
        case let value as Float:
            return Int64(value.rounded())
        case let value as String:
            let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
            if let intValue = Int64(trimmed) {
                return intValue
            }
            if let doubleValue = Double(trimmed) {
                return Int64(doubleValue.rounded())
            }
            return nil
        default:
            return nil
        }
    }

    private func toTimeInterval(_ raw: Any) -> TimeInterval? {
        switch raw {
        case let value as Double:
            return value
        case let value as Float:
            return TimeInterval(value)
        default:
            if let intValue = toInt64(raw) {
                return TimeInterval(intValue)
            }
            return nil
        }
    }

    private func derivedSharedKey(conversationID: UUID, participantIDs: [UUID]) -> String {
        let memberIDs = participantIDs
            .map { $0.uuidString.lowercased() }
            .sorted()
            .joined(separator: ",")
        let material = "qgramm:e2e:v1:\(conversationID.uuidString.lowercased()):\(memberIDs)"
        let digest = SHA256.hash(data: Data(material.utf8))
        return Data(digest).base64EncodedString()
    }

    private func ensureE2EIdentityPublishedIfNeeded(remoteUser: QGAuthUserDTO) async -> QGAuthUserDTO {
        let identity = ensureLocalE2EIdentity()
        let localPublicKey = identity.publicKey
        let remotePublicKey = sanitizedE2EPublicKey(remoteUser.e2ePublicKey) ?? ""

        if remotePublicKey == localPublicKey {
            return remoteUser
        }

        // Do not rotate a published key silently: this would break decryptability on other devices.
        if !remotePublicKey.isEmpty {
            return remoteUser
        }

        do {
            let updated = try await backend.updateMe(
                firstName: remoteUser.firstName,
                lastName: remoteUser.lastName,
                nickname: remoteUser.nickname,
                avatarPath: remoteUser.avatarURL,
                metadata: ["e2e_public_key": localPublicKey]
            )
            return updated
        } catch {
            return remoteUser
        }
    }

    private func ensureLocalE2EIdentity() -> (privateKey: String, publicKey: String) {
        let defaults = UserDefaults.standard
        let storedPrivate = defaults.string(forKey: Self.e2ePrivateKeyStorageKey)?.trimmingCharacters(in: .whitespacesAndNewlines)
        let storedPublic = defaults.string(forKey: Self.e2ePublicKeyStorageKey)?.trimmingCharacters(in: .whitespacesAndNewlines)

        if
            let storedPrivate, !storedPrivate.isEmpty,
            let storedPublic, !storedPublic.isEmpty
        {
            return (storedPrivate, storedPublic)
        }

        let pair = crypto.makeIdentityKeyPair()
        defaults.set(pair.privateKey, forKey: Self.e2ePrivateKeyStorageKey)
        defaults.set(pair.publicKey, forKey: Self.e2ePublicKeyStorageKey)
        return pair
    }

    private func resolvedConversationSharedKey(conversationID: UUID, participantIDs: [UUID], existingKey: String?) -> String {
        if let directKey = directConversationSharedKey(conversationID: conversationID, participantIDs: participantIDs) {
            return directKey
        }

        if let existingKey, !existingKey.isEmpty {
            return existingKey
        }

        return derivedSharedKey(conversationID: conversationID, participantIDs: participantIDs)
    }

    private func directConversationSharedKey(conversationID: UUID, participantIDs: [UUID]) -> String? {
        guard participantIDs.count == 2 else { return nil }
        guard let currentUserID = state.session.currentUserID else { return nil }
        guard let peerID = participantIDs.first(where: { $0 != currentUserID }) else { return nil }
        guard let peerPublicKey = sanitizedE2EPublicKey(user(id: peerID)?.e2ePublicKey) else { return nil }

        let identity = ensureLocalE2EIdentity()
        return crypto.deriveDirectConversationKey(
            privateKeyBase64: identity.privateKey,
            peerPublicKeyBase64: peerPublicKey,
            conversationID: conversationID
        )
    }

    private func sanitizedE2EPublicKey(_ raw: String?) -> String? {
        guard let raw else { return nil }
        let key = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !key.isEmpty, Data(base64Encoded: key) != nil else { return nil }
        return key
    }

    private func mergeInvites(existing: [InviteRecord], remote: [InviteRecord]) -> [InviteRecord] {
        guard !remote.isEmpty else {
            return existing
        }

        var byID: [UUID: InviteRecord] = [:]
        for invite in existing {
            byID[invite.id] = invite
        }
        for invite in remote {
            byID[invite.id] = invite
        }
        return byID.values.sorted { $0.createdAt > $1.createdAt }
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
                    self.ensureRealtimeConnected()
                } else if !online {
                    self.stopRealtime()
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
        let interval = isChatRoomOpen ? 2.5 : 4.0
        syncTimer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
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

    private func ensureRealtimeConnected() {
        guard isOnline, state.session.isAuthenticated, !state.session.accessToken.isEmpty else {
            stopRealtime()
            return
        }
        guard realtimeTask == nil else { return }
        startRealtime()
    }

    private func startRealtime() {
        guard realtimeTask == nil else { return }
        do {
            let request = try backend.webSocketRequest()
            let task = URLSession.shared.webSocketTask(with: request)
            realtimeTask = task
            task.resume()
            listenRealtime(on: task)
        } catch {
            scheduleRealtimeReconnect()
        }
    }

    private func stopRealtime() {
        realtimeTask?.cancel(with: .goingAway, reason: nil)
        realtimeTask = nil
        realtimeReconnectTask?.cancel()
        realtimeReconnectTask = nil
    }

    private func scheduleRealtimeReconnect() {
        guard isOnline, state.session.isAuthenticated, realtimeReconnectTask == nil else { return }
        realtimeReconnectTask = Task { @MainActor in
            defer { realtimeReconnectTask = nil }
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            guard isOnline, state.session.isAuthenticated else { return }
            if realtimeTask == nil {
                startRealtime()
            }
        }
    }

    private func listenRealtime(on task: URLSessionWebSocketTask) {
        task.receive { [weak self] result in
            Task { @MainActor in
                guard let self, self.realtimeTask === task else { return }
                switch result {
                case let .success(message):
                    switch message {
                    case let .string(text):
                        self.handleRealtimeEnvelopeData(Data(text.utf8))
                    case let .data(data):
                        self.handleRealtimeEnvelopeData(data)
                    @unknown default:
                        break
                    }
                    self.listenRealtime(on: task)
                case .failure:
                    self.realtimeTask = nil
                    self.scheduleRealtimeReconnect()
                }
            }
        }
    }

    private func handleRealtimeEnvelopeData(_ data: Data) {
        guard let envelope = try? backend.decodeRealtimeEnvelope(data) else { return }
        switch envelope.type {
        case "message.created":
            if let message = envelope.payload.message {
                applyRealtimeMessage(message)
            }
        case "reaction.updated":
            applyRealtimeReaction(envelope.payload)
        default:
            break
        }
    }

    private func applyRealtimeMessage(_ dto: QGMessageDTO) {
        guard let conversationID = UUID(uuidString: dto.conversationID) else { return }

        guard let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }) else {
            Task { @MainActor in
                try? await refreshFromServer()
            }
            return
        }

        let currentUserID = state.session.currentUserID
        let isActiveConversation = activeConversationID == conversationID
        let conversationSharedKey = state.conversations[conversationIndex].sharedKey
        guard let mappedMessage = mapMessage(dto, sharedKey: conversationSharedKey) else { return }
        let ackClientMessageID = clientMessageIDFromMetadata(dto.metadata)

        mutate { state in
            guard let index = state.conversations.firstIndex(where: { $0.id == conversationID }) else { return }

            if let ackClientMessageID,
               mappedMessage.senderID == currentUserID,
               let pendingIndex = state.conversations[index].messages.firstIndex(where: { $0.id == ackClientMessageID }) {
                state.conversations[index].messages[pendingIndex].id = mappedMessage.id
                state.conversations[index].messages[pendingIndex].sentAt = mappedMessage.sentAt
                state.conversations[index].messages[pendingIndex].transfer = .sent
                if state.conversations[index].messages[pendingIndex].attachment?.id == nil,
                   let remoteAttachmentID = mappedMessage.attachment?.id {
                    state.conversations[index].messages[pendingIndex].attachment?.id = remoteAttachmentID
                }
            } else if let existingIndex = state.conversations[index].messages.firstIndex(where: { $0.id == mappedMessage.id }) {
                state.conversations[index].messages[existingIndex] = mappedMessage
            } else {
                state.conversations[index].messages.append(mappedMessage)
            }

            state.conversations[index].messages.sort { $0.sentAt < $1.sentAt }
            state.conversations[index].lastActivityAt = max(state.conversations[index].lastActivityAt, mappedMessage.sentAt)

            if mappedMessage.senderID != currentUserID {
                if isActiveConversation {
                    state.conversations[index].unreadCount = 0
                } else {
                    state.conversations[index].unreadCount += 1
                }
            }

            state.conversations.sort { $0.lastActivityAt > $1.lastActivityAt }
        }

        queueAttachmentHydrationIfNeeded(conversationID: conversationID, messageID: mappedMessage.id)

        if mappedMessage.senderID != currentUserID, isActiveConversation {
            Task { @MainActor in
                await markConversationReadOnServer(conversationID, lastReadMessageID: mappedMessage.id)
            }
        }
    }

    private func applyRealtimeReaction(_ payload: QGRealtimePayloadDTO) {
        guard
            let conversationRaw = payload.conversationID,
            let conversationID = UUID(uuidString: conversationRaw),
            let messageRaw = payload.messageID,
            let messageID = UUID(uuidString: messageRaw),
            let emoji = payload.emoji,
            let userRaw = payload.userID,
            let userID = UUID(uuidString: userRaw),
            let action = payload.action
        else {
            return
        }

        guard let conversationIndex = state.conversations.firstIndex(where: { $0.id == conversationID }),
              let messageIndex = state.conversations[conversationIndex].messages.firstIndex(where: { $0.id == messageID }) else {
            return
        }

        mutate { state in
            var reactions = state.conversations[conversationIndex].messages[messageIndex].reactions
            if let reactionIndex = reactions.firstIndex(where: { $0.emoji == emoji }) {
                if action == "add" {
                    if !reactions[reactionIndex].userIDs.contains(userID) {
                        reactions[reactionIndex].userIDs.append(userID)
                    }
                } else {
                    reactions[reactionIndex].userIDs.removeAll(where: { $0 == userID })
                    if reactions[reactionIndex].userIDs.isEmpty {
                        reactions.removeAll(where: { $0.emoji == emoji })
                    }
                }
            } else if action == "add" {
                reactions.append(MessageReaction(emoji: emoji, userIDs: [userID]))
            }
            state.conversations[conversationIndex].messages[messageIndex].reactions = reactions
        }
    }

    private func markConversationReadLocally(_ conversationID: UUID) -> UUID? {
        guard let index = state.conversations.firstIndex(where: { $0.id == conversationID }) else {
            return nil
        }
        let lastMessageID = state.conversations[index].messages.sorted(by: { $0.sentAt < $1.sentAt }).last?.id
        mutate {
            $0.conversations[index].unreadCount = 0
        }
        return lastMessageID
    }

    private func markConversationReadOnServer(_ conversationID: UUID, lastReadMessageID: UUID? = nil) async {
        do {
            try await backend.markConversationRead(
                conversationID: conversationID.uuidString,
                lastReadMessageID: lastReadMessageID?.uuidString
            )
        } catch {
            // Keep local read state even when network is unstable.
        }
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
            encryptionKeyHint: sharedKey,
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
                e2ePublicKey: nil,
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
                e2ePublicKey: nil,
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
                e2ePublicKey: nil,
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
                e2ePublicKey: nil,
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
                e2ePublicKey: nil,
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
                encryptionKeyHint: key,
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
