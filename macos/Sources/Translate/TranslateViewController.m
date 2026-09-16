#import "TranslateViewController.h"
#import "core_bridge.h"
#import "TTSettingsStore.h"

@interface TranslateViewController () <NSWindowDelegate, NSTextViewDelegate>
@property (nonatomic, strong) NSPopUpButton *targetPopup;
@property (nonatomic, strong) NSTextView *sourceView;
@property (nonatomic, strong) NSTextView *resultView;
@property (nonatomic, strong) NSTextField *statusLabel;
@property (nonatomic, strong) NSButton *settingsButton;
@property (nonatomic, strong) NSButton *duplicateButton;
@property (nonatomic, strong) NSButton *quitButton;

// Debounce timer for auto-translation after typing pauses.
@property (nonatomic, strong) dispatch_source_t translateTimer;
// Bumped on every auto-translate so stale deltas/completions are dropped.
@property (nonatomic, assign) NSUInteger translationGeneration;
// Throttle for forcing full layout of the streaming result view.
@property (nonatomic, assign) NSTimeInterval lastResultLayoutTime;

// Modal settings panel, created on demand and owned by this controller.
@property (nonatomic, strong) NSPanel *settingsPanel;
@property (nonatomic, strong) NSPopUpButton *panelProviderPopup;
@property (nonatomic, strong) NSSecureTextField *panelApiKeyField;
// Edit-in-progress keys, keyed by provider index. Switching the model moves
// the field's text to the provider it belongs to and swaps in the other
// provider's key, so each model keeps its own credential.
@property (nonatomic, strong) NSMutableDictionary<NSNumber *, NSString *> *panelKeysByProvider;
@property (nonatomic, assign) NSInteger panelActiveProvider;
@property (nonatomic, strong) NSTextView *panelGlossaryView;
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

    [stack addArrangedSubview:[self scrollForTextView:self.sourceView
                                                height:100
                                              editable:YES
                                             growable:NO]];

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
    [bottom addArrangedSubview:[self rowWithTitle:@"目标语言" control:self.targetPopup]];
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

// Critical: the text view must track the scroll-view width through
// autoresizing. Give it a height only — a manually-set start width (even a
// 100pt fallback) gets ADDED to the clip's growth when the scroll view lays
// out, leaving the document ~100pt wider than the viewport and enabling
// horizontal panning. A zero-width start lets the clip drive the width; after
// layout it equals the clip width, so text wraps and long unbreakable tokens
// clip at the right edge instead of extending the scrollable area.
- (NSScrollView *)scrollForTextView:(NSTextView *)textView
                             height:(CGFloat)height
                           editable:(BOOL)editable
                           growable:(BOOL)growable {
    NSScrollView *scroll = [[NSScrollView alloc] init];
    scroll.hasVerticalScroller = YES;
    scroll.hasHorizontalScroller = NO;
    // With short text the document is shorter than the clip (scroll range 0),
    // so any trackpad gesture is elastic overscroll — a visible "jump". Disable
    // rubber-banding: no range, no bounce.
    scroll.verticalScrollElasticity = NSScrollElasticityNone;
    scroll.horizontalScrollElasticity = NSScrollElasticityNone;
    scroll.borderType = NSBezelBorder;
    scroll.translatesAutoresizingMaskIntoConstraints = NO;

    textView.editable = editable;
    textView.richText = NO;
    textView.delegate = self;
    textView.font = [NSFont systemFontOfSize:13];
    textView.textContainerInset = NSMakeSize(4, 4);
    textView.verticallyResizable = YES;
    textView.horizontallyResizable = NO;
    textView.autoresizingMask = NSViewWidthSizable;
    textView.textContainer.widthTracksTextView = YES;
    textView.textContainer.heightTracksTextView = NO;
    textView.frame = NSMakeRect(0, 0, 0, MAX(scroll.contentSize.height, height));

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
    NSString *glossary = [TTSettingsStore glossary] ?: @"";
    [TTSettingsStore saveProviderIndex:provider
                           targetIndex:self.targetPopup.indexOfSelectedItem
                                apiKeys:[TTSettingsStore allApiKeys]
                              glossary:glossary];
}

#pragma mark - Settings modal panel

