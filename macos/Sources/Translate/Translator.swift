import Foundation
import CoreFoundation

// MARK: - 可配置的翻译模型（Providers）

enum TranslationProvider: String, CaseIterable {
    case siliconflow
    case zhipu

    /// OpenAI 兼容的 chat/completions 接口地址
    var endpoint: String {
        switch self {
        case .siliconflow: return "https://api.siliconflow.cn/v1/chat/completions"
        case .zhipu:       return "https://open.bigmodel.cn/api/paas/v4/chat/completions"
        }
    }

    /// 该服务商对应的模型名
    var modelName: String {
        switch self {
        case .siliconflow: return "Qwen/Qwen2.5-7B-Instruct"
        case .zhipu:       return "glm-4-flash"
        }
    }

    var displayName: String {
        switch self {
        case .siliconflow: return "硅基流动 · Qwen2.5-7B-Instruct"
        case .zhipu:       return "智谱 · GLM-4-Flash"
        }
    }
}

// MARK: - 目标语言

enum TargetLanguage: String, CaseIterable {
    case english, chinese, japanese, korean, french, german, spanish

    var displayName: String {
        switch self {
        case .english:  return "英语 English"
        case .chinese:  return "简体中文 Chinese"
        case .japanese: return "日本語 Japanese"
        case .korean:   return "한국어 Korean"
        case .french:   return "Français French"
        case .german:   return "Deutsch German"
        case .spanish:  return "Español Spanish"
        }
    }

    /// 写入 prompt 的英文语言名，便于模型稳定识别
    var promptName: String {
        switch self {
        case .english:  return "English"
        case .chinese:  return "Simplified Chinese"
        case .japanese: return "Japanese"
        case .korean:   return "Korean"
        case .french:   return "French"
        case .german:   return "German"
        case .spanish:  return "Spanish"
        }
    }
}

// MARK: - 响应解析结构

private struct ChatMessage: Codable {
    let role: String
    let content: String
}

private struct ChatChoice: Codable {
    let message: ChatMessage
}

private struct APIError: Codable {
    let message: String
}

private struct ChatResponse: Decodable {
    let choices: [ChatChoice]?
    let error: APIError?
}

// MARK: - 翻译器

final class Translator {
    private let session: URLSession

    init() {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 60
        cfg.waitsForConnectivity = false
        session = URLSession(configuration: cfg)
    }

    func translate(text: String,
                   provider: TranslationProvider,
                   apiKey: String,
                   target: TargetLanguage,
                   completion: @escaping (Result<String, Error>) -> Void) {
        let key = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !key.isEmpty else {
            completion(.failure(makeError("请先填写 API Key")))
            return
        }
        let src = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !src.isEmpty else {
            completion(.failure(makeError("Please enter text to translate")))
            return
        }
        guard let url = URL(string: provider.endpoint) else {
            completion(.failure(makeError("无效的接口地址")))
            return
        }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization")

        // 针对模型容易出现的两个问题强化提示：
        // 1. 英文单词/短词被原样返回（echo 原文）；
        // 2. 简体中文目标输出繁体字（如 蘋果 而不是 苹果）。
        let targetName: String
        let constraints: String
        switch target {
        case .chinese:
            targetName = "Mainland China Simplified Chinese (中国大陆简体字，禁止使用繁体字)"
            constraints = "Use only Mainland China standard simplified characters. Do NOT use traditional Chinese characters."
        case .english:
            targetName = "English"
            constraints = ""
        default:
            targetName = target.promptName
            constraints = ""
        }

        let system = "You are a professional translator. " +
                     "Translate the user's text exactly into the requested target language. " +
                     "Always provide a translation; never echo or return the original text unchanged. " +
                     "Output only the translated text, with no explanations, notes, examples, or quotation marks. " +
                     constraints
        let user = "Translate the following text into \(targetName). Provide only the translation:\n\n\(src)"
        let payload: [String: Any] = [
            "model": provider.modelName,
            "messages": [
                ["role": "system", "content": system],
                ["role": "user", "content": user]
            ],
            "temperature": 0.1,
            "stream": false
        ]

        do {
            request.httpBody = try JSONSerialization.data(withJSONObject: payload)
        } catch {
            completion(.failure(error))
            return
        }

        let task = session.dataTask(with: request) { [weak self] data, _, error in
            guard let self else { return }
            if let error {
                completion(.failure(self.makeError("网络错误：\(error.localizedDescription)")))
                return
            }
            guard let data else {
                completion(.failure(self.makeError("服务器未返回数据")))
                return
            }
            self.parse(data: data, target: target, completion: completion)
        }
        task.resume()
    }

    private func parse(data: Data, target: TargetLanguage, completion: @escaping (Result<String, Error>) -> Void) {
        if let decoded = try? JSONDecoder().decode(ChatResponse.self, from: data),
           let content = decoded.choices?.first?.message.content {
            let raw = content.trimmingCharacters(in: .whitespacesAndNewlines)
            completion(.success(self.normalizeIfChinese(raw, target: target)))
            return
        }
        if let err = try? JSONDecoder().decode(ChatResponse.self, from: data),
           let msg = err.error?.message {
            completion(.failure(makeError("接口错误：\(msg)")))
            return
        }
        let raw = String(data: data, encoding: .utf8) ?? ""
        completion(.failure(makeError("解析失败：\(String(raw.prefix(200)))")))
    }

    // 兜底：当模型不听话输出繁体时，本地统一转为简体中文。
    private func normalizeIfChinese(_ text: String, target: TargetLanguage) -> String {
        guard target == .chinese else { return text }
        let mutable = NSMutableString(string: text)
        CFStringTransform(mutable, nil, "Hant-Hans" as CFString, false)
        return mutable as String
    }

    private func makeError(_ message: String) -> Error {
        NSError(domain: "Translate", code: -1, userInfo: [NSLocalizedDescriptionKey: message])
    }
}
