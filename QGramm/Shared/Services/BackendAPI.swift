import Foundation

enum QGBackendError: LocalizedError {
    case invalidURL
    case invalidResponse
    case unauthorized
    case server(String)
    case transport(String)

    var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "Backend URL is invalid."
        case .invalidResponse:
            return "Backend returned an invalid response."
        case .unauthorized:
            return "Session is unauthorized."
        case let .server(message):
            return message
        case let .transport(message):
            return message
        }
    }
}

enum QGHTTPMethod: String {
    case get = "GET"
    case post = "POST"
    case put = "PUT"
    case patch = "PATCH"
    case delete = "DELETE"
}

struct QGJSONValue: Codable, Hashable {
    let value: AnyHashable

    init(_ value: AnyHashable) {
        self.value = value
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let string = try? container.decode(String.self) {
            value = AnyHashable(string)
            return
        }
        if let int = try? container.decode(Int.self) {
            value = AnyHashable(int)
            return
        }
        if let double = try? container.decode(Double.self) {
            value = AnyHashable(double)
            return
        }
        if let bool = try? container.decode(Bool.self) {
            value = AnyHashable(bool)
            return
        }
        if container.decodeNil() {
            value = AnyHashable("null")
            return
        }
        value = AnyHashable("")
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch value {
        case let v as String:
            try container.encode(v)
        case let v as Int:
            try container.encode(v)
        case let v as Double:
            try container.encode(v)
        case let v as Bool:
            try container.encode(v)
        default:
            try container.encode(String(describing: value))
        }
    }
}

private struct QGBackendErrorEnvelope: Decodable {
    let error: String
}

struct QGDeviceActivationDTO: Decodable {
    let allowed: Bool
    let inviteCode: String?
    let activatedAt: Date?
    let expiresAt: Date?

    enum CodingKeys: String, CodingKey {
        case allowed
        case inviteCode = "invite_code"
        case activatedAt = "activated_at"
        case expiresAt = "expires_at"
    }
}

struct QGSendCodeDTO: Decodable {
    let expiresAt: Date
    let delivery: String
    let debugCode: String?

    enum CodingKeys: String, CodingKey {
        case expiresAt = "expires_at"
        case delivery
        case debugCode = "debug_code"
    }
}

struct QGAuthUserDTO: Decodable {
    let id: String
    let uid: String
    let email: String
    let firstName: String
    let lastName: String
    let nickname: String
    let e2ePublicKey: String?
    let trustLevel: Int
    let isRoot: Bool
    let isBanned: Bool
    let invitedByUserID: String?
    let createdAt: Date
    let lastSeenAt: Date?
    let avatarURL: String?

    enum CodingKeys: String, CodingKey {
        case id
        case uid
        case email
        case firstName = "first_name"
        case lastName = "last_name"
        case nickname
        case e2ePublicKey = "e2e_public_key"
        case trustLevel = "trust_level"
        case isRoot = "is_root"
        case isBanned = "is_banned"
        case invitedByUserID = "invited_by_user_id"
        case createdAt = "created_at"
        case lastSeenAt = "last_seen_at"
        case avatarURL = "avatar_url"
    }
}

struct QGAuthResultDTO: Decodable {
    let accessToken: String
    let expiresAt: Date
    let user: QGAuthUserDTO

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case expiresAt = "expires_at"
        case user
    }
}

struct QGInviteDTO: Decodable {
    let id: String
    let code: String
    let inviterUserID: String
    let createdAt: Date
    let expiresAt: Date
    let maxUses: Int
    let usedCount: Int
    let isRevoked: Bool
    let isActive: Bool

    enum CodingKeys: String, CodingKey {
        case id
        case code
        case inviterUserID = "inviter_user_id"
        case createdAt = "created_at"
        case expiresAt = "expires_at"
        case maxUses = "max_uses"
        case usedCount = "used_count"
        case isRevoked = "is_revoked"
        case isActive = "is_active"
    }
}

struct QGInviteGraphNodeDTO: Decodable {
    let userID: String
    let uid: String
    let nickname: String
    let e2ePublicKey: String?
    let invitedByUserID: String?
    let createdAt: Date
    let isBanned: Bool
    let trustLevel: Int

