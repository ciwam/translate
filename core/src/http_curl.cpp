#include "http_curl.hpp"

#include <curl/curl.h>

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

} // namespace translate
