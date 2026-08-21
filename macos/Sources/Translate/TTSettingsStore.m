#import "TTSettingsStore.h"

#import <CommonCrypto/CommonCryptor.h>
#import <Security/SecRandom.h>

// Legacy NSUserDefaults keys, used only for the one-time migration.
static NSString *const kLegacyProviderKey = @"providerIndex";
static NSString *const kLegacyApiKeyKey = @"apiKey";
static NSString *const kLegacyTargetKey = @"targetIndex";

static NSString *const kSettingsFileName = @"settings.plist";
static NSString *const kKeyFileName = @"encryption.key";

static NSString *const kSettingsProviderKey = @"providerIndex";
static NSString *const kSettingsTargetKey = @"targetIndex";
static NSString *const kSettingsApiKeyKey = @"apiKeyCipher";
static NSString *const kSettingsFormatKey = @"formatVersion";
static const NSInteger kSettingsFormatVersion = 1;

static const size_t kKeySize = 32; // AES-256
static const size_t kIVSize = 16;  // AES block size

@implementation TTSettingsStore

#pragma mark - Public API

+ (NSInteger)providerIndex {
    NSNumber *value = [self settingsDictionary][kSettingsProviderKey];
    return value ? value.integerValue : NSNotFound;
}

+ (NSInteger)targetIndex {
    NSNumber *value = [self settingsDictionary][kSettingsTargetKey];
    return value ? value.integerValue : NSNotFound;
}

+ (nullable NSString *)apiKey {
    NSDictionary *settings = [self settingsDictionary];
    NSString *cipherB64 = settings[kSettingsApiKeyKey];
    if (![cipherB64 isKindOfClass:[NSString class]] || cipherB64.length == 0) {
        return nil;
    }
    NSData *key = [self loadOrCreateEncryptionKey];
    NSData *cipher = [[NSData alloc] initWithBase64EncodedString:cipherB64 options:0];
    NSData *plain = [self decrypt:cipher key:key];
    if (!plain) return nil;
    return [[NSString alloc] initWithData:plain encoding:NSUTF8StringEncoding];
}

+ (void)saveProviderIndex:(NSInteger)provider
              targetIndex:(NSInteger)target
                   apiKey:(nullable NSString *)apiKey {
    NSString *plain = apiKey ?: @"";
    NSData *key = [self loadOrCreateEncryptionKey];
    NSData *cipher = [self encrypt:[plain dataUsingEncoding:NSUTF8StringEncoding] key:key];
    if (!cipher) return;

    // Read the raw plist (no migration): this method is itself the writer used
    // by the migration, so triggering migration here would recurse forever.
    NSMutableDictionary *settings =
        [[NSDictionary dictionaryWithContentsOfURL:[self settingsFileURL]] mutableCopy]
            ?: [NSMutableDictionary dictionary];
    settings[kSettingsProviderKey] = @(provider);
    settings[kSettingsTargetKey] = @(target);
    settings[kSettingsApiKeyKey] = [cipher base64EncodedStringWithOptions:0];
    settings[kSettingsFormatKey] = @(kSettingsFormatVersion);
    [settings writeToURL:[self settingsFileURL] atomically:YES];
}

#pragma mark - File layout

+ (NSURL *)storeDirectoryURL {
    NSArray<NSURL *> *appSupport = [[NSFileManager defaultManager]
        URLsForDirectory:NSApplicationSupportDirectory
               inDomains:NSUserDomainMask];
    NSURL *base = appSupport.firstObject;
    NSString *bundleID = NSBundle.mainBundle.bundleIdentifier ?: @"Translate";
    NSURL *dir = [[base URLByAppendingPathComponent:bundleID] URLByStandardizingPath];
    [NSFileManager.defaultManager createDirectoryAtURL:dir
                           withIntermediateDirectories:YES
                                            attributes:nil
                                                 error:nil];
    return dir;
}

+ (NSURL *)settingsFileURL {
    return [[self storeDirectoryURL] URLByAppendingPathComponent:kSettingsFileName];
}

+ (NSURL *)keyFileURL {
    return [[self storeDirectoryURL] URLByAppendingPathComponent:kKeyFileName];
}