    enum CodingKeys: String, CodingKey {
        case userID = "user_id"
        case uid
        case nickname
        case e2ePublicKey = "e2e_public_key"
        case invitedByUserID = "invited_by_user_id"
        case createdAt = "created_at"
        case isBanned = "is_banned"
        case trustLevel = "trust_level"
    }
}

struct QGConversationDTO: Decodable {
    let id: String
    let kind: String
    let title: String
    let isQGramm: Bool
    let updatedAt: Date
    let participantIDs: [String]
    let unreadCount: Int

    enum CodingKeys: String, CodingKey {
        case id
        case kind
        case title
        case isQGramm = "is_qgramm"
        case updatedAt = "updated_at"
        case participantIDs = "participant_ids"
        case unreadCount = "unread_count"
    }
}

struct QGReactionDTO: Decodable {
    let emoji: String
    let userIDs: [String]

    enum CodingKeys: String, CodingKey {
        case emoji
        case userIDs = "user_ids"
    }
}

struct QGMessageDTO: Decodable {
    let id: String
    let conversationID: String
    let senderUserID: String?
    let messageType: String
    let bodyCiphertext: String?
    let bodyNonce: String?
    let bodyTag: String?
    let quotedCiphertext: String?
    let quotedNonce: String?
    let quotedTag: String?
    let replyToMessageID: String?
    let forwardedFromConversationID: String?
    let attachmentID: String?
    let createdAt: Date
    let reportCount: Int
    let reactions: [QGReactionDTO]?
    let metadata: [String: QGJSONValue]?

    enum CodingKeys: String, CodingKey {
        case id
        case conversationID = "conversation_id"
        case senderUserID = "sender_user_id"
        case messageType = "message_type"
        case bodyCiphertext = "body_ciphertext"
        case bodyNonce = "body_nonce"
        case bodyTag = "body_tag"
        case quotedCiphertext = "quoted_ciphertext"
        case quotedNonce = "quoted_nonce"
        case quotedTag = "quoted_tag"
        case replyToMessageID = "reply_to_message_id"
        case forwardedFromConversationID = "forwarded_from_conversation_id"
        case attachmentID = "attachment_id"
        case createdAt = "created_at"
        case reportCount = "report_count"
        case reactions
        case metadata
    }
}

struct QGUploadDTO: Decodable {
    let id: String
    let status: String
    let finalizedAttachmentID: String?

    enum CodingKeys: String, CodingKey {
        case id
        case status
        case finalizedAttachmentID = "finalized_attachment_id"
    }
}

struct QGAttachmentDTO: Decodable {
    let id: String
    let ownerUserID: String
    let kind: String
    let previewPath: String?
    let fileName: String
    let mimeType: String?
    let sizeBytes: Int64
    let durationSeconds: Int?
    let checksumSHA256: String?
    let createdAt: Date?

    enum CodingKeys: String, CodingKey {
        case id
        case ownerUserID = "owner_user_id"
        case kind
        case previewPath = "preview_path"
        case fileName = "file_name"
        case mimeType = "mime_type"
        case sizeBytes = "size_bytes"
        case durationSeconds = "duration_seconds"
        case checksumSHA256 = "checksum_sha256"
        case createdAt = "created_at"
    }
}

struct QGAttachmentDownloadPayload {
    let data: Data
    let suggestedFileName: String?
    let mimeType: String?
}

struct QGCallDTO: Decodable {
    let id: String
    let callerUserID: String
    let calleeUserID: String
    let kind: String
    let status: String
    let startedAt: Date
    let endedAt: Date?
    let durationSeconds: Int?

    enum CodingKeys: String, CodingKey {
        case id
        case callerUserID = "caller_user_id"
        case calleeUserID = "callee_user_id"
        case kind
        case status
        case startedAt = "started_at"
        case endedAt = "ended_at"
        case durationSeconds = "duration_seconds"
    }
}

struct QGReportDTO: Decodable {
    let id: String
}

