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
}

enum PresenceState: String, Codable {
    case online
    case typing
    case offline
}

enum AttachmentKind: String, Codable {
    case file
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
    case abuse
    case malware
    case impersonation

    var id: String { rawValue }

    var title: String {
        switch self {
        case .spam: "Спам"
        case .abuse: "Оскорбление"
        case .malware: "Вредоносный файл"
        case .impersonation: "Выдача за другого"
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
    var recoveryKey: String
    var shouldRevealRecoveryKey: Bool
    var language: AppLanguage
    var safetyMode: SafetyMode

    var hasBoundInvite: Bool { acceptedInviteCode != nil }
    var isAuthenticated: Bool { currentUserID != nil }
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
