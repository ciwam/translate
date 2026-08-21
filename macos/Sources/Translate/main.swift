import Cocoa
import Foundation

// MARK: - App Delegate（菜单栏常驻，无主窗口，内存占用最小）

final class AppDelegate: NSObject, NSApplicationDelegate {
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let translator = Translator()
    private var mainWindow: NSWindow?

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupMenus()
        let vc = TranslateViewController(translator: translator)
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 800, height: 540),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.contentViewController = vc
        window.title = "Translate"
        window.center()
        window.makeKeyAndOrderFront(nil)
        mainWindow = window

        // 菜单栏图标作为快捷入口（点击显示/隐藏窗口）
        if let button = statusItem.button {
            if #available(macOS 11.0, *) {
                button.image = NSImage(systemSymbolName: "character.book.closed",
                                       accessibilityDescription: "Translate")
            } else {
                button.title = "T"
            }
            button.action = #selector(toggleWindow(_:))
            button.target = self
        }
    }

    @objc private func toggleWindow(_ sender: Any?) {
        guard let window = mainWindow else { return }
        if window.isVisible {
            window.orderOut(nil)
        } else {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
        }
    }

    // 程序化创建主菜单：没有 nib 时，必须手动提供 Edit 菜单，
    // 否则 Cmd+X/C/V/A 没有 key equivalent 路由到 paste:/copy: 等，
    // 密码框（NSSecureTextField）也就无法粘贴。
    private func setupMenus() {
        let main = NSMenu()

        let appName = "Translate"
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "关于 \(appName)",
                        action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)),
                        keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "退出 \(appName)",
                        action: #selector(NSApplication.terminate(_:)),
                        keyEquivalent: "q")
        let appItem = NSMenuItem(title: appName, action: nil, keyEquivalent: "")
        appItem.submenu = appMenu
        main.addItem(appItem)

        let edit = NSMenu(title: "编辑")
        edit.addItem(withTitle: "撤销", action: Selector(("undo:")), keyEquivalent: "z")
        edit.addItem(withTitle: "重做", action: Selector(("redo:")), keyEquivalent: "Z")
        edit.addItem(.separator())
        edit.addItem(withTitle: "剪切", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        edit.addItem(withTitle: "拷贝", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "粘贴", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(.separator())
        edit.addItem(withTitle: "全选", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        let editItem = NSMenuItem(title: "编辑", action: nil, keyEquivalent: "")
        editItem.submenu = edit
        main.addItem(editItem)

        NSApp.mainMenu = main
    }

    // 点击 Dock 图标时（窗口被关掉后）重新显示
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        mainWindow?.makeKeyAndOrderFront(nil)
        return true
    }
}

// MARK: - 翻译面板

final class TranslateViewController: NSViewController {
    private let translator: Translator

    private let providerPopup = NSPopUpButton()
    private let apiKeyField = NSSecureTextField()
    private let targetPopup = NSPopUpButton()
    private let sourceView = NSTextView()
    private let resultView = NSTextView()
    private let translateButton = NSButton(title: "Translate", target: nil, action: nil)
    private let statusLabel = NSTextField(labelWithString: "")
    private let copyButton = NSButton(title: "复制结果", target: nil, action: nil)
    private let quitButton = NSButton(title: "退出", target: nil, action: nil)

    private enum Keys {
        static let provider = "providerIndex"
        static let apiKey = "apiKey"
        static let target = "targetIndex"
    }

    init(translator: Translator) {
        self.translator = translator
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    override func loadView() {
        view = NSView(frame: NSRect(x: 0, y: 0, width: 800, height: 540))
        buildUI()
        restore()
    }

    private func buildUI() {
        let stack = NSStackView()
        stack.orientation = .vertical
        stack.spacing = 8
        stack.edgeInsets = NSEdgeInsets(top: 12, left: 12, bottom: 12, right: 12)
        stack.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.topAnchor.constraint(equalTo: view.topAnchor),
            stack.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            stack.bottomAnchor.constraint(equalTo: view.bottomAnchor)
        ])

        providerPopup.addItems(withTitles: TranslationProvider.allCases.map { $0.displayName })
        stack.addArrangedSubview(row("模型", providerPopup))

        apiKeyField.placeholderString = "在此填写 API Key"
        stack.addArrangedSubview(row("Key", apiKeyField))

        targetPopup.addItems(withTitles: TargetLanguage.allCases.map { $0.displayName })
        stack.addArrangedSubview(row("目标", targetPopup))

        stack.addArrangedSubview(scroll(for: sourceView, height: 100, editable: true))

        translateButton.bezelStyle = .rounded
        translateButton.target = self
        translateButton.action = #selector(translateTapped)
        stack.addArrangedSubview(translateButton)

