import Foundation

enum RootTab: String, Codable, CaseIterable, Identifiable {
    case chats
    case network
    case calls
    case settings

    var id: String { rawValue }

    var title: String {
        switch self {
        case .chats: "Чаты"
        case .network: "Сеть"
        case .calls: "Звонки"
        case .settings: "Настройки"
        }
    }

    func title(language: AppLanguage) -> String {
        switch self {
        case .chats: return language.text(ru: "Чаты", en: "Chats")
        case .network: return language.text(ru: "Сеть", en: "Network")
        case .calls: return language.text(ru: "Звонки", en: "Calls")
        case .settings: return language.text(ru: "Настройки", en: "Settings")
        }
    }
}

enum ConversationGroup: String, Codable, CaseIterable, Identifiable {
    case chats
    case groups
    case channels
    case threads

    var id: String { rawValue }

    var title: String {
        switch self {
        case .chats: "Чаты"
        case .groups: "Группы"
        case .channels: "Каналы"
        case .threads: "Треды"
        }
    }

    func title(language: AppLanguage) -> String {
        switch self {
        case .chats: return language.text(ru: "Чаты", en: "Chats")
        case .groups: return language.text(ru: "Группы", en: "Groups")
        case .channels: return language.text(ru: "Каналы", en: "Channels")
        case .threads: return language.text(ru: "Треды", en: "Threads")
        }
    }
}

enum TrustLevel: Int, Codable, CaseIterable, Identifiable {
    case one = 1
    case two
    case three
    case four
    case five
    case six
    case seven
    case eight

    var id: Int { rawValue }

    var weeklyInviteLimit: Int {
        switch self {
        case .one: 0
        case .two, .three, .four: 3
        case .five, .six, .seven: 10
        case .eight: Int.max
        }
    }

    var title: String { "Уровень \(rawValue)" }

    func title(language: AppLanguage) -> String {
        language.text(ru: "Уровень \(rawValue)", en: "Level \(rawValue)")
    }
}

enum SafetyMode: String, Codable, CaseIterable, Identifiable {
    case classic
    case local

    var id: String { rawValue }

    var title: String {
        switch self {
        case .classic: "Classic"
        case .local: "Local"
        }
    }
}

enum AuthPurpose: String, Codable {
    case register
    case login
}

enum AppLanguage: String, Codable, CaseIterable, Identifiable {
    case russian
    case english

    var id: String { rawValue }

    var title: String {
        switch self {
        case .russian: "Русский"
        case .english: "English"
        }
    }

    var localeIdentifier: String {
        switch self {
        case .russian: return "ru_RU"
        case .english: return "en_US"
        }
    }

    func text(ru: String, en: String) -> String {
        self == .english ? en : ru
    }
}

enum AppThemeMode: String, Codable, CaseIterable, Identifiable {
    case system
    case light
    case dark

    var id: String { rawValue }

    func title(language: AppLanguage) -> String {
        switch self {
        case .system:
            return language.text(ru: "Системная", en: "System")
        case .light:
            return language.text(ru: "Светлая", en: "Light")
        case .dark:
            return language.text(ru: "Тёмная", en: "Dark")
        }
    }
}

enum PresenceState: String, Codable {
    case online
    case typing
    case offline

    func title(language: AppLanguage) -> String {
        switch self {
        case .online:
            return language.text(ru: "Онлайн", en: "Online")
        case .typing:
            return language.text(ru: "Печатает...", en: "Typing...")
        case .offline:
            return language.text(ru: "Офлайн", en: "Offline")
        }
    }
}

enum AttachmentKind: String, Codable {
    case file
    case media
    case voiceNote
    case circularVideo
}

enum TransferPhase: String, Codable {
    case sending
    case sent
    case failed
}

enum CallDirection: String, Codable {
    case incoming
    case outgoing
}

enum CallKind: String, Codable {
    case audio
    case video
    case group
}

enum ReportReason: String, Codable, CaseIterable, Identifiable {
    case spam
    case insult
    case virus
    case scam
    case other

    var id: String { rawValue }

