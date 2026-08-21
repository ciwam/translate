#include "normalize_icu.hpp"

#include <unicode/translit.h>
#include <unicode/unistr.h>
#include <unicode/utrans.h>

#include <memory>

namespace translate {

std::string normalize_if_chinese(const std::string &utf8, Target target) {
    if (target != Target::Chinese) {
        return utf8;
    }

    UErrorCode status = U_ZERO_ERROR;
    icu::UnicodeString id("Hant-Hans");
    std::unique_ptr<icu::Transliterator> tr(
        icu::Transliterator::createInstance(id, UTRANS_FORWARD, status));
    if (U_FAILURE(status) || !tr) {
        return utf8; // ICU unavailable: keep model output as-is.
    }

    icu::UnicodeString u = icu::UnicodeString::fromUTF8(utf8);
    tr->transliterate(u);
    std::string out;
    u.toUTF8String(out);
    return out;
}

} // namespace translate
