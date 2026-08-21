#import "TranslateViewController.h"
#import "core_bridge.h"
#import "TTSettingsStore.h"

@interface TranslateViewController () <NSWindowDelegate>
@property (nonatomic, strong) NSPopUpButton *targetPopup;
@property (nonatomic, strong) NSTextView *sourceView;
@property (nonatomic, strong) NSTextView *resultView;
@property (nonatomic, strong) NSButton *translateButton;
@property (nonatomic, strong) NSTextField *statusLabel;
@property (nonatomic, strong) NSButton *settingsButton;
@property (nonatomic, strong) NSButton *duplicateButton;
@property (nonatomic, strong) NSButton *quitButton;

// Modal settings panel, created on demand and owned by this controller.
@property (nonatomic, strong) NSPanel *settingsPanel;
@property (nonatomic, strong) NSPopUpButton *panelProviderPopup;
@property (nonatomic, strong) NSSecureTextField *panelApiKeyField;
@end

@implementation TranslateViewController

- (void)loadView {
    NSView *view = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, 800, 540)];
    self.view = view;
    [self buildUI];
    [self restore];
}

- (void)buildUI {
    NSStackView *stack = [[NSStackView alloc] init];
    stack.orientation = NSUserInterfaceLayoutOrientationVertical;
    stack.spacing = 8;
    stack.edgeInsets = NSEdgeInsetsMake(12, 12, 12, 12);
    stack.translatesAutoresizingMaskIntoConstraints = NO;
    [self.view addSubview:stack];
    [NSLayoutConstraint activateConstraints:@[
        [stack.topAnchor constraintEqualToAnchor:self.view.topAnchor],
        [stack.leadingAnchor constraintEqualToAnchor:self.view.leadingAnchor],
        [stack.trailingAnchor constraintEqualToAnchor:self.view.trailingAnchor],
        [stack.bottomAnchor constraintEqualToAnchor:self.view.bottomAnchor]
    ]];

    self.sourceView = [[NSTextView alloc] init];
    self.resultView = [[NSTextView alloc] init];

    self.targetPopup = [[NSPopUpButton alloc] init];
    int tc = [TTCore targetCount];
    for (int i = 0; i < tc; i++) {
        [self.targetPopup addItemWithTitle:[TTCore targetDisplayName:i]];
    }
    [stack addArrangedSubview:[self rowWithTitle:@"目标语言" control:self.targetPopup]];

    [stack addArrangedSubview:[self scrollForTextView:self.sourceView
                                                height:100
                                              editable:YES
                                             growable:NO]];

    self.translateButton = [NSButton buttonWithTitle:@"翻译"
                                              target:self
                                              action:@selector(translateTapped:)];
    self.translateButton.bezelStyle = NSBezelStylePush;
    [stack addArrangedSubview:self.translateButton];

    self.statusLabel = [NSTextField labelWithString:@""];
    self.statusLabel.textColor = [NSColor secondaryLabelColor];
    self.statusLabel.font = [NSFont systemFontOfSize:11];
    [stack addArrangedSubview:self.statusLabel];

    [stack addArrangedSubview:[self scrollForTextView:self.resultView
                                                height:130
                                              editable:NO
                                             growable:YES]];

    NSStackView *bottom = [[NSStackView alloc] init];
    bottom.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    bottom.spacing = 8;
    self.settingsButton = [NSButton buttonWithTitle:@"设置"
                                             target:self
                                             action:@selector(presentSettingsPanel:)];
    self.settingsButton.bezelStyle = NSBezelStylePush;
    self.duplicateButton = [NSButton buttonWithTitle:@"复制结果"
                                              target:self
                                              action:@selector(copyTapped:)];
    self.duplicateButton.bezelStyle = NSBezelStylePush;
    self.quitButton = [NSButton buttonWithTitle:@"退出"
                                         target:self
                                         action:@selector(quitTapped:)];
    self.quitButton.bezelStyle = NSBezelStylePush;
    [bottom addArrangedSubview:self.settingsButton];
    [bottom addArrangedSubview:self.duplicateButton];
    [bottom addArrangedSubview:self.quitButton];
    [stack addArrangedSubview:bottom];
}

- (NSView *)rowWithTitle:(NSString *)title control:(NSView *)control {
    NSStackView *h = [[NSStackView alloc] init];
    h.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    h.spacing = 8;
    NSTextField *label = [NSTextField labelWithString:title];
    label.alignment = NSTextAlignmentRight;
    [label setContentCompressionResistancePriority:NSLayoutPriorityRequired
                                     forOrientation:NSLayoutConstraintOrientationHorizontal];
    [control setContentHuggingPriority:NSLayoutPriorityDefaultLow
                         forOrientation:NSLayoutConstraintOrientationHorizontal];
    [h addArrangedSubview:label];
    [h addArrangedSubview:control];
    return h;
}