+ (NSDictionary *)settingsDictionary {
    NSDictionary *settings = [NSDictionary dictionaryWithContentsOfURL:[self settingsFileURL]];
    if (!settings) {
        // First launch: pull any legacy NSUserDefaults settings into the store.
        [self migrateLegacyDefaultsIfNeeded];
        settings = [NSDictionary dictionaryWithContentsOfURL:[self settingsFileURL]];
    }
    return settings ?: @{};
}

#pragma mark - Encryption key

+ (NSData *)loadOrCreateEncryptionKey {
    NSData *existing = [NSData dataWithContentsOfURL:[self keyFileURL]];
    if (existing.length == kKeySize) return existing;

    uint8_t bytes[kKeySize];
    if (SecRandomCopyBytes(kSecRandomDefault, kKeySize, bytes) != errSecSuccess) {
        return nil;
    }
    NSData *key = [NSData dataWithBytes:bytes length:kKeySize];
    NSError *error = nil;
    [key writeToURL:[self keyFileURL] options:NSDataWritingAtomic error:&error];
    if (error) return nil;
    [NSFileManager.defaultManager
        setAttributes:@{NSFilePosixPermissions : @0600}
         ofItemAtPath:[self keyFileURL].path
                error:nil];
    return key;
}

#pragma mark - AES-256-CBC helpers

+ (nullable NSData *)encrypt:(NSData *)plain key:(NSData *)key {
    if (!plain || key.length != kKeySize) return nil;
    uint8_t iv[kIVSize];
    if (SecRandomCopyBytes(kSecRandomDefault, kIVSize, iv) != errSecSuccess) return nil;

    size_t capacity = plain.length + kCCBlockSizeAES128;
    NSMutableData *cipherData = [NSMutableData dataWithLength:capacity];
    size_t moved = 0;
    CCCryptorStatus status = CCCrypt(
        kCCEncrypt, kCCAlgorithmAES, kCCOptionPKCS7Padding,
        key.bytes, kKeySize, iv,
        plain.bytes, plain.length,
        cipherData.mutableBytes, capacity, &moved);
    if (status != kCCSuccess) return nil;
    cipherData.length = moved;

    NSMutableData *blob = [NSMutableData dataWithLength:kIVSize + moved];
    [blob replaceBytesInRange:NSMakeRange(0, kIVSize) withBytes:iv];
    [blob replaceBytesInRange:NSMakeRange(kIVSize, moved) withBytes:cipherData.bytes];
    return blob;
}

+ (nullable NSData *)decrypt:(NSData *)blob key:(NSData *)key {
    if (!blob || blob.length <= kIVSize || key.length != kKeySize) return nil;
    uint8_t iv[kIVSize];
    [blob getBytes:iv length:kIVSize];
    NSData *cipher = [blob subdataWithRange:NSMakeRange(kIVSize, blob.length - kIVSize)];

    size_t capacity = cipher.length + kCCBlockSizeAES128;
    NSMutableData *plain = [NSMutableData dataWithLength:capacity];
    size_t moved = 0;
    CCCryptorStatus status = CCCrypt(
        kCCDecrypt, kCCAlgorithmAES, kCCOptionPKCS7Padding,
        key.bytes, kKeySize, iv,
        cipher.bytes, cipher.length,
        plain.mutableBytes, capacity, &moved);
    if (status != kCCSuccess) return nil;
    plain.length = moved;
    return plain;
}

#pragma mark - One-time migration

+ (void)migrateLegacyDefaultsIfNeeded {
    NSUserDefaults *defaults = [NSUserDefaults standardUserDefaults];
    if ([defaults objectForKey:kLegacyProviderKey] == nil &&
        [defaults objectForKey:kLegacyTargetKey] == nil &&
        [defaults stringForKey:kLegacyApiKeyKey] == nil) {
        return; // nothing legacy to migrate
    }
    NSInteger provider = [defaults integerForKey:kLegacyProviderKey];
    NSInteger target = [defaults integerForKey:kLegacyTargetKey];
    NSString *apiKey = [defaults stringForKey:kLegacyApiKeyKey] ?: @"";
    [self saveProviderIndex:provider targetIndex:target apiKey:apiKey];
    [defaults removeObjectForKey:kLegacyProviderKey];
    [defaults removeObjectForKey:kLegacyTargetKey];
    [defaults removeObjectForKey:kLegacyApiKeyKey];
}

@end