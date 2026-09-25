import SwiftUI

// MARK: - Source and models

enum DigestSource {
    static let host = URL(string: "http://100.71.240.14:17371")!
    static var latest: URL { host.appendingPathComponent("latest.json") }
    static var archive: URL { host.appendingPathComponent("archive.json") }

    static func entry(_ jsonPath: String) -> URL {
        let trimmed = jsonPath.trimmingCharacters(in: .whitespacesAndNewlines)
        return host.appendingPathComponent(trimmed.hasPrefix("/") ? String(trimmed.dropFirst()) : trimmed)
    }

    static func fetch<T: Decodable>(_ url: URL) async -> T? {
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 15)
        request.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
        do {
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else { return nil }
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            return nil
        }
    }

    static func fetchArchive() async -> [DigestArchiveEntry]? {
        let index: DigestArchiveIndex? = await fetch(archive)
        return index?.digests
    }

    static func document(for entry: DigestArchiveEntry) async -> DigestDocument? {
        guard let path = entry.jsonPath else { return nil }
        return await fetch(self.entry(path))
    }
}

struct DigestDocument: Codable, Hashable, Identifiable {
    struct Module: Codable, Hashable, Identifiable {
        let id: String
        let label: String
        let status: String?
        let priority: String?
        let summary: String?
        let raw: String?
    }

    struct Counts: Codable, Hashable {
        let modules: Int?
        let actions: Int?
        let links: Int?
    }

    let id: String
    let title: String?
    let slot: String?
    let capturedAt: String?
    let generatedAt: String?
    let executiveSummary: [String]?
    let modules: [Module]?
    let counts: Counts?
}

struct DigestArchiveEntry: Codable, Hashable, Identifiable {
    let id: String
    let title: String?
    let slot: String?
    let capturedAt: String?
    let jsonPath: String?
}

struct DigestArchiveIndex: Codable {
    let latest: DigestArchiveEntry?
    let digests: [DigestArchiveEntry]?
}

@MainActor
final class DigestStore: ObservableObject {
    @Published var latest: DigestDocument?
    @Published var archive: [DigestArchiveEntry] = []
    @Published var loadError: String?
    @Published var isRefreshing = false

    func refresh() async {
        isRefreshing = true
        loadError = nil
        async let latestFetch: DigestDocument? = DigestSource.fetch(DigestSource.latest)
        async let archiveFetch: [DigestArchiveEntry]? = DigestSource.fetchArchive()
        let (fetchedLatest, fetchedArchive) = await (latestFetch, archiveFetch)
        if let fetchedLatest { latest = fetchedLatest }
        if let fetchedArchive { archive = fetchedArchive }
        if fetchedLatest == nil && fetchedArchive == nil {
            loadError = "Couldn't reach your digest. Make sure Tailscale is connected, then try again."
        }
        isRefreshing = false
    }
}

// MARK: - Digest

struct DigestView: View {
    @StateObject private var store = DigestStore()

    var body: some View {
        NavigationStack {
            Group {
                if let latest = store.latest {
                    DigestDocumentView(document: latest, archive: store.archive)
                } else if let error = store.loadError {
                    ContentUnavailableView {
                        Label("Digest Unavailable", systemImage: "wifi.exclamationmark")
                    } description: {
                        Text(error)
                    } actions: {
                        Button("Try Again") { Task { await store.refresh() } }
                            .buttonStyle(HealthPrimaryButtonStyle(fillsWidth: false))
                    }
                } else {
                    ProgressView("Loading your digest…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            .background(HealthStyle.paper)
            .healthNavigationTitle("Digest")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Refresh", systemImage: "arrow.clockwise") {
                        Task { await store.refresh() }
                    }
                    .disabled(store.isRefreshing)
                }
            }
            .refreshable { await store.refresh() }
            .task { if store.latest == nil { await store.refresh() } }
        }
    }
}

struct DigestDocumentView: View {
    let document: DigestDocument
    let archive: [DigestArchiveEntry]

    private var dateLabel: String? {
        guard let capturedAt = document.capturedAt, let date = DigestText.date(capturedAt) else { return nil }
        return date.formatted(.dateTime.weekday(.wide).month(.wide).day())
    }

    private var metadata: String {
        [document.slot?.capitalized, dateLabel].compactMap { $0 }.joined(separator: " · ")
    }

    private var counts: String? {
        guard let counts = document.counts else { return nil }
        var values: [String] = []
        if let modules = counts.modules, modules > 0 { values.append("\(modules) sections") }
        if let actions = counts.actions, actions > 0 { values.append("\(actions) actions") }
        if let links = counts.links, links > 0 { values.append("\(links) links") }
        return values.isEmpty ? nil : values.joined(separator: " · ")
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: HealthStyle.sectionGap) {
                VStack(alignment: .leading, spacing: 7) {
                    if !metadata.isEmpty {
                        Text(metadata)
                            .font(.subheadline)
                            .foregroundStyle(HealthStyle.secondaryInk)
                    }
                    Text(document.title ?? "Personal Digest")
                        .font(HealthStyle.title)
                        .fixedSize(horizontal: false, vertical: true)
                    if let counts {
                        Text(counts)
                            .font(.caption)
                            .foregroundStyle(HealthStyle.secondaryInk)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                if let summary = document.executiveSummary, !summary.isEmpty {
                    DigestCard(title: "Summary", systemImage: "sparkles") {
                        VStack(alignment: .leading, spacing: 12) {
                            ForEach(Array(summary.enumerated()), id: \.offset) { _, paragraph in
                                DigestRichText(paragraph)
                            }
                        }
                    }
                }

                ForEach(document.modules ?? []) { module in
                    DigestModuleView(module: module)
                }

                if !archive.isEmpty {
                    DigestArchiveView(entries: archive, currentID: document.id)
                }
            }
            .padding(.horizontal, HealthStyle.pageInset)
            .padding(.vertical, 16)
        }
        .background(HealthStyle.paper)
        .scrollBounceBehavior(.basedOnSize)
    }
}

