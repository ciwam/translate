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

// Context object owned by the streaming callback's C pointer until the
// terminal event; holds the user's progress and completion blocks.
@interface TTCoreStreamContext : NSObject
@property (nonatomic, copy) TTCoreDelta delta;
@property (nonatomic, copy) TTCoreCompletion completion;
@end

@implementation TTCoreStreamContext
@end

// Streaming C trampoline. `userdata` is a retained TTCoreStreamContext owned by
// the C pointer until the TERMINAL event; DELTA events borrow it and must NOT
// release it (main-queue blocks run in FIFO dispatch order, so the terminal
// transfer always runs after the last delta block).
static void tt_stream_trampoline(translate_event_t event,
                                 const char *text,
                                 const char *error,
                                 void *userdata) {
    NSString *str = text ? [NSString stringWithUTF8String:text] : nil;
    NSString *errMsg = error ? [NSString stringWithUTF8String:error] : nil;
    if (text) translate_free((char *)text);
    if (error) translate_free((char *)error);

    if (event == TRANSLATE_EVENT_DELTA) {
        dispatch_async(dispatch_get_main_queue(), ^{
            TTCoreStreamContext *ctx = (__bridge TTCoreStreamContext *)userdata; // borrowed
            if (ctx.delta) ctx.delta(str);
        });
        return;
    }
    // Terminal (DONE/ERROR): transfer the pointer's +1 so ARC releases the
    // context exactly once, on the main queue, after the completion block runs.
    dispatch_async(dispatch_get_main_queue(), ^{
        TTCoreStreamContext *ctx = (__bridge_transfer TTCoreStreamContext *)userdata;
        if (event == TRANSLATE_EVENT_DONE && ctx.completion) {
            ctx.completion(YES, str, nil);
        } else if (ctx.completion) {
            ctx.completion(NO, nil, errMsg);
        }
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
                 glossary:(NSString *)glossary
                     text:(NSString *)text
               completion:(TTCoreCompletion)completion {
    TTCoreCompletion block = [completion copy];
    NSString *key = apiKey ?: @"";
    NSString *gloss = glossary ?: @"";
    NSString *src = text ?: @"";
    dispatch_async(dispatch_get_global_queue(DISPATCH_QUEUE_PRIORITY_DEFAULT, 0), ^{
        translate_run((translate_provider_t)provider,
                      (translate_target_t)target,
                      [key UTF8String],
                      [gloss UTF8String],
                      [src UTF8String],
                      tt_trampoline,
                      (__bridge_retained void *)block);
    });
}

+ (void)translateProvider:(int)provider
                   target:(int)target
                   apiKey:(NSString *)apiKey
                 glossary:(NSString *)glossary
                     text:(NSString *)text
                    delta:(TTCoreDelta)delta
               completion:(TTCoreCompletion)completion {
    TTCoreStreamContext *ctx = [[TTCoreStreamContext alloc] init];
    ctx.delta = [delta copy];
    ctx.completion = [completion copy];
    NSString *key = apiKey ?: @"";
    NSString *gloss = glossary ?: @"";
    NSString *src = text ?: @"";
    dispatch_async(dispatch_get_global_queue(DISPATCH_QUEUE_PRIORITY_DEFAULT, 0), ^{
        translate_run_stream((translate_provider_t)provider,
                             (translate_target_t)target,
                             [key UTF8String],
                             [gloss UTF8String],
                             [src UTF8String],
                             tt_stream_trampoline,
                             (__bridge_retained void *)ctx);
    });
}

@end