- (void)presentSettingsIfNeeded {
    NSInteger provider = [TTSettingsStore providerIndex];
    if (provider == NSNotFound) provider = 0;
    if ([TTSettingsStore apiKeyForProvider:provider].length == 0) {
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
        initWithContentRect:NSMakeRect(0, 0, 420, 340)
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
    self.panelActiveProvider = MIN(provider, pc - 1);
    [self.panelProviderPopup selectItemAtIndex:self.panelActiveProvider];
    // Switching the model swaps the key field to that provider's own key
    // (each vendor authenticates only against its own endpoint).
    [self.panelProviderPopup setTarget:self];
    [self.panelProviderPopup setAction:@selector(providerPopupChanged:)];
    [stack addArrangedSubview:[self rowWithTitle:@"模型" control:self.panelProviderPopup]];

    self.panelKeysByProvider = [[NSMutableDictionary alloc] init];
    for (int i = 0; i < pc; i++) {
        NSString *k = [TTSettingsStore apiKeyForProvider:i];
        if (k.length) self.panelKeysByProvider[@(i)] = k;
    }

    self.panelApiKeyField = [[NSSecureTextField alloc] init];
    self.panelApiKeyField.placeholderString = @"在此填写当前模型的 API Key";
    self.panelApiKeyField.stringValue =
        self.panelKeysByProvider[@(self.panelActiveProvider)] ?: @"";
    [stack addArrangedSubview:[self rowWithTitle:@"API Key" control:self.panelApiKeyField]];

    NSTextField *glossaryLabel = [NSTextField labelWithString:@"术语词典（每行一条：术语 = 翻译）"];
    glossaryLabel.font = [NSFont systemFontOfSize:11];
    glossaryLabel.textColor = [NSColor secondaryLabelColor];
    [stack addArrangedSubview:glossaryLabel];

    self.panelGlossaryView = [[NSTextView alloc] init];
    self.panelGlossaryView.string = [TTSettingsStore glossary] ?: @"";
    // scrollForTextView sets the text-did-change delegate, but textDidChange
    // early-returns for any view other than sourceView, so editing the glossary
    // here never triggers a translation.
    NSScrollView *glossaryScroll = [self scrollForTextView:self.panelGlossaryView
                                                   height:120
                                                 editable:YES
                                                growable:NO];
    [stack addArrangedSubview:glossaryScroll];

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

// Model popup changed: file the currently edited key under the provider it
// was typed for, then show the newly selected provider's own key.
- (void)providerPopupChanged:(id)sender {
    NSInteger selected = self.panelProviderPopup.indexOfSelectedItem;
    if (selected == self.panelActiveProvider) return;
    NSString *edited = self.panelApiKeyField.stringValue ?: @"";
    if (edited.length) {
        self.panelKeysByProvider[@(self.panelActiveProvider)] = edited;
    } else {
        [self.panelKeysByProvider removeObjectForKey:@(self.panelActiveProvider)];
    }
    self.panelActiveProvider = selected;
    self.panelApiKeyField.stringValue = self.panelKeysByProvider[@(selected)] ?: @"";
}

- (void)confirmSettingsTapped:(id)sender {
    NSInteger target = [TTSettingsStore targetIndex];
    if (target == NSNotFound) target = [TTCore defaultTargetIndex];
    NSString *edited = self.panelApiKeyField.stringValue ?: @"";
    if (edited.length) {
        self.panelKeysByProvider[@(self.panelActiveProvider)] = edited;
    } else {
        [self.panelKeysByProvider removeObjectForKey:@(self.panelActiveProvider)];
    }
    [TTSettingsStore saveProviderIndex:self.panelProviderPopup.indexOfSelectedItem
                           targetIndex:target
                                apiKeys:self.panelKeysByProvider
                              glossary:self.panelGlossaryView.string ?: @""];
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
    self.panelKeysByProvider = nil;
    self.panelGlossaryView = nil;
    [parent makeKeyAndOrderFront:nil];
}

static const NSTimeInterval kAutoTranslateDelay = 0.8;

// Translating starts automatically once the user pauses typing in the source
// text view; a debounce prevents a request per keystroke.
- (void)textDidChange:(NSNotification *)notification {
    if (notification.object != self.sourceView) return;
    if (self.translateTimer) {
        dispatch_source_cancel(self.translateTimer);
        self.translateTimer = nil;
    }
    dispatch_source_t timer = dispatch_source_create(
        DISPATCH_SOURCE_TYPE_TIMER, 0, 0, dispatch_get_main_queue());
    dispatch_source_set_timer(timer,
                             dispatch_time(DISPATCH_TIME_NOW,
                                           (int64_t)(kAutoTranslateDelay * NSEC_PER_SEC)),
                             DISPATCH_TIME_FOREVER, 0);
    dispatch_source_set_event_handler(timer, ^{
        self.translateTimer = nil;
        [self startTranslation];
    });
    self.translateTimer = timer;
    dispatch_resume(timer);
}

// The streaming result view's frame grows lazily: appended text is laid out
// only when it becomes visible. Without forcing layout, the document height
// lags behind the real text, and the first scroll to the far end triggers a
// layout catch-up that grows the document mid-scroll — a one-time jump. Force
// full layout periodically while streaming (throttled) and once at completion
// so the document height tracks the content and the scroll range stays stable.
- (void)keepResultLayoutCurrent {
    NSTimeInterval now = [NSDate timeIntervalSinceReferenceDate];
    if (now - self.lastResultLayoutTime > 0.1) {
        self.lastResultLayoutTime = now;
        [self.resultView.layoutManager
            ensureLayoutForTextContainer:self.resultView.textContainer];
    }
}

- (void)startTranslation {
    NSString *raw = self.sourceView.string ?: @"";
    NSCharacterSet *ws = [NSCharacterSet whitespaceAndNewlineCharacterSet];
    if ([raw stringByTrimmingCharactersInSet:ws].length == 0) {
        self.resultView.string = @"";
        self.statusLabel.stringValue = @"";
        return;
    }

    NSInteger pIndex = [TTSettingsStore providerIndex];
    if (pIndex == NSNotFound || pIndex < 0) pIndex = 0;
    pIndex = MIN(pIndex, [TTCore providerCount] - 1);
    NSInteger tIndex = MIN(self.targetPopup.indexOfSelectedItem, [TTCore targetCount] - 1);
    NSString *key = [TTSettingsStore apiKeyForProvider:pIndex] ?: @"";
    NSString *glossary = [TTSettingsStore glossary] ?: @"";

    [self save];

    NSUInteger gen = ++self.translationGeneration;
    self.lastResultLayoutTime = 0;
    self.statusLabel.stringValue = @"翻译中…";
    self.resultView.string = @""; // clear the previous result
    // Guarantee the source document's height is final before the user scrolls
    // (paste normally sizes it eagerly; this covers any lazy layout state so
    // the first scroll pass has a stable range).
    [self.sourceView.layoutManager
        ensureLayoutForTextContainer:self.sourceView.textContainer];

    __weak typeof(self) weakSelf = self;
    [TTCore translateProvider:(int)pIndex
                       target:(int)tIndex
                       apiKey:key
                     glossary:glossary
                         text:raw
                        delta:^(NSString *partial) { // main queue
        typeof(self) strongSelf = weakSelf;
        if (!strongSelf || gen != strongSelf.translationGeneration) return;
        // Overwrite with the running full result. `partial` is always a valid
        // UTF-8 prefix (the core holds back incomplete trailing code points),
        // so this can never show U+FFFD or truncate.
        // Apply the running result incrementally. Each delta is a strict prefix
        // extension of the previous one, so appending only the new characters
        // to the text storage typesets just that range — instead of replacing
        // the whole string every delta, which re-typesets the entire document
        // each frame, saturates the main thread, and makes manual scrolling in
        // either box visibly jump.
        NSTextStorage *ts = strongSelf.resultView.textStorage;
        NSString *current = ts.string;
        NSString *partialText = partial ?: @"";
        if (partialText.length != current.length ||
            ![partialText hasPrefix:current]) {
            // Not a strict prefix extension (a retry reset the stream, or the
            // content diverged): replace wholesale.
            [ts replaceCharactersInRange:NSMakeRange(0, ts.length)
                              withString:partialText];
        } else if (partialText.length > current.length) {
            [ts beginEditing];
            [ts replaceCharactersInRange:NSMakeRange(current.length,
                                                     partialText.length - current.length)
                              withString:[partialText substringFromIndex:current.length]];
            [ts endEditing];
        }
        [strongSelf keepResultLayoutCurrent];
        // Follow the stream only while the user is already at the bottom; if
        // they scrolled up to read earlier lines, don't yank the view back
        // down on every delta.
        NSScrollView *rv = strongSelf.resultView.enclosingScrollView;
        if (rv) {
            NSClipView *clip = rv.contentView;
            CGFloat maxY = clip.documentRect.size.height - clip.bounds.size.height;
            if (maxY <= 0 || clip.bounds.origin.y >= maxY - 40) {
                [strongSelf.resultView scrollToEndOfDocument:nil];
            }
        }
    }
                    completion:^(BOOL ok, NSString *result, NSString *error) {
        typeof(self) strongSelf = weakSelf;
        if (!strongSelf || gen != strongSelf.translationGeneration) return;
        if (ok) {
            if (![strongSelf.resultView.string isEqualToString:result]) {
                // The incremental deltas normally already delivered the final
                // text; replace only when they diverged (e.g. a very short
                // result that streamed as a single delta).
                strongSelf.resultView.string = result ?: @"";
            }
            // Finalize the document height so the first scroll after the
            // translation has a stable range (no mid-scroll layout catch-up).
            [strongSelf.resultView.layoutManager
                ensureLayoutForTextContainer:strongSelf.resultView.textContainer];
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