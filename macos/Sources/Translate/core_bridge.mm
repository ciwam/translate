#import "core_bridge.h"
#include "translate.h"

#include <cstdlib>
#include <string>

// C trampoline bridging the kernel's C callback to an Objective-C block.
static void tt_trampoline(int ok, const char *text, const char *error, void *userdata) {
    TTCoreCompletion block = (__bridge_transfer TTCoreCompletion)userdata;
    NSString *result = text ? [NSString stringWithUTF8String:text] : nil;
    NSString *errMsg = error ? [NSString stringWithUTF8String:error] : nil;
    if (text) translate_free((char *)text);
    if (error) translate_free((char *)error);
    dispatch_async(dispatch_get_main_queue(), ^{
        block(ok ? YES : NO, result, errMsg);
    });
}

@implementation TTCore

+ (int)providerCount {
    return translate_provider_count();
}

+ (NSString *)providerDisplayName:(int)index {
    return [NSString stringWithUTF8String:translate_provider_display_name(index)];
}

+ (int)targetCount {
    return translate_target_count();
}

+ (NSString *)targetDisplayName:(int)index {
    return [NSString stringWithUTF8String:translate_target_display_name(index)];
}

+ (int)defaultTargetIndex {
    return translate_default_target_index();
}

+ (void)translateProvider:(int)provider
                   target:(int)target
                   apiKey:(NSString *)apiKey
                     text:(NSString *)text
               completion:(TTCoreCompletion)completion {
    TTCoreCompletion block = [completion copy];
    NSString *key = apiKey ?: @"";
    NSString *src = text ?: @"";
    dispatch_async(dispatch_get_global_queue(DISPATCH_QUEUE_PRIORITY_DEFAULT, 0), ^{
        translate_run((translate_provider_t)provider,
                      (translate_target_t)target,
                      [key UTF8String],
                      [src UTF8String],
                      tt_trampoline,
                      (__bridge_retained void *)block);
    });
}

@end
