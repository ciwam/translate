#ifndef TRANSLATE_TRANSLATE_H
#define TRANSLATE_TRANSLATE_H

#ifdef __cplusplus
extern "C" {
#endif

/* Provider selection (mirrors the configured AI models). */
typedef enum {
    TRANSLATE_PROVIDER_SILICONFLOW = 0, /* DeepSeek-V3.2 */
    TRANSLATE_PROVIDER_ZHIPU = 1,       /* GLM-4-Flash */
    TRANSLATE_PROVIDER_SILICONFLOW_QWEN = 2 /* Qwen2.5-7B-Instruct */
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

/* Streaming events delivered to translate_stream_callback_t. */
typedef enum {
    TRANSLATE_EVENT_DELTA = 0, /* `text`: translation accumulated so far, a valid
                                  UTF-8 prefix of the final result (never split
                                  mid-character; heap-allocated, free with
                                  translate_free). Not terminal. */
    TRANSLATE_EVENT_DONE  = 1, /* `text`: final complete translation, trimmed and
                                  normalized (free with translate_free). Terminal. */
    TRANSLATE_EVENT_ERROR = 2  /* `error`: message (free with translate_free). Terminal. */
} translate_event_t;

typedef void (*translate_stream_callback_t)(translate_event_t event,
                                            const char *text,
                                            const char *error,
                                            void *userdata);

/* Streaming translate. DELTA events allow progressive rendering; each DELTA
   `text` is the ENTIRE result so far, so a front-end overwrites its display.
   Exactly one DONE or ERROR is delivered (never both, never extra). Synchronous
   (blocks until the stream ends); callback invoked on the calling thread. */
void translate_run_stream(translate_provider_t provider,
                          translate_target_t target,
                          const char *api_key,
                          const char *text,
                          translate_stream_callback_t callback,
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