struct QGRealtimePayloadDTO: Decodable {
    let message: QGMessageDTO?
    let conversationID: String?
    let messageID: String?
    let emoji: String?
    let userID: String?
    let action: String?

    enum CodingKeys: String, CodingKey {
        case message
        case conversationID = "conversation_id"
        case messageID = "message_id"
        case emoji
        case userID = "user_id"
        case action
    }
}

struct QGRealtimeEnvelopeDTO: Decodable {
    let type: String
    let timestamp: Date
    let payload: QGRealtimePayloadDTO
}

final class QGBackendClient {
    private static let defaultBaseURL = "http://178.140.207.217:18080"
    private static let baseURLKey = "qg.backend.baseURL"

    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder
    private(set) var baseURL: URL
    private(set) var accessToken: String = ""

    init() {
        let configured = UserDefaults.standard.string(forKey: Self.baseURLKey) ?? Self.defaultBaseURL
        baseURL = URL(string: configured) ?? URL(string: Self.defaultBaseURL)!
        session = URLSession(configuration: .default)

        decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom(QGBackendClient.decodeDate(from:))

        encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
    }

    func setBaseURL(_ raw: String) {
        guard let url = URL(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)) else { return }
        baseURL = url
        UserDefaults.standard.set(url.absoluteString, forKey: Self.baseURLKey)
    }

    func setAccessToken(_ token: String) {
        accessToken = token
    }

    func clearAccessToken() {
        accessToken = ""
    }

    func health() async throws {
        _ = try await requestRaw(path: "/healthz", method: .get)
    }

    func activateInvite(code: String, deviceFingerprint: String) async throws -> QGDeviceActivationDTO {
        try await request(
            path: "/v1/auth/invites/activate",
            method: .post,
            body: ["code": code, "device_fingerprint": deviceFingerprint]
        )
    }

    func checkDeviceActivation(deviceFingerprint: String) async throws -> QGDeviceActivationDTO {
        try await request(
            path: "/v1/auth/device/check",
            method: .post,
            body: ["device_fingerprint": deviceFingerprint]
        )
    }

    func sendCode(email: String, purpose: AuthPurpose, captchaToken: String, deviceFingerprint: String) async throws -> QGSendCodeDTO {
        try await request(
            path: "/v1/auth/send-code",
            method: .post,
            body: [
                "email": email,
                "purpose": purpose.rawValue,
                "captcha_token": captchaToken,
                "device_fingerprint": deviceFingerprint
            ]
        )
    }

    func register(email: String, code: String, deviceFingerprint: String, firstName: String, lastName: String, nickname: String, recoveryCiphertext: String, recoveryMeta: [String: String]) async throws -> QGAuthResultDTO {
        try await request(
            path: "/v1/auth/register",
            method: .post,
            bodyAny: [
                "email": email,
                "verification_code": code,
                "device_fingerprint": deviceFingerprint,
                "first_name": firstName,
                "last_name": lastName,
                "nickname": nickname,
                "recovery_ciphertext": recoveryCiphertext,
                "recovery_meta": recoveryMeta
            ]
        )
    }

    func login(email: String, code: String, deviceFingerprint: String) async throws -> QGAuthResultDTO {
        try await request(
            path: "/v1/auth/login",
            method: .post,
            body: [
                "email": email,
                "verification_code": code,
                "device_fingerprint": deviceFingerprint
            ]
        )
    }

    func logout() async throws {
        _ = try await requestRaw(path: "/v1/auth/logout", method: .post, authorized: true)
    }

    func me() async throws -> QGAuthUserDTO {
        try await request(path: "/v1/me", method: .get, authorized: true)
    }

    func updateMe(firstName: String, lastName: String, nickname: String, avatarPath: String?, metadata: [String: Any] = [:]) async throws -> QGAuthUserDTO {
        try await request(
            path: "/v1/me",
            method: .patch,
            bodyAny: [
                "first_name": firstName,
                "last_name": lastName,
                "nickname": nickname,
                "avatar_path": avatarPath ?? "",
                "metadata": metadata
            ],
            authorized: true
        )
    }

    func listInvites() async throws -> [QGInviteDTO] {
        try await request(path: "/v1/invites", method: .get, authorized: true)
    }

    func createInvite() async throws -> QGInviteDTO {
        try await request(path: "/v1/invites", method: .post, body: [String: String](), authorized: true)
    }

    func inviteGraph() async throws -> [QGInviteGraphNodeDTO] {
        try await request(path: "/v1/invites/graph", method: .get, authorized: true)
    }

    func listChats() async throws -> [QGConversationDTO] {
        try await request(path: "/v1/chats", method: .get, authorized: true)
    }

    func directByNickname(_ nickname: String) async throws -> QGConversationDTO {
        try await request(
            path: "/v1/chats/direct/by-nickname",
            method: .post,
            body: ["nickname": nickname],
            authorized: true
        )
    }

    func listMessages(conversationID: String, limit: Int = 100) async throws -> [QGMessageDTO] {
        try await request(path: "/v1/chats/\(conversationID)/messages?limit=\(limit)", method: .get, authorized: true)
    }

    func markConversationRead(conversationID: String, lastReadMessageID: String?) async throws {
        var body: [String: Any] = [:]
        if let lastReadMessageID, !lastReadMessageID.isEmpty {
            body["last_read_message_id"] = lastReadMessageID
        }
        _ = try await requestRaw(
            path: "/v1/chats/\(conversationID)/read",
            method: .post,
            bodyAny: body,
            authorized: true
        )
    }

    func sendMessage(conversationID: String, payload: [String: Any]) async throws -> QGMessageDTO {
        try await request(path: "/v1/chats/\(conversationID)/messages", method: .post, bodyAny: payload, authorized: true)
    }

    func addReaction(conversationID: String, messageID: String, emoji: String) async throws {
        _ = try await requestRaw(
            path: "/v1/chats/\(conversationID)/messages/\(messageID)/reactions",
            method: .post,
            bodyAny: ["emoji": emoji],
            authorized: true
        )
    }

    func removeReaction(conversationID: String, messageID: String, emoji: String) async throws {
        let encodedEmoji = emoji.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? emoji
        _ = try await requestRaw(
            path: "/v1/chats/\(conversationID)/messages/\(messageID)/reactions/\(encodedEmoji)",
            method: .delete,
            authorized: true
        )
    }

    func reportMessage(conversationID: String, messageID: String, reason: String, note: String) async throws -> QGReportDTO {
        try await request(
            path: "/v1/chats/\(conversationID)/messages/\(messageID)/report",
            method: .post,
            body: ["reason": reason, "note": note],
            authorized: true
        )
    }

    func createUpload(kind: String, fileName: String, mimeType: String, totalSize: Int64, chunkSize: Int) async throws -> QGUploadDTO {
        try await request(
            path: "/v1/uploads",
            method: .post,
            bodyAny: [
                "kind": kind,
                "file_name": fileName,
                "mime_type": mimeType,
                "total_size": totalSize,
                "chunk_size": chunkSize
            ],
            authorized: true
        )
    }

    func uploadChunk(uploadID: String, chunkIndex: Int, chunkData: Data) async throws -> QGUploadDTO {
        guard let url = URL(string: "/v1/uploads/\(uploadID)/chunks/\(chunkIndex)", relativeTo: baseURL) else {
            throw QGBackendError.invalidURL
        }
        var request = URLRequest(url: url)
        request.httpMethod = QGHTTPMethod.put.rawValue
        request.httpBody = chunkData
        request.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        if !accessToken.isEmpty {
            request.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
        }
        let (data, response) = try await session.data(for: request)
        try validate(response: response, data: data)
        return try decoder.decode(QGUploadDTO.self, from: data)
    }

    func completeUpload(uploadID: String) async throws -> QGUploadDTO {
        try await request(path: "/v1/uploads/\(uploadID)/complete", method: .post, authorized: true)
    }

    func cancelUpload(uploadID: String) async throws -> QGUploadDTO {
        try await request(path: "/v1/uploads/\(uploadID)/cancel", method: .post, authorized: true)
    }

    func attachmentMeta(attachmentID: String) async throws -> QGAttachmentDTO {
        try await request(path: "/v1/attachments/\(attachmentID)", method: .get, authorized: true)
    }

    func attachmentDownload(attachmentID: String) async throws -> QGAttachmentDownloadPayload {
        guard let url = URL(string: "/v1/attachments/\(attachmentID)/download", relativeTo: baseURL) else {
            throw QGBackendError.invalidURL
        }
        var request = URLRequest(url: url)
        request.httpMethod = QGHTTPMethod.get.rawValue
        request.setValue("application/octet-stream", forHTTPHeaderField: "Accept")
        guard !accessToken.isEmpty else { throw QGBackendError.unauthorized }
        request.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")

        do {
            let (data, response) = try await session.data(for: request)
            try validate(response: response, data: data)

            let http = response as? HTTPURLResponse
            let contentDisposition = http?.value(forHTTPHeaderField: "Content-Disposition")
            let mimeType = http?.mimeType
            return QGAttachmentDownloadPayload(
                data: data,
                suggestedFileName: Self.parseContentDispositionFilename(contentDisposition),
                mimeType: mimeType
            )
        } catch let error as QGBackendError {
            throw error
        } catch {
            throw QGBackendError.transport(error.localizedDescription)
        }
    }

    func startCall(calleeUserID: String, kind: String) async throws -> QGCallDTO {
        try await request(
            path: "/v1/calls/start",
            method: .post,
            body: ["callee_user_id": calleeUserID, "kind": kind],
            authorized: true
        )
    }

    func endCall(callID: String, status: String, durationSeconds: Int) async throws -> QGCallDTO {
        try await request(
            path: "/v1/calls/\(callID)/end",
            method: .post,
            bodyAny: ["status": status, "duration_seconds": durationSeconds],
            authorized: true
        )
    }

    func listCalls(limit: Int = 100) async throws -> [QGCallDTO] {
        try await request(path: "/v1/calls?limit=\(limit)", method: .get, authorized: true)
    }

    func getUserByNickname(_ nickname: String) async throws -> QGAuthUserDTO {
        let encoded = nickname.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? nickname
        return try await request(path: "/v1/users/by-nickname/\(encoded)", method: .get, authorized: true)
    }

    func adminSetTrust(userID: String, trustLevel: Int) async throws {
        _ = try await requestRaw(
            path: "/v1/admin/users/\(userID)/trust",
            method: .post,
            bodyAny: ["trust_level": trustLevel],
            authorized: true
        )
    }

    func adminBlockUser(userID: String, block: Bool) async throws {
        _ = try await requestRaw(
            path: "/v1/admin/users/\(userID)/block",
            method: .post,
            bodyAny: ["block": block],
            authorized: true
        )
    }

    func webSocketRequest() throws -> URLRequest {
        guard !accessToken.isEmpty else { throw QGBackendError.unauthorized }
        guard
            let url = URL(string: "/v1/auth/ws", relativeTo: baseURL),
            var components = URLComponents(url: url, resolvingAgainstBaseURL: true)
        else {
            throw QGBackendError.invalidURL
        }

        switch components.scheme?.lowercased() {
        case "https":
            components.scheme = "wss"
        case "http":
            components.scheme = "ws"
        case "wss", "ws":
            break
        default:
            throw QGBackendError.invalidURL
        }

        guard let websocketURL = components.url else {
            throw QGBackendError.invalidURL
        }

        var request = URLRequest(url: websocketURL)
        request.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
        return request
    }

    func decodeRealtimeEnvelope(_ data: Data) throws -> QGRealtimeEnvelopeDTO {
        try decoder.decode(QGRealtimeEnvelopeDTO.self, from: data)
    }

    private func request<T: Decodable>(
        path: String,
        method: QGHTTPMethod,
        authorized: Bool = false
    ) async throws -> T {
        let data = try await requestRaw(path: path, method: method, authorized: authorized)
        return try decoder.decode(T.self, from: data)
    }

    private func request<T: Decodable, B: Encodable>(
        path: String,
        method: QGHTTPMethod,
        body: B?,
        authorized: Bool = false
    ) async throws -> T {
        let data = try await requestRaw(path: path, method: method, body: body, authorized: authorized)
        return try decoder.decode(T.self, from: data)
    }

    private func request<T: Decodable>(
        path: String,
        method: QGHTTPMethod,
        bodyAny: [String: Any]? = nil,
        authorized: Bool = false
    ) async throws -> T {
        let data = try await requestRaw(path: path, method: method, bodyAny: bodyAny, authorized: authorized)
        return try decoder.decode(T.self, from: data)
    }

    private func requestRaw(
        path: String,
        method: QGHTTPMethod,
        authorized: Bool = false
    ) async throws -> Data {
        try await requestRaw(path: path, method: method, body: Optional<AnyEncodable>.none, bodyAny: nil, authorized: authorized)
    }

    private func requestRaw(
        path: String,
        method: QGHTTPMethod,
        bodyAny: [String: Any]?,
        authorized: Bool = false
    ) async throws -> Data {
        try await requestRaw(path: path, method: method, body: Optional<AnyEncodable>.none, bodyAny: bodyAny, authorized: authorized)
    }

    private func requestRaw<B: Encodable>(
        path: String,
        method: QGHTTPMethod,
        body: B?,
        bodyAny: [String: Any]? = nil,
        authorized: Bool = false
    ) async throws -> Data {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw QGBackendError.invalidURL
        }
        var request = URLRequest(url: url)
        request.httpMethod = method.rawValue
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if authorized {
            guard !accessToken.isEmpty else { throw QGBackendError.unauthorized }
            request.setValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
        }

        if let bodyAny {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: bodyAny)
        } else if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try encoder.encode(AnyEncodable(body))
        }

        do {
            let (data, response) = try await session.data(for: request)
            try validate(response: response, data: data)
            return data
        } catch let error as QGBackendError {
            throw error
        } catch {
            throw QGBackendError.transport(error.localizedDescription)
        }
    }

    private func validate(response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else {
            throw QGBackendError.invalidResponse
        }
        if (200...299).contains(http.statusCode) {
            return
        }
        if http.statusCode == 401 {
            throw QGBackendError.unauthorized
        }
        if let envelope = try? decoder.decode(QGBackendErrorEnvelope.self, from: data) {
            throw QGBackendError.server(envelope.error)
        }
        let text = String(data: data, encoding: .utf8) ?? "HTTP \(http.statusCode)"
        throw QGBackendError.server(text)
    }

    private static func decodeDate(from decoder: Decoder) throws -> Date {
        let container = try decoder.singleValueContainer()
        let raw = try container.decode(String.self)

        if let date = iso8601WithFractional.date(from: raw) {
            return date
        }
        if let date = iso8601.date(from: raw) {
            return date
        }
        throw DecodingError.dataCorruptedError(in: container, debugDescription: "Invalid date format: \(raw)")
    }

    private static func parseContentDispositionFilename(_ raw: String?) -> String? {
        guard let raw else { return nil }
        let segments = raw.split(separator: ";").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }

        if let encoded = segments.first(where: { $0.lowercased().hasPrefix("filename*=") }) {
            var value = String(encoded.dropFirst("filename*=".count))
            value = value.trimmingCharacters(in: CharacterSet(charactersIn: "\""))
            if let range = value.range(of: "''") {
                value = String(value[range.upperBound...])
            }
            return value.removingPercentEncoding ?? value
        }

        if let plain = segments.first(where: { $0.lowercased().hasPrefix("filename=") }) {
            var value = String(plain.dropFirst("filename=".count))
            value = value.trimmingCharacters(in: CharacterSet(charactersIn: "\""))
            return value.isEmpty ? nil : value
        }

        return nil
    }
}

private let iso8601WithFractional: ISO8601DateFormatter = {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return f
}()

private let iso8601: ISO8601DateFormatter = {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime]
    return f
}()

private struct AnyEncodable: Encodable {
    private let encodeImpl: (Encoder) throws -> Void

    init<T: Encodable>(_ wrapped: T) {
        encodeImpl = wrapped.encode(to:)
    }

    func encode(to encoder: Encoder) throws {
        try encodeImpl(encoder)
    }
}
