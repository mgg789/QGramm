@preconcurrency import AVFoundation
import CoreImage.CIFilterBuiltins
import CryptoKit
import UIKit
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

enum QGHaptics {
    static let powerSavingKey = "qg.powerSavingEnabled"

    static func light() {
        impact(.medium, intensity: 0.95)
    }

    static func medium() {
        impact(.rigid, intensity: 1)
    }

    static func heavy() {
        impact(.heavy, intensity: 1)
    }

    static func forceHeavy() {
        let generator = UIImpactFeedbackGenerator(style: .heavy)
        generator.prepare()
        generator.impactOccurred(intensity: 1)
    }

    static func error() {
        notification(.error)
    }

    private static func impact(_ style: UIImpactFeedbackGenerator.FeedbackStyle, intensity: CGFloat) {
        guard !UserDefaults.standard.bool(forKey: powerSavingKey) else { return }
        let generator = UIImpactFeedbackGenerator(style: style)
        generator.prepare()
        generator.impactOccurred(intensity: intensity)
    }

    private static func notification(_ type: UINotificationFeedbackGenerator.FeedbackType) {
        guard !UserDefaults.standard.bool(forKey: powerSavingKey) else { return }
        let generator = UINotificationFeedbackGenerator()
        generator.prepare()
        generator.notificationOccurred(type)
    }
}

@inline(__always)
func qgAnimate(_ animation: Animation, _ action: () -> Void) {
    if UserDefaults.standard.bool(forKey: QGHaptics.powerSavingKey) {
        action()
    } else {
        withAnimation(animation, action)
    }
}

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
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playback, mode: .default, options: [.defaultToSpeaker])
            try session.setActive(true)
            player = try AVAudioPlayer(contentsOf: url)
            player?.delegate = self
            player?.prepareToPlay()
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

enum CircularVideoRecorderError: LocalizedError {
    case accessDenied
    case captureUnavailable
    case recordingFailed

    var errorDescription: String? {
        switch self {
        case .accessDenied:
            return "Camera or microphone access is denied."
        case .captureUnavailable:
            return "Unable to configure camera capture."
        case .recordingFailed:
            return "Video recording failed."
        }
    }
}

@MainActor
final class CircularVideoRecorderService: NSObject, ObservableObject, AVCaptureFileOutputRecordingDelegate {
    @Published private(set) var isRecording = false
    @Published private(set) var recordingDuration: TimeInterval = 0

    private let captureSession = AVCaptureSession()
    private let movieOutput = AVCaptureMovieFileOutput()
    private var durationTimer: Timer?
    private var stopContinuation: CheckedContinuation<URL?, Never>?

    func startRecording(useFrontCamera: Bool) async throws {
        guard !isRecording else { return }

        try await ensurePermissions()
        try configureSession(useFrontCamera: useFrontCamera)

        if !captureSession.isRunning {
            captureSession.startRunning()
        }

        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .videoRecording, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try session.setActive(true)

        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
            .appendingPathExtension("mov")
        if FileManager.default.fileExists(atPath: outputURL.path) {
            try? FileManager.default.removeItem(at: outputURL)
        }

        recordingDuration = 0
        durationTimer?.invalidate()
        durationTimer = Timer.scheduledTimer(
            timeInterval: 0.2,
            target: self,
            selector: #selector(updateCircularRecordingDuration),
            userInfo: nil,
            repeats: true
        )

        movieOutput.startRecording(to: outputURL, recordingDelegate: self)
        isRecording = true
    }

    func stopRecording() async -> URL? {
        guard isRecording else { return nil }
        return await withCheckedContinuation { continuation in
            stopContinuation = continuation
            movieOutput.stopRecording()
        }
    }

    func cancelRecording() async {
        let url = await stopRecording()
        if let url {
            try? FileManager.default.removeItem(at: url)
        }
    }

    private func ensurePermissions() async throws {
        let cameraAllowed = try await ensurePermission(for: .video)
        let microphoneAllowed = try await ensurePermission(for: .audio)
        if !cameraAllowed || !microphoneAllowed {
            throw CircularVideoRecorderError.accessDenied
        }
    }

