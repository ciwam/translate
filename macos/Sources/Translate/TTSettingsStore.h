#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN

/* Persists app settings to ~/Library/Application Support/<Bundle ID>/.
   The API key is encrypted with AES-256; the random key lives in an adjacent
   file (0600 permissions). This stops casual inspection / accidental copy of
   the plaintext key, but NOT a determined attacker with the same user
   privileges — a deliberate trade-off of not using the Keychain. */
@interface TTSettingsStore : NSObject

/* NSNotFound if the settings file has never been written (first launch). */
+ (NSInteger)providerIndex;
+ (NSInteger)targetIndex;

/* Decrypted API key; nil when nothing is stored. Plaintext exists only in
   memory, never on disk. */
+ (nullable NSString *)apiKey;

+ (void)saveProviderIndex:(NSInteger)provider
              targetIndex:(NSInteger)target
                   apiKey:(nullable NSString *)apiKey;

@end

NS_ASSUME_NONNULL_END
