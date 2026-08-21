#import "TranslateViewController.h"
#import "core_bridge.h"

static NSString *const kProviderKey = @"providerIndex";
static NSString *const kApiKeyKey = @"apiKey";
static NSString *const kTargetKey = @"targetIndex";

@interface TranslateViewController ()
@property (nonatomic, strong) NSPopUpButton *providerPopup;
@property (nonatomic, strong) NSSecureTextField *apiKeyField;
@property (nonatomic, strong) NSPopUpButton *targetPopup;
@property (nonatomic, strong) NSTextView *sourceView;
@property (nonatomic, strong) NSTextView *resultView;
@property (nonatomic, strong) NSButton *translateButton;
@property (nonatomic, strong) NSTextField *statusLabel;
@property (nonatomic, strong) NSButton *duplicateButton;
@property (nonatomic, strong) NSButton *quitButton;
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

    self.providerPopup = [[NSPopUpButton alloc] init];
    int pc = [TTCore providerCount];
    for (int i = 0; i < pc; i++) {
        [self.providerPopup addItemWithTitle:[TTCore providerDisplayName:i]];
    }
    [stack addArrangedSubview:[self rowWithTitle:@"模型" control:self.providerPopup]];

    self.apiKeyField = [[NSSecureTextField alloc] init];
    self.apiKeyField.placeholderString = @"在此填写 API Key";
    [stack addArrangedSubview:[self rowWithTitle:@"API Key" control:self.apiKeyField]];

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
    self.duplicateButton = [NSButton buttonWithTitle:@"复制结果"
                                         target:self
                                         action:@selector(copyTapped:)];
    self.duplicateButton.bezelStyle = NSBezelStylePush;
    self.quitButton = [NSButton buttonWithTitle:@"退出"
                                         target:self
                                         action:@selector(quitTapped:)];
    self.quitButton.bezelStyle = NSBezelStylePush;
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
    NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
    [self.providerPopup selectItemAtIndex:[d integerForKey:kProviderKey]];
    if ([d objectForKey:kTargetKey] != nil) {
        [self.targetPopup selectItemAtIndex:[d integerForKey:kTargetKey]];
    } else {
        [self.targetPopup selectItemAtIndex:[TTCore defaultTargetIndex]];
    }
    self.apiKeyField.stringValue = [d stringForKey:kApiKeyKey] ?: @"";
}

- (void)save {
    NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
    [d setInteger:self.providerPopup.indexOfSelectedItem forKey:kProviderKey];
    [d setInteger:self.targetPopup.indexOfSelectedItem forKey:kTargetKey];
    [d setObject:self.apiKeyField.stringValue forKey:kApiKeyKey];
}

- (void)translateTapped:(id)sender {
    NSInteger pIndex = MIN(self.providerPopup.indexOfSelectedItem, [TTCore providerCount] - 1);
    NSInteger tIndex = MIN(self.targetPopup.indexOfSelectedItem, [TTCore targetCount] - 1);
    NSString *key = self.apiKeyField.stringValue;
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
