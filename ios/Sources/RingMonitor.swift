import Foundation
import CoreBluetooth
import Combine

final class RingMonitor: NSObject, ObservableObject, CBCentralManagerDelegate, CBPeripheralDelegate {
    @Published var status = "Ready to set up"
    @Published var detail = "Your personal ring setup will appear here."
    @Published var isConfigured = false
    @Published var wantsMonitoring = false
    @Published var isReceiving = false
    @Published var probability = 0.0
    @Published var alertsToday = 0
    @Published var samplesReceived = 0
    @Published var battery: Int?
    @Published var alertsAllowed = false
    @Published var lastAlert: Date?
    let recordings = RecordingStore.shared
    @Published var sensitivity = UserDefaults.standard.object(forKey: "sensitivity") as? Double ?? 0.5 {
        didSet { UserDefaults.standard.set(sensitivity, forKey: "sensitivity") }
    }

    private let serviceID = CBUUID(string: "98ed0001-a541-11e4-b6a0-0002a5d5c51b")
    private let writeID = CBUUID(string: "98ed0002-a541-11e4-b6a0-0002a5d5c51b")
    private let notifyID = CBUUID(string: "98ed0003-a541-11e4-b6a0-0002a5d5c51b")
    private var central: CBCentralManager!
    private var ring: CBPeripheral?
    private var writeCharacteristic: CBCharacteristic?
    private var setup: RingSetup?
    private var connectionTimeout: DispatchWorkItem?
    private var watchdog: Timer?
    private var lastSample = Date.distantPast
    private var renewedAt = Date.distantPast
    private var lastDiagnostics = Date.distantPast
    private var lastSequence: UInt8?
    private var lastFrameTime = Date.distantPast
    private var window: [MotionSample] = []
    private var strideCount = 0
    private var gate = EpisodeGate()
    private var authenticated = false
    private var stopping = false
    private var connectionGeneration = 0