    var title: String {
        switch self {
        case .spam: "Спам"
        case .insult: "Оскорбление"
        case .virus: "Вирус"
        case .scam: "Обман"
        case .other: "Другое"
        }
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        let raw = try container.decode(String.self)
        switch raw {
        case "spam":
            self = .spam
        case "abuse":
            self = .insult
        case "malware":
            self = .virus
        case "impersonation":
            self = .scam
        case "insult":
            self = .insult
        case "virus":
            self = .virus
        case "scam":
            self = .scam
        case "other":
            self = .other
        default:
            self = .other
        }
    }
}

struct SecureTextEnvelope: Codable, Hashable {
    var nonce: String
    var ciphertext: String
    var tag: String
}

struct MessageReaction: Codable, Hashable, Identifiable {
    var emoji: String
    var userIDs: [UUID]

    var id: String { emoji }
}

struct MessageTransferState: Codable, Hashable {
    var phase: TransferPhase
    var progress: Double

    static let sent = MessageTransferState(phase: .sent, progress: 1)
}

struct MessageAttachment: Codable, Hashable, Identifiable {
    var id: UUID
    var kind: AttachmentKind
    var name: String
    var localPath: String?
    var previewPath: String?
    var fileSizeBytes: Int64
    var duration: TimeInterval?
}

struct MessageRecord: Codable, Hashable, Identifiable {
    var id: UUID
    var senderID: UUID
    var sentAt: Date
    var body: SecureTextEnvelope?
    var quotedBody: SecureTextEnvelope?
    var replyToMessageID: UUID?
    var forwardedFromTitle: String?
    var attachment: MessageAttachment?
    var reactions: [MessageReaction]
    var transfer: MessageTransferState
    var reportCount: Int
}

struct UserProfile: Codable, Hashable, Identifiable {
    var id: UUID
    var firstName: String
    var lastName: String
    var nickname: String
    var emailOrPhone: String
    var bio: String
    var trustLevel: TrustLevel
    var presence: PresenceState
    var joinedAt: Date
    var avatarLocalPath: String?
    var avatarAssetName: String?
    var isAdmin: Bool
    var isBanned: Bool
    var invitedByUserID: UUID?

    var displayName: String {
        [firstName, lastName]
            .filter { !$0.isEmpty }
            .joined(separator: " ")
    }
}

struct ConversationRecord: Codable, Hashable, Identifiable {
    var id: UUID
    var category: ConversationGroup
    var title: String
    var participantIDs: [UUID]
    var sharedKey: String
    var lastActivityAt: Date
    var unreadCount: Int
    var onlineUserID: UUID?
    var messages: [MessageRecord]
}

struct InviteRecord: Codable, Hashable, Identifiable {
    var id: UUID
    var code: String
    var linkToken: String
    var createdAt: Date
    var expiresAt: Date
    var inviterUserID: UUID
    var boundDeviceID: String?
    var redeemedByUserID: UUID?

    var isActive: Bool {
        redeemedByUserID == nil && expiresAt > Date()
    }

    var deepLink: String { "qgramm://invite/\(code)" }
}

struct CallRecord: Codable, Hashable, Identifiable {
    var id: UUID
    var peerUserID: UUID
    var title: String
    var startedAt: Date
    var duration: TimeInterval
    var direction: CallDirection
    var kind: CallKind
}

struct MessageReportRecord: Codable, Hashable, Identifiable {
    var id: UUID
    var messageID: UUID
    var conversationID: UUID
    var reporterUserID: UUID
    var reason: ReportReason
    var note: String
    var createdAt: Date
}

struct SessionState: Codable, Hashable {
    var deviceBindingID: String
    var acceptedInviteCode: String?
    var pendingContact: String
    var expectedVerificationCode: String
    var currentUserID: UUID?
    var accessToken: String
    var accessTokenExpiresAt: Date?
    var authPurpose: AuthPurpose
    var recoveryKey: String
    var shouldRevealRecoveryKey: Bool
    var language: AppLanguage
    var safetyMode: SafetyMode
    var appPinCode: String
    var isFaceIDEnabled: Bool
    var isPowerSavingEnabled: Bool

    var hasBoundInvite: Bool { acceptedInviteCode != nil }
    var isAuthenticated: Bool { currentUserID != nil }

