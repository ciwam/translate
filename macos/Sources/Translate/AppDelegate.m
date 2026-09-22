#import "AppDelegate.h"
#import "TranslateViewController.h"

@interface AppDelegate ()
@property (nonatomic, strong) NSWindow *mainWindow;
@property (nonatomic, strong) NSStatusItem *statusItem;
@property (nonatomic, strong) TranslateViewController *vc;
@end

@implementation AppDelegate

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    [self setupMenus];

    self.vc = [[TranslateViewController alloc] init];

    NSRect rect = NSMakeRect(0, 0, 800, 540);
    NSWindow *window = [[NSWindow alloc]
        initWithContentRect:rect
                  styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                              NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
                    backing:NSBackingStoreBuffered
                      defer:NO];
    window.contentViewController = self.vc;
    window.title = @"Translate";
    [window center];
    [window makeKeyAndOrderFront:nil];
    self.mainWindow = window;

    // Status-bar icon as a quick toggle (show/hide the window).
    self.statusItem =
        [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    if (self.statusItem.button) {
        if (@available(macOS 11.0, *)) {
            NSImage *img = [NSImage imageWithSystemSymbolName:@"character.book.closed"
                                         accessibilityDescription:@"Translate"];
            self.statusItem.button.image = img;
        } else {
            self.statusItem.button.title = @"T";
        }
        self.statusItem.button.target = self;
        self.statusItem.button.action = @selector(toggleWindow:);
    }

    // With no API key configured yet, ask the user to fill it in right away.
    [self.vc presentSettingsIfNeeded];
}

- (void)toggleWindow:(id)sender {
    if (self.mainWindow.visible) {
        [self.mainWindow orderOut:nil];
    } else {
        [self.mainWindow makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    }
}

// Programmatic main menu. Without a nib, an Edit menu is mandatory; otherwise
// Cmd+X/C/V/A have no key equivalent routing to cut:/copy:/paste:, and the
// secure API-Key field cannot be pasted into.
- (void)setupMenus {
    NSMenu *main = [[NSMenu alloc] init];
    NSString *appName = @"Translate";

    NSMenu *appMenu = [[NSMenu alloc] init];
    [appMenu addItemWithTitle:[NSString stringWithFormat:@"关于 %@", appName]
                        action:@selector(orderFrontStandardAboutPanel:)
                 keyEquivalent:@""];
    [appMenu addItem:[NSMenuItem separatorItem]];
    [appMenu addItemWithTitle:[NSString stringWithFormat:@"退出 %@", appName]
                        action:@selector(terminate:)
                 keyEquivalent:@"q"];
    NSMenuItem *appItem = [[NSMenuItem alloc] initWithTitle:appName
                                                     action:nil
                                              keyEquivalent:@""];
    appItem.submenu = appMenu;
    [main addItem:appItem];

    NSMenu *edit = [[NSMenu alloc] initWithTitle:@"编辑"];
    [edit addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
    [edit addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"Z"];
    [edit addItem:[NSMenuItem separatorItem]];
    [edit addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
    [edit addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
    [edit addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
    [edit addItem:[NSMenuItem separatorItem]];
    [edit addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
    NSMenuItem *editItem = [[NSMenuItem alloc] initWithTitle:@"编辑"
                                                      action:nil
                                               keyEquivalent:@""];
    editItem.submenu = edit;
    [main addItem:editItem];

    // Window menu: routes Cmd+M to performMiniaturize: (the window is created
    // with NSWindowStyleMaskMiniaturizable; without this item the shortcut has
    // no responder chain target).
    NSMenu *window = [[NSMenu alloc] initWithTitle:@"窗口"];
    [window addItemWithTitle:@"最小化"
                      action:@selector(performMiniaturize:)
               keyEquivalent:@"m"];
    [window addItemWithTitle:@"缩放"
                      action:@selector(performZoom:)
               keyEquivalent:@""];
    NSMenuItem *windowItem = [[NSMenuItem alloc] initWithTitle:@"窗口"
                                                        action:nil
                                                 keyEquivalent:@""];
    windowItem.submenu = window;
    [main addItem:windowItem];

    [NSApp setMainMenu:main];
}

// Re-show the window when the Dock icon is clicked after it was closed.
- (BOOL)applicationShouldHandleReopen:(NSApplication *)sender
                     hasVisibleWindows:(BOOL)flag {
    [self.mainWindow makeKeyAndOrderFront:nil];
    return YES;
}

@end
