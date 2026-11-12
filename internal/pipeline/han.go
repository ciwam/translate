package pipeline

import "github.com/longbridgeapp/opencc"

var defaultConv *opencc.OpenCC

// DefaultInit initializes the OpenCC converter (Traditional -> Simplified).
func DefaultInit() error {
	conv, err := opencc.New("t2s")
	if err != nil {
		return err
	}
	defaultConv = conv
	return nil
}

// normalizeIfChinese converts Hant to Hans when the target is Simplified
// Chinese. Port of normalize_icu.cpp via the pure-Go OpenCC.
func normalizeIfChinese(target int, s string) string {
	if target != Chinese || defaultConv == nil {
		return s
	}
	out, err := defaultConv.Convert(s)
	if err != nil {
		return s
	}
	return out
}

// Chinese is the pipeline's own copy of the target enum value (1), so this
// package does not import provider everywhere. Must match provider.Chinese.
const Chinese = 1