    init(
        deviceBindingID: String,
        acceptedInviteCode: String?,
        pendingContact: String,
        expectedVerificationCode: String,
        currentUserID: UUID?,
        accessToken: String,
        accessTokenExpiresAt: Date?,
        authPurpose: AuthPurpose,
        recoveryKey: String,
        shouldRevealRecoveryKey: Bool,
        language: AppLanguage,
        safetyMode: SafetyMode,
        appPinCode: String,
        isFaceIDEnabled: Bool,
        isPowerSavingEnabled: Bool
    ) {
        self.deviceBindingID = deviceBindingID
        self.acceptedInviteCode = acceptedInviteCode
        self.pendingContact = pendingContact
        self.expectedVerificationCode = expectedVerificationCode
        self.currentUserID = currentUserID
        self.accessToken = accessToken
        self.accessTokenExpiresAt = accessTokenExpiresAt
        self.authPurpose = authPurpose
        self.recoveryKey = recoveryKey
        self.shouldRevealRecoveryKey = shouldRevealRecoveryKey
        self.language = language
        self.safetyMode = safetyMode
        self.appPinCode = appPinCode
        self.isFaceIDEnabled = isFaceIDEnabled
        self.isPowerSavingEnabled = isPowerSavingEnabled
    }

    private enum CodingKeys: String, CodingKey {
        case deviceBindingID
        case acceptedInviteCode
        case pendingContact
        case expectedVerificationCode
        case currentUserID
        case accessToken
        case accessTokenExpiresAt
        case authPurpose
        case recoveryKey
        case shouldRevealRecoveryKey
        case language
        case safetyMode
        case appPinCode
        case isFaceIDEnabled
        case isPowerSavingEnabled
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        self.deviceBindingID = try container.decode(String.self, forKey: .deviceBindingID)
        self.acceptedInviteCode = try container.decodeIfPresent(String.self, forKey: .acceptedInviteCode)
        self.pendingContact = try container.decode(String.self, forKey: .pendingContact)
        self.expectedVerificationCode = try container.decode(String.self, forKey: .expectedVerificationCode)
        self.currentUserID = try container.decodeIfPresent(UUID.self, forKey: .currentUserID)
        self.accessToken = try container.decodeIfPresent(String.self, forKey: .accessToken) ?? ""
        self.accessTokenExpiresAt = try container.decodeIfPresent(Date.self, forKey: .accessTokenExpiresAt)
        self.authPurpose = try container.decodeIfPresent(AuthPurpose.self, forKey: .authPurpose) ?? .register
        self.recoveryKey = try container.decode(String.self, forKey: .recoveryKey)
        self.shouldRevealRecoveryKey = try container.decode(Bool.self, forKey: .shouldRevealRecoveryKey)
        self.language = try container.decodeIfPresent(AppLanguage.self, forKey: .language) ?? .russian
        self.safetyMode = try container.decodeIfPresent(SafetyMode.self, forKey: .safetyMode) ?? .classic
        self.appPinCode = try container.decodeIfPresent(String.self, forKey: .appPinCode) ?? ""
        self.isFaceIDEnabled = try container.decodeIfPresent(Bool.self, forKey: .isFaceIDEnabled) ?? false
        self.isPowerSavingEnabled = try container.decodeIfPresent(Bool.self, forKey: .isPowerSavingEnabled) ?? false
    }
}

struct AppState: Codable, Hashable {
    var session: SessionState
    var selectedTab: RootTab
    var selectedConversationGroup: ConversationGroup
    var users: [UserProfile]
    var conversations: [ConversationRecord]
    var invites: [InviteRecord]
    var calls: [CallRecord]
    var reports: [MessageReportRecord]
    var cacheSizeBytes: Int64
}

struct ActiveCallSession: Identifiable, Hashable {
    var id: UUID
    var userID: UUID
    var title: String
    var kind: CallKind
    var startedAt: Date
    var isMuted: Bool
    var isSpeakerEnabled: Bool
}

struct DraftQuote: Identifiable, Hashable {
    var id: UUID
    var sourceMessageID: UUID
    var originalText: String
    var editableExcerpt: String
}