        statusLabel.textColor = .secondaryLabelColor
        statusLabel.font = .systemFont(ofSize: 11)
        stack.addArrangedSubview(statusLabel)

        stack.addArrangedSubview(scroll(for: resultView, height: 130, editable: false, growable: true))

        let bottom = NSStackView()
        bottom.orientation = .horizontal
        bottom.spacing = 8
        copyButton.bezelStyle = .rounded
        copyButton.target = self
        copyButton.action = #selector(copyTapped)
        quitButton.bezelStyle = .rounded
        quitButton.target = self
        quitButton.action = #selector(quitTapped)
        bottom.addArrangedSubview(copyButton)
        bottom.addArrangedSubview(quitButton)
        stack.addArrangedSubview(bottom)
    }

    private func row(_ title: String, _ control: NSView) -> NSView {
        let h = NSStackView()
        h.orientation = .horizontal
        h.spacing = 8
        let label = NSTextField(labelWithString: title)
        label.alignment = .right
        label.setContentCompressionResistancePriority(.required, for: .horizontal)
        control.setContentHuggingPriority(.defaultLow, for: .horizontal)
        h.addArrangedSubview(label)
        h.addArrangedSubview(control)
        return h
    }

    private func scroll(for textView: NSTextView, height: CGFloat, editable: Bool, growable: Bool = false) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.borderType = .bezelBorder
        scroll.translatesAutoresizingMaskIntoConstraints = false

        // 关键：让文本视图跟随滚动视图宽度并拥有可输入区域，
        // 否则 NSTextView 默认 frame/textContainer 为 0，点击不到、无法输入。
        textView.isEditable = editable
        textView.isRichText = false
        textView.font = .systemFont(ofSize: 13)
        textView.textContainerInset = NSSize(width: 4, height: 4)
        textView.isVerticallyResizable = true
        textView.isHorizontallyResizable = false
        textView.autoresizingMask = [.width]
        if let tc = textView.textContainer {
            tc.widthTracksTextView = true
            tc.heightTracksTextView = false
        }
        let w = max(scroll.contentSize.width, 100)
        textView.frame = NSRect(x: 0, y: 0, width: w, height: max(scroll.contentSize.height, height))

        scroll.documentView = textView
        if growable {
            // 窗口变大时，结果框自动撑大（输入/按钮/标签保持自然高度）
            scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: height).isActive = true
            scroll.setContentHuggingPriority(.defaultLow, for: .vertical)
        } else {
            scroll.heightAnchor.constraint(equalToConstant: height).isActive = true
        }
        return scroll
    }

    private func restore() {
        let d = UserDefaults.standard
        providerPopup.selectItem(at: d.integer(forKey: Keys.provider))
        if let saved = d.object(forKey: Keys.target) as? Int {
            targetPopup.selectItem(at: saved)
        } else if let idx = TargetLanguage.allCases.firstIndex(of: .chinese) {
            // 首次启动：默认目标语言为「简体中文」
            targetPopup.selectItem(at: idx)
        }
        apiKeyField.stringValue = d.string(forKey: Keys.apiKey) ?? ""
    }

    private func save() {
        let d = UserDefaults.standard
        d.set(providerPopup.indexOfSelectedItem, forKey: Keys.provider)
        d.set(targetPopup.indexOfSelectedItem, forKey: Keys.target)
        d.set(apiKeyField.stringValue, forKey: Keys.apiKey)
    }

    @objc private func translateTapped() {
        let pIndex = min(providerPopup.indexOfSelectedItem, TranslationProvider.allCases.count - 1)
        let tIndex = min(targetPopup.indexOfSelectedItem, TargetLanguage.allCases.count - 1)
        let provider = TranslationProvider.allCases[pIndex]
        let target = TargetLanguage.allCases[tIndex]
        let key = apiKeyField.stringValue
        let text = sourceView.string
        save()

        statusLabel.stringValue = "Translating…"
        translateButton.isEnabled = false
        translator.translate(text: text, provider: provider, apiKey: key, target: target) { [weak self] result in
            DispatchQueue.main.async {
                guard let self else { return }
                self.translateButton.isEnabled = true
                switch result {
                case .success(let out):
                    self.resultView.string = out
                    self.statusLabel.stringValue = "完成"
                case .failure(let err):
                    self.statusLabel.stringValue = err.localizedDescription
                }
            }
        }
    }

    @objc private func copyTapped() {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(resultView.string, forType: .string)
        statusLabel.stringValue = "已复制"
    }

    @objc private func quitTapped() {
        NSApplication.shared.terminate(nil)
    }
}

// MARK: - 入口（纯代码驱动，无 nib / 无 SwiftUI）

let app = NSApplication.shared
app.setActivationPolicy(.regular)     // 正常 App：出现在 Dock / 桌面，带窗口
let delegate = AppDelegate()
app.delegate = delegate
app.run()
