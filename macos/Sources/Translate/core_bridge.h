#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN

typedef void (^TTCoreCompletion)(BOOL ok,
                                 NSString *_Nullable result,
                                 NSString *_Nullable error);

/* Incremental progress. `partial` is the full translation accumulated so far
   (valid UTF-8, never split mid-character); overwrite your output view with it.
   Invoked on the main queue. */
typedef void (^TTCoreDelta)(NSString *_Nullable partial);

/* Thin Objective-C wrapper over the C++ kernel's C API (translate.h).
   The heavy network call runs on a background queue; the delta and completion
   blocks are always invoked on the main thread. */
@interface TTCore : NSObject

+ (int)providerCount;
+ (NSString *)providerDisplayName:(int)index;
+ (int)targetCount;
+ (NSString *)targetDisplayName:(int)index;
+ (int)defaultTargetIndex;

+ (void)translateProvider:(int)provider
                   target:(int)target
                   apiKey:(NSString *)apiKey
                 glossary:(NSString *)glossary
                     text:(NSString *)text
               completion:(TTCoreCompletion)completion;

/* Streaming variant: `delta` is called as translation progresses, `completion`
   exactly once at the end. `delta` may be nil. `glossary` may be nil. */
+ (void)translateProvider:(int)provider
                   target:(int)target
                   apiKey:(NSString *)apiKey
                 glossary:(NSString *)glossary
                     text:(NSString *)text
                    delta:(TTCoreDelta _Nullable)delta
               completion:(TTCoreCompletion)completion;

@end

NS_ASSUME_NONNULL_END
