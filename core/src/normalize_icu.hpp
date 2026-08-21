#pragma once

#include <string>
#include "translator.hpp"

namespace translate {

/* Final fallback: when the model ignores the prompt and emits Traditional
   Chinese for a Simplified-Chinese target, convert it locally via ICU
   (Hant -> Hans). No-op for every other target language. */
std::string normalize_if_chinese(const std::string &utf8, Target target);

} // namespace translate
