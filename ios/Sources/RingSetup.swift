import Foundation
import Security
import CommonCrypto

struct RingSetup: Codable {
    let keyHex: String
    let peripheralID: String?
    let model: TouchModel

    var key: Data? {
        guard keyHex.count == 32 else { return nil }
        var data = Data()
        var index = keyHex.startIndex
        for _ in 0..<16 {
            let end = keyHex.index(index, offsetBy: 2)
            guard let byte = UInt8(keyHex[index..<end], radix: 16) else { return nil }
            data.append(byte)
            index = end
        }
        return data
    }

    static func load() throws -> RingSetup? {
        let url = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("faceguard-setup.json")
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword,
                                    kSecAttrService as String: "com.avyay.faceguard.ring",
                                    kSecAttrAccount as String: "active-ring"]
        if FileManager.default.fileExists(atPath: url.path) {
            let data = try Data(contentsOf: url)
            let setup = try JSONDecoder().decode(RingSetup.self, from: data)
            guard setup.key != nil, setup.model.isValid else { throw SetupError.invalid }
            let attributes: [String: Any] = [kSecValueData as String: data,
                kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly]
            var result = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
            if result == errSecItemNotFound {
                result = SecItemAdd(query.merging(attributes) { _, new in new } as CFDictionary, nil)
            }
            guard result == errSecSuccess else { throw SetupError.keychain(result) }
            try FileManager.default.removeItem(at: url)
            return setup
        }
        var read = query
        read[kSecReturnData as String] = true
        read[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(read as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw SetupError.keychain(status) }
        let setup = try JSONDecoder().decode(RingSetup.self, from: data)
        guard setup.key != nil, setup.model.isValid else { throw SetupError.invalid }
        return setup
    }

    func authenticate(nonce: Data) throws -> Data {
        guard nonce.count == 15, let key else { throw SetupError.invalid }
        var input = nonce
        input.append(1)
        var output = Data(count: 16)
        var written = 0
        let status = output.withUnsafeMutableBytes { out in
            input.withUnsafeBytes { inputBytes in
                key.withUnsafeBytes { keyBytes in
                    CCCrypt(CCOperation(kCCEncrypt), CCAlgorithm(kCCAlgorithmAES),
                            CCOptions(kCCOptionECBMode), keyBytes.baseAddress, 16, nil,
                            inputBytes.baseAddress, 16, out.baseAddress, 16, &written)
                }
            }
        }
        guard status == kCCSuccess, written == 16 else { throw SetupError.invalid }
        return output
    }
}

enum SetupError: LocalizedError {
    case invalid
    case keychain(OSStatus)
    var errorDescription: String? {
        switch self {
        case .invalid: return "The ring setup file is invalid."
        case .keychain: return "Could not securely save the ring setup. Unlock your phone and retry."
        }
    }
}