    override init() {
        super.init()
        loadSetup()
        updateDay()
        central = CBCentralManager(delegate: self, queue: .main,
            options: [CBCentralManagerOptionRestoreIdentifierKey: "com.avyay.faceguard.connection"])
        watchdog = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in self?.checkStream() }
    }

    func loadSetup() {
        do {
            setup = try RingSetup.load()
            isConfigured = setup != nil
            if !wantsMonitoring {
                status = isConfigured ? "Ready when you are" : "Finishing setup"
                detail = isConfigured ? "Wear your ring, then start a session." : "Waiting for your ring’s private setup."
            }
        } catch { status = "Setup needs attention"; detail = error.localizedDescription }
    }

    func startRecording() {
        loadSetup()
        guard let model = setup?.model else { return }
        do {
            try recordings.start(model: model, threshold: 0.9 - sensitivity * 0.4)
            start()
        } catch {
            status = "Recording couldn't start"
            detail = error.localizedDescription
        }
    }

    func endRecording() {
        recordings.end()
        stop()
    }

    func start() {
        loadSetup()
        guard isConfigured else { return }
        wantsMonitoring = true
        UserDefaults.standard.set(true, forKey: "monitoring")
        samplesReceived = 0
        resetWindow()
        AlertCenter.shared.requestPermission { [weak self] allowed in self?.alertsAllowed = allowed }
        connect()
    }

    func stop() {
        wantsMonitoring = false
        UserDefaults.standard.set(false, forKey: "monitoring")
        central.stopScan()
        connectionTimeout?.cancel()
        isReceiving = false
        resetWindow()
        status = "Session paused"
        detail = "Your ring can reconnect to the Oura app."
        if authenticated, ring?.state == .connected, writeCharacteristic != nil {
            stopping = true
            write(Data([0x06, 0x04, 0, 0, 0, 0]))
            let generation = connectionGeneration
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
                guard let self, self.connectionGeneration == generation, self.stopping else { return }
                self.disconnect()
            }
        } else { disconnect() }
        saveDiagnostics()
    }

    func testAlert() {
        AlertCenter.shared.requestPermission { [weak self] allowed in
            self?.alertsAllowed = allowed
            if allowed { AlertCenter.shared.notify(test: true) }
        }
    }

    private func connect() {
        guard wantsMonitoring, setup != nil else { return }
        guard central.state == .poweredOn else {
            status = central.state == .unauthorized ? "Allow Bluetooth access" : "Turn Bluetooth on"
            detail = "Face Guard needs Bluetooth to receive movement from your ring."
            return
        }
        if let ring, ring.state == .connected || ring.state == .connecting { return }
        status = "Looking for your ring"
        detail = "Keep it nearby. If needed, place it on its charger briefly."
        central.scanForPeripherals(withServices: [serviceID])
        if let text = setup?.peripheralID, let id = UUID(uuidString: text),
           let known = central.retrievePeripherals(withIdentifiers: [id]).first {
            beginConnection(known)
        }
    }

    private func beginConnection(_ peripheral: CBPeripheral) {
        guard wantsMonitoring else { return }
        central.stopScan()
        connectionGeneration += 1
        ring = peripheral
        peripheral.delegate = self
        status = "Connecting"
        central.connect(peripheral)
        connectionTimeout?.cancel()
        let generation = connectionGeneration
        let timeout = DispatchWorkItem { [weak self] in
            guard let self, self.connectionGeneration == generation, !self.isReceiving else { return }
            self.detail = "Connection timed out. Looking for the ring again."
            self.central.cancelPeripheralConnection(peripheral)
        }
        connectionTimeout = timeout
        DispatchQueue.main.asyncAfter(deadline: .now() + 20, execute: timeout)
    }

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        if central.state != .poweredOn { isReceiving = false; resetWindow() }
        if wantsMonitoring { connect() }
    }

    func centralManager(_ central: CBCentralManager, willRestoreState dict: [String: Any]) {
        wantsMonitoring = UserDefaults.standard.bool(forKey: "monitoring")
        if let restored = (dict[CBCentralManagerRestoredStatePeripheralsKey] as? [CBPeripheral])?.first {
            ring = restored
            restored.delegate = self
            if !wantsMonitoring { central.cancelPeripheralConnection(restored) }
            else if restored.state == .connected { restored.discoverServices([serviceID]) }
        }
    }

    func centralManager(_ central: CBCentralManager, didDiscover peripheral: CBPeripheral,
                        advertisementData: [String: Any], rssi RSSI: NSNumber) {
        guard ring?.state != .connecting, ring?.state != .connected else { return }
        beginConnection(peripheral)
    }

    func centralManager(_ central: CBCentralManager, didConnect peripheral: CBPeripheral) {
        guard wantsMonitoring else { central.cancelPeripheralConnection(peripheral); return }
        status = "Authenticating"
        peripheral.discoverServices([serviceID])
    }

    func centralManager(_ central: CBCentralManager, didFailToConnect peripheral: CBPeripheral, error: Error?) {
        connectionLost()
    }

    func centralManager(_ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?) {
        connectionLost()
    }

    private func connectionLost() {
        connectionTimeout?.cancel()
        ring = nil
        writeCharacteristic = nil
        authenticated = false
        stopping = false
        isReceiving = false
        resetWindow()
        if wantsMonitoring {
            status = "Reconnecting"
            detail = "Alerts resume when live ring data returns."
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in self?.connect() }
        }
        saveDiagnostics()
    }

    func peripheral(_ peripheral: CBPeripheral, didDiscoverServices error: Error?) {
        if let error { fail(error.localizedDescription); return }
        guard let service = peripheral.services?.first(where: { $0.uuid == serviceID }) else {
            fail("The ring’s Bluetooth service is unavailable."); return
        }
        peripheral.discoverCharacteristics([writeID, notifyID], for: service)
    }

    func peripheral(_ peripheral: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?) {
        if let error { fail(error.localizedDescription); return }
        writeCharacteristic = service.characteristics?.first { $0.uuid == writeID }
        guard let notify = service.characteristics?.first(where: { $0.uuid == notifyID }), writeCharacteristic != nil else {
            fail("The ring’s data channel is unavailable."); return
        }
        peripheral.setNotifyValue(true, for: notify)
    }

    func peripheral(_ peripheral: CBPeripheral, didUpdateNotificationStateFor characteristic: CBCharacteristic, error: Error?) {
        if let error { fail(error.localizedDescription); return }
        if characteristic.isNotifying { write(Data([0x2f, 0x01, 0x2b])) }
    }

    func peripheral(_ peripheral: CBPeripheral, didWriteValueFor characteristic: CBCharacteristic, error: Error?) {
        if let error { fail(error.localizedDescription); return }
        if stopping { stopping = false; disconnect() }
    }

    func peripheral(_ peripheral: CBPeripheral, didUpdateValueFor characteristic: CBCharacteristic, error: Error?) {
        if let error { fail(error.localizedDescription); return }
        guard let data = characteristic.value else { return }
        let bytes = [UInt8](data)
        var offset = 0
        while offset + 2 <= bytes.count {
            let end = offset + 2 + Int(bytes[offset + 1])
            guard end <= bytes.count else { return }
            handle(Array(bytes[offset..<end]))
            offset = end
        }
    }

    private func handle(_ b: [UInt8]) {
        guard wantsMonitoring, b.count >= 3 else { return }
        if b[0] == 0x2f && b[2] == 0x2c {
            do {
                guard let setup else { return }
                let encrypted = try setup.authenticate(nonce: Data(b.dropFirst(3)))
                write(Data([0x2f, 0x11, 0x2d]) + encrypted)
            } catch { fail("Could not authenticate with the ring.") }
        } else if b[0] == 0x2f && b[2] == 0x2e && b.count >= 4 {
            guard b[3] == 0 else { fail("The ring rejected its saved key."); return }
            authenticated = true
            write(Data([0x0c, 0]))
            startStream()
        } else if b[0] == 0x0d { battery = Int(b[2]) }
        else if b[0] == 0x07 && b[2] != 0 { fail("The ring could not start motion tracking.") }
        else if b[0] == 0x33 && authenticated { handleMotion(b) }
    }

    private func startStream() {
        // ACM bitmask 0x20; two-minute maximum, renewed only while monitoring.
        renewedAt = Date()
        write(Data([0x06, 0x07, 0x20, 0, 0, 0, 0x02, 0, 0]))
        if !isReceiving { status = "Waiting for movement data" }
    }

    private func handleMotion(_ b: [UInt8]) {
        guard b.count >= 10, b[2] == 49 else {
            fail("This ring’s sample format differs from the calibrated format."); return
        }
        let now = Date()
        if let frame = MotionFrame(b), recordings.isRecording {
            recordings.append(frame: frame, at: now, uptime: ProcessInfo.processInfo.systemUptime)
        }
        if lastSequence == b[3] && now.timeIntervalSince(lastFrameTime) < 0.1 { return }
        if now.timeIntervalSince(lastSample) > 0.5 { resetWindow() }
        lastSequence = b[3]
        lastFrameTime = now
        lastSample = now
        connectionTimeout?.cancel()
        if !isReceiving {
            isReceiving = true
            status = "Looking out for you"
            detail = "Live ring motion · reminders are on"
        }
        for offset in stride(from: 4, through: b.count - 6, by: 6) {
            func axis(_ i: Int) -> Double { Double(Int16(bitPattern: UInt16(b[i]) | UInt16(b[i + 1]) << 8)) / 1000 }
            window.append(MotionSample(x: axis(offset), y: axis(offset + 2), z: axis(offset + 4)))
            samplesReceived += 1
            if window.count > 49 { window.removeFirst(window.count - 49) }
            strideCount += 1
            if window.count == 49 && strideCount >= 25, let model = setup?.model {
                strideCount = 0
                probability = model.probability(window)
                if gate.observe(probability: probability, threshold: 0.9 - sensitivity * 0.4, now: now.timeIntervalSince1970) {
                    updateDay()
                    alertsToday += 1
                    lastAlert = now
                    UserDefaults.standard.set(alertsToday, forKey: "alertsToday")
                    recordings.candidate(probability: probability, at: now)
                    AlertCenter.shared.notify()
                }
            }
        }
        if now.timeIntervalSince(renewedAt) > 60 { startStream() }
        if now.timeIntervalSince(lastDiagnostics) > 5 { saveDiagnostics() }
    }

    private func checkStream() {
        guard wantsMonitoring, authenticated, ring?.state == .connected else { return }
        if Date().timeIntervalSince(lastSample) > 5 {
            isReceiving = false
            status = "Waiting for ring data"
            detail = "No alerts are generated while data is missing."
            resetWindow()
            if Date().timeIntervalSince(renewedAt) > 10 { startStream() }
        }
    }

    private func resetWindow() {
        window.removeAll(keepingCapacity: true)
        strideCount = 0
        probability = 0
        gate = EpisodeGate()
        lastSequence = nil
    }

    private func write(_ data: Data) {
        guard let ring, ring.state == .connected, let writeCharacteristic else { return }
        ring.writeValue(data, for: writeCharacteristic, type: .withResponse)
    }

    private func disconnect() {
        if let ring { central.cancelPeripheralConnection(ring) }
    }

    private func fail(_ message: String) {
        stop()
        status = "Connection needs attention"
        detail = message
    }

    private func updateDay() {
        let day = Calendar.current.startOfDay(for: Date()).timeIntervalSince1970
        if UserDefaults.standard.double(forKey: "alertDay") != day {
            UserDefaults.standard.set(day, forKey: "alertDay")
            UserDefaults.standard.set(0, forKey: "alertsToday")
        }
        alertsToday = UserDefaults.standard.integer(forKey: "alertsToday")
    }

    private func saveDiagnostics() {
        lastDiagnostics = Date()
        let data: [String: Any] = ["status": status, "monitoring": wantsMonitoring, "receiving": isReceiving,
                                  "samples": samplesReceived, "alerts": alertsToday,
                                  "probability": probability, "updatedAt": lastDiagnostics.timeIntervalSince1970]
        let url = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0].appendingPathComponent("status.json")
        if let json = try? JSONSerialization.data(withJSONObject: data) { try? json.write(to: url, options: .atomic) }
    }
}
