#pragma once

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

} // namespace translate
