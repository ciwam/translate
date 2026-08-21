#import <Cocoa/Cocoa.h>

@interface TranslateViewController : NSViewController

/* Shows the centered settings modal if no API key has been configured yet
   (empty or never stored); used on first launch. */
- (void)presentSettingsIfNeeded;

@end
