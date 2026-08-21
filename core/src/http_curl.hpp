#pragma once

#include <functional>
#include <string>

namespace translate {

/* POST application/json to `url` with `Bearer <bearer_token>` auth.
   Returns the raw response body on success (even for non-2xx, so the caller
   can surface the provider's error message). */
bool http_post_json(const std::string &url,
                    const std::string &bearer_token,
                    const std::string &json_body,
                    std::string &response_out,
                    std::string &error_out);

/* SSE transport for streaming chat completions. on_data() is invoked
   synchronously on the calling thread (from within curl_easy_perform) once per
   SSE `data:` payload, delivered verbatim (not parsed). For OpenAI-compatible
   endpoints each payload is a JSON object; the sentinel "[DONE]" is delivered
   as a payload too, so the caller owns the semantics. Handles `\r\n`/`\n` line
   endings, `data:` with or without a space, lines split across TCP reads, and
   an unterminated trailing event. If the response contains no SSE framing at
   all (a provider that ignores "stream": true), the entire body is delivered
   as one on_data invocation.

   total_timeout_seconds is libcurl's total operation timeout (connect + upload
   + generation + download); the streaming stall guard (LOW_SPEED) and connect
   timeout are configured internally here.
   On CURLE_OK returns true and sets *http_status_out (optional). On transport
   failure returns false with error_out set. */
using SseDataCallback = std::function<void(const std::string &data)>;
bool http_post_sse(const std::string &url,
                   const std::string &bearer_token,
                   const std::string &json_body,
                   long total_timeout_seconds,
                   SseDataCallback on_data,
                   std::string &error_out,
                   long *http_status_out = nullptr);

} // namespace translate