// Critical: the text view must track the scroll-view width and have a non-zero
// frame, otherwise its textContainer is 0-sized and it can't be clicked into.
- (NSScrollView *)scrollForTextView:(NSTextView *)textView
                             height:(CGFloat)height
                           editable:(BOOL)editable
                           growable:(BOOL)growable {
    NSScrollView *scroll = [[NSScrollView alloc] init];
    scroll.hasVerticalScroller = YES;
    scroll.borderType = NSBezelBorder;
    scroll.translatesAutoresizingMaskIntoConstraints = NO;

    textView.editable = editable;
    textView.richText = NO;
    textView.font = [NSFont systemFontOfSize:13];
    textView.textContainerInset = NSMakeSize(4, 4);
    textView.verticallyResizable = YES;
    textView.horizontallyResizable = NO;
    textView.autoresizingMask = NSViewWidthSizable;
    textView.textContainer.widthTracksTextView = YES;
    textView.textContainer.heightTracksTextView = NO;
    CGFloat w = MAX(scroll.contentSize.width, 100);
    textView.frame = NSMakeRect(0, 0, w, MAX(scroll.contentSize.height, height));

    scroll.documentView = textView;
    if (growable) {
        [scroll.heightAnchor constraintGreaterThanOrEqualToConstant:height].active = YES;
        [scroll setContentHuggingPriority:NSLayoutPriorityDefaultLow
                            forOrientation:NSLayoutConstraintOrientationVertical];
    } else {
        [scroll.heightAnchor constraintEqualToConstant:height].active = YES;
    }
    return scroll;
}

- (void)restore {
    NSInteger target = [TTSettingsStore targetIndex];
    if (target != NSNotFound) {
        [self.targetPopup selectItemAtIndex:target];
    } else {
        [self.targetPopup selectItemAtIndex:[TTCore defaultTargetIndex]];
    }
}

- (void)save {
    NSInteger provider = [TTSettingsStore providerIndex];
    if (provider == NSNotFound) provider = 0;
    NSString *key = [TTSettingsStore apiKey] ?: @"";
    [TTSettingsStore saveProviderIndex:provider
                           targetIndex:self.targetPopup.indexOfSelectedItem
                                apiKey:key];
}

#pragma mark - Settings modal panel

- (void)presentSettingsIfNeeded {
    if ([TTSettingsStore apiKey].length == 0) {
        [self presentSettingsPanel:nil];
    }
}

- (void)presentSettingsPanel:(id)sender {
    NSWindow *parent = self.view.window;
    if (!parent) return;
    if (self.settingsPanel) {
        [self.settingsPanel makeKeyAndOrderFront:nil];
        return;
    }

    NSPanel *panel = [[NSPanel alloc]
        initWithContentRect:NSMakeRect(0, 0, 400, 150)
                  styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable
                    backing:NSBackingStoreBuffered
                      defer:NO];
    panel.title = @"设置";
    panel.releasedWhenClosed = NO;
    panel.delegate = self;
    panel.hidesOnDeactivate = NO;
    self.settingsPanel = panel;

    NSStackView *stack = [[NSStackView alloc] init];
    stack.orientation = NSUserInterfaceLayoutOrientationVertical;
    stack.spacing = 10;
    stack.edgeInsets = NSEdgeInsetsMake(12, 14, 12, 14);
    stack.translatesAutoresizingMaskIntoConstraints = NO;
    [panel.contentView addSubview:stack];
    [NSLayoutConstraint activateConstraints:@[
        [stack.topAnchor constraintEqualToAnchor:panel.contentView.topAnchor],
        [stack.leadingAnchor constraintEqualToAnchor:panel.contentView.leadingAnchor],
        [stack.trailingAnchor constraintEqualToAnchor:panel.contentView.trailingAnchor],
        [stack.bottomAnchor constraintEqualToAnchor:panel.contentView.bottomAnchor]
    ]];

    self.panelProviderPopup = [[NSPopUpButton alloc] init];
    int pc = [TTCore providerCount];
    for (int i = 0; i < pc; i++) {
        [self.panelProviderPopup addItemWithTitle:[TTCore providerDisplayName:i]];
    }
    NSInteger provider = [TTSettingsStore providerIndex];
    if (provider == NSNotFound || provider < 0) provider = 0;
    [self.panelProviderPopup selectItemAtIndex:MIN(provider, pc - 1)];
    [stack addArrangedSubview:[self rowWithTitle:@"模型" control:self.panelProviderPopup]];

    self.panelApiKeyField = [[NSSecureTextField alloc] init];
    self.panelApiKeyField.placeholderString = @"在此填写 API Key";
    self.panelApiKeyField.stringValue = [TTSettingsStore apiKey] ?: @"";
    [stack addArrangedSubview:[self rowWithTitle:@"API Key" control:self.panelApiKeyField]];

    NSStackView *buttons = [[NSStackView alloc] init];
    buttons.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    buttons.spacing = 8;
    NSView *spacer = [[NSView alloc] initWithFrame:NSZeroRect];
    [spacer setContentHuggingPriority:NSLayoutPriorityDefaultLow
                       forOrientation:NSLayoutConstraintOrientationHorizontal];
    NSButton *confirm = [NSButton buttonWithTitle:@"确定"
                                           target:self
                                           action:@selector(confirmSettingsTapped:)];
    confirm.bezelStyle = NSBezelStylePush;
    confirm.keyEquivalent = @"\r"; // Return
    NSButton *cancel = [NSButton buttonWithTitle:@"取消"
                                          target:self
                                          action:@selector(cancelSettingsTapped:)];
    cancel.bezelStyle = NSBezelStylePush;
    cancel.keyEquivalent = @"\e"; // Escape
    [buttons addArrangedSubview:spacer];
    [buttons addArrangedSubview:confirm];
    [buttons addArrangedSubview:cancel];
    [stack addArrangedSubview:buttons];

    // Center the panel over the main window, then run modally.
    NSRect parentFrame = parent.frame;
    NSRect panelFrame = panel.frame;
    CGFloat x = parentFrame.origin.x + (parentFrame.size.width - panelFrame.size.width) / 2;
    CGFloat y = parentFrame.origin.y + (parentFrame.size.height - panelFrame.size.height) / 2;
    [panel setFrameOrigin:NSMakePoint(round(x), round(y))];

    [parent addChildWindow:panel ordered:NSWindowAbove];
    [NSApp runModalForWindow:panel];
}

