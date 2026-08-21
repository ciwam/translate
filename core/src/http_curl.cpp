#include "http_curl.hpp"

#include <curl/curl.h>

#include <string>
#include <utility>

namespace translate {
namespace {

struct WriteBuf {
    std::string data;
};

extern "C" size_t write_cb(void *contents, size_t size, size_t nmemb, void *userp) {
    auto *buf = static_cast<WriteBuf *>(userp);
    size_t total = size * nmemb;
    buf->data.append(static_cast<const char *>(contents), total);
    return total;
}

// --- SSE decoder: turns a byte stream into one on_data() per event ---------
class SseDecoder {
public:
    explicit SseDecoder(std::function<void(const std::string &)> on_data)
        : on_data_(std::move(on_data)) {}

    bool any_dispatched() const { return dispatched_; }
    void deliver_fallback(const std::string &body) { on_data_(body); }

    void feed(const char *data, size_t len) {
        pending_.append(data, len);
        size_t pos = 0;
        for (;;) {
            size_t nl = pending_.find('\n', pos);
            if (nl == std::string::npos) break; // incomplete final line stays pending
            process_line(pending_, pos, nl);
            pos = nl + 1;
        }
        pending_.erase(0, pos);
    }

    // Flush a trailing SSE event whose terminating blank line never arrived.
    void finish() {
        if (!event_.empty()) {
            dispatch(event_);
            event_.clear();
        }
        pending_.clear();
    }

private:
    void process_line(const std::string &buf, size_t b, size_t e) {
        if (e == b) { // blank line: end of event
            if (!event_.empty()) {
                dispatch(event_);
                event_.clear();
            }
            return;
        }
        if (buf[e - 1] == '\r') { // CRLF: strip \r, handle "\r" as blank line
            if (e - 1 == b) {
                if (!event_.empty()) {
                    dispatch(event_);
                    event_.clear();
                }
                return;
            }
            --e;
        }
        if (buf.compare(b, 5, "data:") == 0) {
            size_t v = b + 5;
            if (v < e && buf[v] == ' ') ++v; // optional single space after colon
            if (!event_.empty()) event_ += '\n'; // multi-line data joined with '\n'
            event_.append(buf, v, e - v);
        }
        // Other SSE fields (event:, id:, retry:, comments) are ignored.
    }

    void dispatch(const std::string &payload) { dispatched_ = true; on_data_(payload); }

    std::function<void(const std::string &)> on_data_;
    std::string pending_; // bytes not yet split into complete lines
    std::string event_;   // data payload of the event being read
    bool dispatched_ = false;
};

struct SseWriteCtx {
    std::string body; // full raw response, kept for the non-SSE fallback
    SseDecoder *decoder = nullptr;
};

extern "C" size_t sse_write_cb(void *contents, size_t size, size_t nmemb, void *userp) {
    auto *ctx = static_cast<SseWriteCtx *>(userp);
    size_t total = size * nmemb;
    ctx->body.append(static_cast<const char *>(contents), total);
    ctx->decoder->feed(static_cast<const char *>(contents), total);
    return total;
}

} // namespace

bool http_post_json(const std::string &url,
                    const std::string &bearer_token,
                    const std::string &json_body,
                    std::string &response_out,
                    std::string &error_out) {
    CURL *curl = curl_easy_init();
    if (!curl) {
        error_out = "Failed to initialize HTTP client";
        return false;
    }

    WriteBuf buf{};
    struct curl_slist *headers = nullptr;
    headers = curl_slist_append(headers, "Content-Type: application/json");
    std::string auth = "Authorization: Bearer " + bearer_token;
    headers = curl_slist_append(headers, auth.c_str());

    curl_easy_setopt(curl, CURLOPT_URL, url.c_str());
    curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(curl, CURLOPT_POST, 1L);
    curl_easy_setopt(curl, CURLOPT_POSTFIELDS, json_body.c_str());
    curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, static_cast<long>(json_body.size()));
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, write_cb);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &buf);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 60L);
    curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L);

    CURLcode res = curl_easy_perform(curl);
    curl_slist_free_all(headers);
    curl_easy_cleanup(curl);

    if (res != CURLE_OK) {
        error_out = std::string("Network error: ") + curl_easy_strerror(res);
        return false;
    }

    response_out = buf.data;
    return true;
}

bool http_post_sse(const std::string &url,
                   const std::string &bearer_token,
                   const std::string &json_body,
                   long total_timeout_seconds,
                   SseDataCallback on_data,
                   std::string &error_out,
                   long *http_status_out) {
    if (!on_data) {
        error_out = "No SSE data callback provided";
        return false;
    }
    CURL *curl = curl_easy_init();
    if (!curl) {
        error_out = "Failed to initialize HTTP client";
        return false;
    }

    SseWriteCtx sctx{};
    struct curl_slist *headers = nullptr;
    headers = curl_slist_append(headers, "Content-Type: application/json");
    headers = curl_slist_append(headers, "Accept: text/event-stream");
    std::string auth = "Authorization: Bearer " + bearer_token;
    headers = curl_slist_append(headers, auth.c_str());

    curl_easy_setopt(curl, CURLOPT_URL, url.c_str());
    curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
    curl_easy_setopt(curl, CURLOPT_POST, 1L);
    curl_easy_setopt(curl, CURLOPT_POSTFIELDS, json_body.c_str());
    curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, static_cast<long>(json_body.size()));
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, sse_write_cb);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, &sctx);
    // Streaming timeout strategy: the connect timeout fails fast on a dead
    // host; the low-speed rule is the primary stall guard (streaming keeps
    // bytes flowing, so silence means a hung connection); the total is a slow-
    // but-alive backstop computed by the caller from input size.
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, total_timeout_seconds);
    curl_easy_setopt(curl, CURLOPT_CONNECTTIMEOUT, 15L);
    curl_easy_setopt(curl, CURLOPT_LOW_SPEED_LIMIT, 1L);
    curl_easy_setopt(curl, CURLOPT_LOW_SPEED_TIME, 90L);
    curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L);

    SseDecoder decoder(std::move(on_data));
    sctx.decoder = &decoder;

    CURLcode res = curl_easy_perform(curl);
    long http_status = 0;
    curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &http_status);
    curl_slist_free_all(headers);
    curl_easy_cleanup(curl);

    if (res != CURLE_OK) {
        error_out = std::string("Network error: ") + curl_easy_strerror(res);
        return false;
    }
    if (http_status_out) *http_status_out = http_status;

    decoder.finish();

    // Provider ignored "stream": true and returned a plain (non-SSE) body.
    // No event was dispatched above, so deliver the whole body as one payload.
    if (!decoder.any_dispatched() && !sctx.body.empty()) {
        decoder.deliver_fallback(sctx.body);
    }
    return true;
}

} // namespace translate