struct DigestCard<Content: View>: View {
    let title: String
    let systemImage: String
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Label(title, systemImage: systemImage)
                .font(.headline)
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .healthCard()
    }
}

struct DigestModuleView: View {
    let module: DigestDocument.Module

    private var lines: [String] {
        let source = (module.raw?.isEmpty == false ? module.raw : module.summary) ?? ""
        return source
            .split(separator: "\n", omittingEmptySubsequences: true)
            .map {
                let line = $0.trimmingCharacters(in: .whitespaces)
                return line.hasPrefix("- ") ? String(line.dropFirst(2)) : line
            }
            .filter { !$0.isEmpty }
    }

    private var icon: String {
        switch module.id.lowercased() {
        case let id where id.contains("action"): "checklist"
        case let id where id.contains("mail") || id.contains("inbox"): "envelope"
        case let id where id.contains("calendar"): "calendar"
        case let id where id.contains("oura") || id.contains("health"): "heart"
        case let id where id.contains("twitter") || id.contains("social"): "bubble.left.and.bubble.right"
        default: "text.alignleft"
        }
    }

    var body: some View {
        DigestCard(title: module.label, systemImage: icon) {
            if let status = module.status, !status.isEmpty {
                Text(status.capitalized)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(HealthStyle.secondaryInk)
            }
            if lines.isEmpty {
                Text("No updates in this section.")
                    .font(.subheadline)
                    .foregroundStyle(HealthStyle.secondaryInk)
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Image(systemName: "circle.fill")
                                .font(.system(size: 5))
                                .foregroundStyle(HealthStyle.secondaryInk)
                            DigestRichText(line)
                        }
                    }
                }
            }
        }
    }
}

struct DigestArchiveView: View {
    let entries: [DigestArchiveEntry]
    let currentID: String

    private var recent: [DigestArchiveEntry] {
        Array(entries.filter { $0.id != currentID }.prefix(10))
    }

    var body: some View {
        DigestCard(title: "Recent Digests", systemImage: "clock.arrow.circlepath") {
            if recent.isEmpty {
                Text("No earlier digests yet.")
                    .font(.subheadline)
                    .foregroundStyle(HealthStyle.secondaryInk)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(recent.enumerated()), id: \.element.id) { index, entry in
                        if index > 0 { Divider() }
                        NavigationLink {
                            DigestDetailView(entry: entry)
                        } label: {
                            HStack(spacing: 12) {
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(entry.title ?? entry.id)
                                        .font(.body.weight(.medium))
                                        .lineLimit(2)
                                    if let slot = entry.slot, !slot.isEmpty {
                                        Text(slot.capitalized)
                                            .font(.caption)
                                            .foregroundStyle(HealthStyle.secondaryInk)
                                    }
                                }
                                Spacer()
                                Image(systemName: "chevron.right")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(HealthStyle.secondaryInk)
                            }
                            .padding(.vertical, 12)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
        }
    }
}

struct DigestDetailView: View {
    let entry: DigestArchiveEntry
    @State private var document: DigestDocument?
    @State private var failed = false

    var body: some View {
        Group {
            if let document {
                DigestDocumentView(document: document, archive: [])
            } else if failed {
                ContentUnavailableView("Digest Unavailable", systemImage: "doc.questionmark")
            } else {
                ProgressView("Loading digest…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(HealthStyle.paper)
        .healthNavigationTitle("Past Digest")
        .task {
            document = await DigestSource.document(for: entry)
            failed = document == nil
        }
    }
}

struct DigestRichText: View {
    let text: String

    init(_ text: String) {
        self.text = text
    }

    var body: some View {
        Text(DigestText.attributed(text))
            .font(.body)
            .lineSpacing(4)
            .fixedSize(horizontal: false, vertical: true)
            .textSelection(.enabled)
    }
}

enum DigestText {
    static func date(_ iso: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: iso) { return date }
        return ISO8601DateFormatter().date(from: iso)
    }

    static func attributed(_ line: String) -> AttributedString {
        var result = AttributedString()
        let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue)
        let range = NSRange(line.startIndex..., in: line)
        let matches = detector?.matches(in: line, options: [], range: range) ?? []
        var cursor = line.startIndex
        for match in matches {
            guard let url = match.url, let matchRange = Range(match.range, in: line) else { continue }
            if cursor < matchRange.lowerBound {
                result += AttributedString(String(line[cursor..<matchRange.lowerBound]))
            }
            var linked = AttributedString(String(line[matchRange]))
            linked.link = url
            linked.foregroundColor = HealthStyle.ink
            linked.underlineStyle = .single
            result += linked
            cursor = matchRange.upperBound
        }
        if cursor < line.endIndex {
            result += AttributedString(String(line[cursor..<line.endIndex]))
        }
        return result
    }
}
