import Foundation

let screenTextMaximumReplyBytes = 2 * 1024 * 1024

private final class ScreenTextEntry: @unchecked Sendable {
    let request: ScreenTextRequest
    let cancellation = ScreenTextCancellation()
    let created = DispatchTime.now().uptimeNanoseconds
    let deadline: UInt64
    var task: Task<Void, Never>?
    var watchdog: DispatchWorkItem?
    var reply: Data?
    init(_ request: ScreenTextRequest) {
        self.request = request
        deadline = created + UInt64(request.timeoutMilliseconds) * 1_000_000
    }
}

final class ScreenTextRegistry: @unchecked Sendable {
    private let lock = NSLock()
    private let environment: any ScreenTextEnvironment
    private var next: UInt64 = 1
    private var entries: [UInt64: ScreenTextEntry] = [:]

    init(environment: any ScreenTextEnvironment) { self.environment = environment }

    func start(_ data: Data) -> UInt64 {
        guard let request = try? decodeScreenTextRequest(data) else { return 0 }
        reap()
        lock.lock()
        guard entries.count < 2 else { lock.unlock(); return 0 }
        var handle = next
        repeat {
            handle = next
            next = next == UInt64.max ? 1 : next + 1
        } while entries[handle] != nil
        let entry = ScreenTextEntry(request)
        entries[handle] = entry
        let environment = self.environment
        let watchdog = DispatchWorkItem { [weak self] in self?.expire(handle) }
        entry.watchdog = watchdog
        entry.task = Task.detached(priority: .utility) { [weak self] in
            let reply = await performScreenText(request, environment: environment, cancellation: entry.cancellation)
            self?.finish(handle, reply: reply)
        }
        lock.unlock()
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + .milliseconds(request.timeoutMilliseconds), execute: watchdog)
        return handle
    }

    // A size query or too-small buffer does not consume the result. This lets a
    // cgo-free caller allocate exactly the bounded response before copying it.
    func poll(_ handle: UInt64, capacity: Int) -> (Int32, Data?) {
        guard capacity >= 0, capacity <= screenTextMaximumReplyBytes else { return (-1, nil) }
        reap()
        lock.lock()
        defer { lock.unlock() }
        guard let entry = entries[handle] else { return (-1, nil) }
        guard let data = entry.reply else { return (0, nil) }
        if capacity >= data.count { entries.removeValue(forKey: handle) }
        return (Int32(data.count), data)
    }

    func result(_ handle: UInt64, capacity: Int) -> Data? { poll(handle, capacity: capacity).1 }

    func cancel(_ handle: UInt64) {
        lock.lock()
        let entry = entries.removeValue(forKey: handle)
        lock.unlock()
        stop(entry)
    }

    private func finish(_ handle: UInt64, reply: ScreenTextReply) {
        lock.lock()
        guard let entry = entries[handle], entry.reply == nil else { lock.unlock(); return }
        let result = DispatchTime.now().uptimeNanoseconds >= entry.deadline ? screenTextReply(entry.request, status: "timeout") : reply
        entry.reply = encode(result) ?? encode(screenTextReply(entry.request, status: "reply_limit"))
        let watchdog = entry.watchdog
        lock.unlock()
        watchdog?.cancel()
    }

    private func expire(_ handle: UInt64) {
        lock.lock()
        guard let entry = entries[handle], entry.reply == nil else { lock.unlock(); return }
        entry.reply = encode(screenTextReply(entry.request, status: "timeout"))
        lock.unlock()
        entry.cancellation.cancel()
        entry.task?.cancel()
    }

    private func reap() {
        let now = DispatchTime.now().uptimeNanoseconds
        lock.lock()
        let expired = entries.filter { now >= $0.value.created && now - $0.value.created > 30_000_000_000 }
        for (handle, _) in expired { entries.removeValue(forKey: handle) }
        lock.unlock()
        for (_, entry) in expired { stop(entry) }
    }

    private func stop(_ entry: ScreenTextEntry?) {
        entry?.watchdog?.cancel()
        entry?.cancellation.cancel()
        entry?.task?.cancel()
    }

    private func encode(_ reply: ScreenTextReply) -> Data? {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        encoder.outputFormatting = [.withoutEscapingSlashes]
        guard let data = try? encoder.encode(reply), data.count > 0, data.count <= screenTextMaximumReplyBytes else { return nil }
        return data
    }
}

private let screenTextRegistry = ScreenTextRegistry(environment: NativeScreenTextEnvironment())

// No entry point reads a filename, requests permission, or writes an image.
@_cdecl("blur_screen_text_start")
public func blurScreenTextStart(_ pointer: UnsafePointer<UInt8>?, _ length: Int32) -> UInt64 {
    guard let pointer, length > 0, length <= 4096 else { return 0 }
    return screenTextRegistry.start(Data(bytes: pointer, count: Int(length)))
}

@_cdecl("blur_screen_text_poll")
public func blurScreenTextPoll(_ handle: UInt64, _ output: UnsafeMutablePointer<UInt8>?, _ capacity: Int32) -> Int32 {
    guard capacity >= 0, capacity <= screenTextMaximumReplyBytes else { return -1 }
    let (size, data) = screenTextRegistry.poll(handle, capacity: output == nil ? 0 : Int(capacity))
    if let output, let data, size > 0, Int(capacity) >= data.count { data.copyBytes(to: output, count: data.count) }
    return size
}

@_cdecl("blur_screen_text_cancel")
public func blurScreenTextCancel(_ handle: UInt64) { screenTextRegistry.cancel(handle) }
