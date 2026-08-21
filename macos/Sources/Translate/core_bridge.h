#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN

typedef void (^TTCoreCompletion)(BOOL ok,
                                 NSString *_Nullable result,
                                 NSString *_Nullable error);

/* Thin Objective-C wrapper over the C++ kernel's C API (translate.h).
   The heavy network call runs on a background queue; the completion block is
   always invoked on the main thread. */
@interface TTCore : NSObject

+ (int)providerCount;
+ (NSString *)providerDisplayName:(int)index;
+ (int)targetCount;
+ (NSString *)targetDisplayName:(int)index;
+ (int)defaultTargetIndex;

+ (void)translateProvider:(int)provider
                   target:(int)target
                   apiKey:(NSString *)apiKey
                     text:(NSString *)text
               completion:(TTCoreCompletion)completion;

@end

NS_ASSUME_NONNULL_END
