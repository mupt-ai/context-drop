import Foundation

final class SnapshotProtocol: URLProtocol {
    static var payload = Data()
    static var fails = false
    static var dashboardRequests = 0
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard request.url?.path == "/dashboard-data.json" else {
            client?.urlProtocol(self, didFailWithError: URLError(.userAuthenticationRequired))
            return
        }
        Self.dashboardRequests += 1
        precondition(request.httpMethod == "GET")
        precondition(request.cachePolicy == .reloadIgnoringLocalCacheData)
        precondition(request.value(forHTTPHeaderField: "Cache-Control") == "no-cache")
        if Self.fails {
            client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
            return
        }
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.payload)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@main
struct SnapshotTests {
    static func snapshot(_ timestamp: String, latest: String, nights: String = "[]", rolling: String = "[]") throws -> HealthSnapshot {
        try JSONDecoder().decode(HealthSnapshot.self, from: Data("""
        {"meta":{"generatedAt":"\(timestamp)"},"latestNight":\(latest),"nights":\(nights),"rolling30":\(rolling)}
        """.utf8))
    }
    @MainActor static func main() async throws {
        let oldNight = #"{"day":"2026-09-13","totalSleepMinutes":419.5}"#
        let newNight = #"{"day":"2026-09-14","totalSleepMinutes":461}"#
        let old = try snapshot("2026-09-13T16:00:00Z", latest: oldNight)
        let fresh = try snapshot("2026-09-14T15:03:45.093Z", latest: oldNight, nights: "[\(newNight),\(oldNight)]")
        precondition(fresh.currentNight?.day == "2026-09-14")
        precondition(fresh.currentNight?.totalSleepMinutes == 461)
        precondition(fresh.allNights.count == 2)
        precondition(old.predates(fresh) && !fresh.predates(old) && !fresh.predates(fresh))
        precondition(fresh.currentNight?.sleepDateLabel == "Oura · Woke Sep 14, 2026")
        precondition(old.currentNight?.sleepDateLabel == "Oura · Woke Sep 13, 2026")
        let savedTimeZone = NSTimeZone.default
        NSTimeZone.default = TimeZone(secondsFromGMT: -12 * 3600)!
        precondition(fresh.currentNight?.sleepDateLabel == "Oura · Woke Sep 14, 2026")
        NSTimeZone.default = TimeZone(secondsFromGMT: 14 * 3600)!
        precondition(fresh.currentNight?.sleepDateLabel == "Oura · Woke Sep 14, 2026")
        NSTimeZone.default = savedTimeZone
        let fallback = try snapshot("2026-09-14T16:00:00Z", latest: "null", rolling: "[{\"night\":\(newNight)},{\"night\":\(oldNight)}]")
        precondition(fallback.currentNight?.day == "2026-09-14")
        let invalid = try snapshot("unknown", latest: #"{"day":"2026-02-30"}"#)
        precondition(invalid.currentNight == nil)
        precondition(invalid.latestNight?.sleepDateLabel == "Oura · Sleep date unavailable")
        let missing = try snapshot("2026-09-14T16:00:00Z", latest: #"{"day":"2026-09-14"}"#)
        precondition(missing.currentNight?.totalSleepMinutes == nil)
        let periods = try snapshot("2026-09-14T16:00:00Z", latest: "null", nights: #"[{"day":"2026-09-14","period":2},{"day":"2026-09-14","period":0}]"#)
        precondition(periods.currentNight?.period == 2)

        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let cache = directory.appendingPathComponent("health-dashboard.json")
        try JSONEncoder().encode(old).write(to: cache)
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [SnapshotProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        let store = HealthStore(directory: directory, session: session)
        precondition(store.snapshot?.currentNight?.day == "2026-09-13")
        SnapshotProtocol.payload = try JSONEncoder().encode(fresh)
        await store.refresh()
        precondition(store.snapshot?.currentNight?.totalSleepMinutes == 461)
        precondition(store.lastChecked != nil && !store.isSyncing)
        let saved = try Data(contentsOf: cache)
        SnapshotProtocol.payload = try JSONEncoder().encode(old)
        await store.refresh()

[39 more lines in file. Use offset=81 to continue.]