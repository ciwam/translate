#pragma once

#include <functional>
#include <memory>
#include <string>

#include "translator.hpp"

namespace translate {

/* A fully-prepared model request. The pipeline owns prompt construction (target
   language, simplified-Chinese constraints, terminology dictionary) so the
   model adapter is transport-only and knows nothing about languages. The text
   never contains technical tokens in splice mode — structure is preserved by
   the pipeline, never by the model. */
struct TranslateRequest {
    std::string api_key;
    std::string system_prompt;
    std::string user_prompt;
};

/* Model adapter layer (设计：模型可切换). Implementations talk to a concrete
   model backend (OpenAI-compatible REST today; Ollama / Apple Neural Engine
   later). Swapping the model only changes which ITranslator the factory
   returns — the pipeline is unchanged. */
class ITranslator {
public:
    virtual ~ITranslator() = default;
    /* Blocking. Invokes `delta` (if non-null) with the translation accumulated
       so far (valid UTF-8 prefix, overwrite semantics) and `done` exactly once
       with the final result. Delivered on the calling thread. */
    virtual void translate(const TranslateRequest &req,
                           DeltaCallback delta,
                           Callback done) = 0;
};

/* Return the adapter for a provider enum value. The provider enum order mirrors
   the adapter's endpoint/model table. */
std::shared_ptr<ITranslator> create_translator(Provider provider);

} // namespace translate