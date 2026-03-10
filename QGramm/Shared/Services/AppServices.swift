@preconcurrency import AVFoundation
import CoreImage.CIFilterBuiltins
import CryptoKit
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

struct QGCryptoService {
    func makeSharedKey() -> String {
        let key = SymmetricKey(size: .bits256)
        return Data(key.withUnsafeBytes { Data($0) }).base64EncodedString()
    }

    func generateRecoveryKey() -> String {
        let alphabet = Array("ABCDEFGHJKLMNPQRSTUVWXYZ23456789")
        let bytes = (0..<24).map { _ in alphabet.randomElement() ?? "A" }
        return stride(from: 0, to: bytes.count, by: 4)
            .map { index in String(bytes[index..<min(index + 4, bytes.count)]) }
            .joined(separator: "-")
    }

    func encrypt(_ text: String, using base64Key: String) -> SecureTextEnvelope? {
        guard
            let plainData = text.data(using: .utf8),
            let keyData = Data(base64Encoded: base64Key)
        else {
            return nil
        }

        let key = SymmetricKey(data: keyData)

        do {
            let sealed = try ChaChaPoly.seal(plainData, using: key)
            return SecureTextEnvelope(
                nonce: Data(sealed.nonce).base64EncodedString(),
                ciphertext: sealed.ciphertext.base64EncodedString(),
                tag: sealed.tag.base64EncodedString()
            )
        } catch {
            return nil
        }
    }

    func decrypt(_ envelope: SecureTextEnvelope?, using base64Key: String) -> String {
        guard
            let envelope,
            let keyData = Data(base64Encoded: base64Key),
            let nonceData = Data(base64Encoded: envelope.nonce),
            let ciphertext = Data(base64Encoded: envelope.ciphertext),
            let tag = Data(base64Encoded: envelope.tag)
        else {
            return ""
        }

        let key = SymmetricKey(data: keyData)

        do {
            let nonce = try ChaChaPoly.Nonce(data: nonceData)
            let sealedBox = try ChaChaPoly.SealedBox(nonce: nonce, ciphertext: ciphertext, tag: tag)
            let data = try ChaChaPoly.open(sealedBox, using: key)
            return String(decoding: data, as: UTF8.self)
        } catch {
            return ""
        }
    }
}

@MainActor
final class AudioRecorderService: NSObject, ObservableObject, AVAudioRecorderDelegate, AVAudioPlayerDelegate {
    @Published private(set) var isRecording = false
    @Published private(set) var recordingDuration: TimeInterval = 0
    @Published private(set) var playingURL: URL?

    private var recorder: AVAudioRecorder?
    private var player: AVAudioPlayer?
    private var timer: Timer?

    func startRecording() throws {
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .default, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try session.setActive(true)

        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
            .appendingPathExtension("m4a")

        let settings: [String: Any] = [
            AVFormatIDKey: Int(kAudioFormatMPEG4AAC),
            AVSampleRateKey: 44_100,
            AVNumberOfChannelsKey: 1,
            AVEncoderAudioQualityKey: AVAudioQuality.high.rawValue
        ]

        recorder = try AVAudioRecorder(url: url, settings: settings)
        recorder?.delegate = self
        recorder?.record()
        isRecording = true
        recordingDuration = 0

        timer = Timer.scheduledTimer(timeInterval: 0.2, target: self, selector: #selector(updateRecordingDuration), userInfo: nil, repeats: true)
    }

    func stopRecording() -> URL? {
        timer?.invalidate()
        timer = nil
        recorder?.stop()
        isRecording = false
        let url = recorder?.url
        recorder = nil
        return url
    }

    func togglePlayback(for url: URL) {
        if playingURL == url {
            player?.stop()
            playingURL = nil
            return
        }

        do {
            player = try AVAudioPlayer(contentsOf: url)
            player?.delegate = self
            player?.play()
            playingURL = url
        } catch {
            playingURL = nil
        }
    }

    @objc private func updateRecordingDuration() {
        recordingDuration = recorder?.currentTime ?? 0
    }

    nonisolated func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        Task { @MainActor in
            self.playingURL = nil
        }
    }
}

enum QGMediaTools {
    static func fileSize(for url: URL) -> Int64 {
        let values = try? url.resourceValues(forKeys: [.fileSizeKey])
        return Int64(values?.fileSize ?? 0)
    }

    static func copyItemIntoAppSupport(from originalURL: URL, folder: String, preferredExtension: String? = nil) throws -> URL {
        let supportDirectory = try applicationSupportDirectory()
        let folderURL = supportDirectory.appendingPathComponent(folder, isDirectory: true)
        try FileManager.default.createDirectory(at: folderURL, withIntermediateDirectories: true, attributes: nil)

        let fileExtension = preferredExtension ?? originalURL.pathExtension
        let destination = folderURL
            .appendingPathComponent(UUID().uuidString)
            .appendingPathExtension(fileExtension)

        let hasSecurityScope = originalURL.startAccessingSecurityScopedResource()
        defer {
            if hasSecurityScope {
                originalURL.stopAccessingSecurityScopedResource()
            }
        }

        if FileManager.default.fileExists(atPath: destination.path) {
            try FileManager.default.removeItem(at: destination)
        }

        try FileManager.default.copyItem(at: originalURL, to: destination)
        return destination
    }

    static func makeThumbnail(for videoURL: URL) throws -> URL {
        let asset = AVURLAsset(url: videoURL)
        let generator = AVAssetImageGenerator(asset: asset)
        generator.appliesPreferredTrackTransform = true

        let cgImage = try generator.copyCGImage(at: .zero, actualTime: nil)
        let uiImage = UIImage(cgImage: cgImage)
        guard let data = uiImage.jpegData(compressionQuality: 0.82) else {
            throw CocoaError(.fileWriteUnknown)
        }

        let folderURL = try applicationSupportDirectory().appendingPathComponent("previews", isDirectory: true)
        try FileManager.default.createDirectory(at: folderURL, withIntermediateDirectories: true, attributes: nil)

        let destination = folderURL.appendingPathComponent(UUID().uuidString).appendingPathExtension("jpg")
        try data.write(to: destination)
        return destination
    }

    static func qrImage(for string: String) -> UIImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.setValue(Data(string.utf8), forKey: "inputMessage")
        filter.correctionLevel = "Q"

        guard let outputImage = filter.outputImage else { return nil }

        let transformed = outputImage.transformed(by: CGAffineTransform(scaleX: 12, y: 12))
        let context = CIContext()

        guard let cgImage = context.createCGImage(transformed, from: transformed.extent) else {
            return nil
        }

        return UIImage(cgImage: cgImage)
    }

    static func applicationSupportDirectory() throws -> URL {
        let url = try FileManager.default
            .url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            .appendingPathComponent("QGramm", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true, attributes: nil)
        return url
    }
}
