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

/* Decrypted API key for ONE provider. Each provider (GLM-4-Flash,
   Qwen2.5-7B-Instruct) has its own key — a Zhipu key never authenticates
   against SiliconFlow. Returns nil when nothing is stored for that provider.
   Legacy single-key files (format 1) are honored for the provider that was
   selected when the legacy key was saved. Plaintext exists only in memory,
   never on disk. */
+ (nullable NSString *)apiKeyForProvider:(NSInteger)provider;

/* All stored keys, keyed by provider index. Legacy files surface their single
   key under the provider it was saved with. */
+ (NSDictionary<NSNumber *, NSString *> *)allApiKeys;

/* User terminology dictionary, one "term = translation" per line. Stored in
   plaintext (not sensitive). Nil when unset. */
+ (nullable NSString *)glossary;

+ (void)saveProviderIndex:(NSInteger)provider
              targetIndex:(NSInteger)target
                  apiKeys:(nullable NSDictionary<NSNumber *, NSString *> *)apiKeysByProvider
                 glossary:(nullable NSString *)glossary;

@end

NS_ASSUME_NONNULL_END