- (void)confirmSettingsTapped:(id)sender {
    NSInteger target = [TTSettingsStore targetIndex];
    if (target == NSNotFound) target = [TTCore defaultTargetIndex];
    [TTSettingsStore saveProviderIndex:self.panelProviderPopup.indexOfSelectedItem
                           targetIndex:target
                                apiKey:self.panelApiKeyField.stringValue ?: @""];
    [self closeSettingsPanel];
}

- (void)cancelSettingsTapped:(id)sender {
    [self closeSettingsPanel];
}

// Red close button on the panel also ends the modal session.
- (void)windowWillClose:(NSNotification *)notification {
    if (notification.object == self.settingsPanel) {
        [self closeSettingsPanel];
    }
}

- (void)closeSettingsPanel {
    NSPanel *panel = self.settingsPanel;
    if (!panel) return;
    if (NSApp.modalWindow == panel) {
        [NSApp stopModal];
    }
    NSWindow *parent = self.view.window;
    [parent removeChildWindow:panel];
    [panel orderOut:nil];
    self.settingsPanel = nil;
    self.panelProviderPopup = nil;
    self.panelApiKeyField = nil;
    [parent makeKeyAndOrderFront:nil];
}

- (void)translateTapped:(id)sender {
    NSInteger pIndex = [TTSettingsStore providerIndex];
    if (pIndex == NSNotFound || pIndex < 0) pIndex = 0;
    pIndex = MIN(pIndex, [TTCore providerCount] - 1);
    NSInteger tIndex = MIN(self.targetPopup.indexOfSelectedItem, [TTCore targetCount] - 1);
    NSString *key = [TTSettingsStore apiKey] ?: @"";
    NSString *text = self.sourceView.string ?: @"";

    [self save];

    self.statusLabel.stringValue = @"翻译中…";
    self.translateButton.enabled = NO;
    self.resultView.string = @""; // clear the previous result

    __weak typeof(self) weakSelf = self;
    [TTCore translateProvider:(int)pIndex
                       target:(int)tIndex
                       apiKey:key
                         text:text
                        delta:^(NSString *partial) { // main queue
        typeof(self) strongSelf = weakSelf;
        if (!strongSelf) return;
        // Overwrite with the running full result. `partial` is always a valid
        // UTF-8 prefix (the core holds back incomplete trailing code points),
        // so this can never show U+FFFD or truncate.
        strongSelf.resultView.string = partial ?: @"";
        [strongSelf.resultView scrollToEndOfDocument:nil];
    }
                    completion:^(BOOL ok, NSString *result, NSString *error) {
        typeof(self) strongSelf = weakSelf;
        if (!strongSelf) return;
        strongSelf.translateButton.enabled = YES;
        if (ok) {
            strongSelf.resultView.string = result ?: @"";
            strongSelf.statusLabel.stringValue = @"完成";
        } else {
            strongSelf.statusLabel.stringValue = error ?: @"错误";
        }
    }];
}

- (void)copyTapped:(id)sender {
    NSPasteboard *pb = [NSPasteboard generalPasteboard];
    [pb clearContents];
    [pb setString:self.resultView.string forType:NSPasteboardTypeString];
    self.statusLabel.stringValue = @"已复制";
}

- (void)quitTapped:(id)sender {
    [[NSApplication sharedApplication] terminate:nil];
}

@end