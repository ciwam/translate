#ifndef TRANSLATE_TRANSLATE_H
#define TRANSLATE_TRANSLATE_H

#ifdef __cplusplus
extern "C" {
#endif

/* Provider selection (mirrors the configured AI models). */
typedef enum {
    TRANSLATE_PROVIDER_SILICONFLOW = 0, /* Qwen2.5-7B-Instruct */
    TRANSLATE_PROVIDER_ZHIPU = 1        /* GLM-4-Flash */
} translate_provider_t;

/* Target language selection. */
typedef enum {
    TRANSLATE_TARGET_ENGLISH = 0,
    TRANSLATE_TARGET_CHINESE = 1, /* Simplified Chinese */
    TRANSLATE_TARGET_JAPANESE = 2,
    TRANSLATE_TARGET_KOREAN = 3,
    TRANSLATE_TARGET_FRENCH = 4,
    TRANSLATE_TARGET_GERMAN = 5,
    TRANSLATE_TARGET_SPANISH = 6
} translate_target_t;

/* Completion callback.
   - ok == 1: success, `text` holds the UTF-8 translation (heap-allocated,
     free it with translate_free).
   - ok == 0: failure, `error` holds the UTF-8 message (free it with
     translate_free).
   Exactly one of text/error is non-NULL. `userdata` is the pointer passed to
   translate_run. The callback is invoked on the calling thread because
   translate_run is synchronous. */
typedef void (*translate_callback_t)(int ok,
                                     const char *text,
                                     const char *error,
                                     void *userdata);

/* Synchronous translate. Blocks until the request completes. */
void translate_run(translate_provider_t provider,
                   translate_target_t target,
                   const char *api_key,
                   const char *text,
                   translate_callback_t callback,
                   void *userdata);

/* Free a string previously returned through the callback. */
void translate_free(char *ptr);

/* ---- UI helpers (return static, read-only strings; do NOT free) ---- */
int         translate_provider_count(void);
const char *translate_provider_display_name(int index);
int         translate_target_count(void);
const char *translate_target_display_name(int index);
int         translate_default_target_index(void); /* Simplified Chinese */

#ifdef __cplusplus
}
#endif

#endif /* TRANSLATE_TRANSLATE_H */