    private func ensurePermission(for mediaType: AVMediaType) async throws -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: mediaType) {
        case .authorized:
            return true
        case .notDetermined:
            return await withCheckedContinuation { continuation in
                AVCaptureDevice.requestAccess(for: mediaType) { granted in
                    continuation.resume(returning: granted)
                }
            }
        case .denied, .restricted:
            return false
        @unknown default:
            return false
        }
    }

    private func configureSession(useFrontCamera: Bool) throws {
        captureSession.beginConfiguration()
        defer { captureSession.commitConfiguration() }

        captureSession.sessionPreset = .high

        for input in captureSession.inputs {
            captureSession.removeInput(input)
        }

        let desiredPosition: AVCaptureDevice.Position = useFrontCamera ? .front : .back
        let camera = AVCaptureDevice.DiscoverySession(
            deviceTypes: [.builtInWideAngleCamera],
            mediaType: .video,
            position: desiredPosition
        ).devices.first

        guard let camera else {
            throw CircularVideoRecorderError.captureUnavailable
        }

        let videoInput = try AVCaptureDeviceInput(device: camera)
        guard captureSession.canAddInput(videoInput) else {
            throw CircularVideoRecorderError.captureUnavailable
        }
        captureSession.addInput(videoInput)

        if let microphone = AVCaptureDevice.default(for: .audio) {
            let audioInput = try AVCaptureDeviceInput(device: microphone)
            if captureSession.canAddInput(audioInput) {
                captureSession.addInput(audioInput)
            }
        }

        if !captureSession.outputs.contains(movieOutput) && captureSession.canAddOutput(movieOutput) {
            captureSession.addOutput(movieOutput)
        }
    }

    @objc private func updateCircularRecordingDuration() {
        recordingDuration += 0.2
    }

    nonisolated func fileOutput(
        _ output: AVCaptureFileOutput,
        didFinishRecordingTo outputFileURL: URL,
        from connections: [AVCaptureConnection],
        error: (any Error)?
    ) {
        Task { @MainActor in
            durationTimer?.invalidate()
            durationTimer = nil
            isRecording = false

            if captureSession.isRunning {
                captureSession.stopRunning()
            }

            var finalURL: URL?
            if error == nil, FileManager.default.fileExists(atPath: outputFileURL.path) {
                let size = (try? outputFileURL.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
                if size > 0 {
                    finalURL = outputFileURL
                }
            }

            stopContinuation?.resume(returning: finalURL)
            stopContinuation = nil
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

    static func makeThumbnail(for videoURL: URL) async throws -> URL {
        let asset = AVURLAsset(url: videoURL)
        let generator = AVAssetImageGenerator(asset: asset)
        generator.appliesPreferredTrackTransform = true

        let cgImage = try await generator.image(at: .zero).image
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

    static func saveFileToDownloads(from sourcePath: String) throws -> URL {
        let sourceURL = URL(fileURLWithPath: sourcePath)
        let downloadsURL = try FileManager.default
            .url(for: .documentDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            .appendingPathComponent("Downloads", isDirectory: true)
        try FileManager.default.createDirectory(at: downloadsURL, withIntermediateDirectories: true)

        let destinationURL = downloadsURL.appendingPathComponent(sourceURL.lastPathComponent)
        if FileManager.default.fileExists(atPath: destinationURL.path) {
            try FileManager.default.removeItem(at: destinationURL)
        }
        try FileManager.default.copyItem(at: sourceURL, to: destinationURL)
        return destinationURL
    }

    static func makePlaceholderPreviewImage() throws -> URL {
        let image = UIImage(named: "ProfilePhoto") ?? UIImage(systemName: "video.fill") ?? UIImage()
        guard let data = image.jpegData(compressionQuality: 0.86) else {
            throw CocoaError(.fileWriteUnknown)
        }
        let folderURL = try applicationSupportDirectory().appendingPathComponent("previews", isDirectory: true)
        try FileManager.default.createDirectory(at: folderURL, withIntermediateDirectories: true)
        let destination = folderURL.appendingPathComponent(UUID().uuidString).appendingPathExtension("jpg")
        try data.write(to: destination)
        return destination
    }

    static func makePlaceholderVideo(duration: TimeInterval = 2.0) throws -> URL {
        let size = CGSize(width: 720, height: 720)
        let outputURL = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
            .appendingPathExtension("mov")

        if FileManager.default.fileExists(atPath: outputURL.path) {
            try FileManager.default.removeItem(at: outputURL)
        }

        let writer = try AVAssetWriter(outputURL: outputURL, fileType: .mov)
        let outputSettings: [String: Any] = [
            AVVideoCodecKey: AVVideoCodecType.h264,
            AVVideoWidthKey: Int(size.width),
            AVVideoHeightKey: Int(size.height)
        ]

        let input = AVAssetWriterInput(mediaType: .video, outputSettings: outputSettings)
        input.expectsMediaDataInRealTime = false

        let adapter = AVAssetWriterInputPixelBufferAdaptor(
            assetWriterInput: input,
            sourcePixelBufferAttributes: [
                kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32ARGB,
                kCVPixelBufferWidthKey as String: Int(size.width),
                kCVPixelBufferHeightKey as String: Int(size.height)
            ]
        )

        guard writer.canAdd(input) else {
            throw CocoaError(.fileWriteUnknown)
        }
        writer.add(input)

        let baseImage = UIImage(named: "ProfilePhoto") ?? UIImage(systemName: "video.fill") ?? UIImage()
        let renderedImage = UIGraphicsImageRenderer(size: size).image { _ in
            UIColor.black.setFill()
            UIBezierPath(rect: CGRect(origin: .zero, size: size)).fill()
            baseImage.draw(in: CGRect(origin: .zero, size: size))
        }

        guard let pixelBuffer = makePixelBuffer(from: renderedImage, size: size) else {
            throw CocoaError(.fileWriteUnknown)
        }

        let fps: Int32 = 24
        let frameCount = max(Int(duration * Double(fps)), Int(fps))

        guard writer.startWriting() else {
            throw writer.error ?? CocoaError(.fileWriteUnknown)
        }
        writer.startSession(atSourceTime: .zero)

        for frame in 0..<frameCount {
            while !input.isReadyForMoreMediaData {
                RunLoop.current.run(until: Date().addingTimeInterval(0.001))
            }

            let time = CMTime(value: CMTimeValue(frame), timescale: fps)
            if !adapter.append(pixelBuffer, withPresentationTime: time) {
                throw writer.error ?? CocoaError(.fileWriteUnknown)
            }
        }

        input.markAsFinished()

        let semaphore = DispatchSemaphore(value: 0)
        var finishError: Error?
        writer.finishWriting {
            finishError = writer.error
            semaphore.signal()
        }
        semaphore.wait()

        if let finishError {
            throw finishError
        }

        return outputURL
    }

    private static func makePixelBuffer(from image: UIImage, size: CGSize) -> CVPixelBuffer? {
        let attributes: [CFString: Any] = [
            kCVPixelBufferCGImageCompatibilityKey: true,
            kCVPixelBufferCGBitmapContextCompatibilityKey: true
        ]

        var pixelBuffer: CVPixelBuffer?
        let status = CVPixelBufferCreate(
            kCFAllocatorDefault,
            Int(size.width),
            Int(size.height),
            kCVPixelFormatType_32ARGB,
            attributes as CFDictionary,
            &pixelBuffer
        )

        guard status == kCVReturnSuccess, let pixelBuffer else { return nil }
        CVPixelBufferLockBaseAddress(pixelBuffer, [])
        defer { CVPixelBufferUnlockBaseAddress(pixelBuffer, []) }

        guard
            let context = CGContext(
                data: CVPixelBufferGetBaseAddress(pixelBuffer),
                width: Int(size.width),
                height: Int(size.height),
                bitsPerComponent: 8,
                bytesPerRow: CVPixelBufferGetBytesPerRow(pixelBuffer),
                space: CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.noneSkipFirst.rawValue
            )
        else {
            return nil
        }

        context.clear(CGRect(origin: .zero, size: size))
        UIGraphicsPushContext(context)
        image.draw(in: CGRect(origin: .zero, size: size))
        UIGraphicsPopContext()
        return pixelBuffer
    }
}
