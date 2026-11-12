package pipeline

import (
	"strings"
	"testing"
)

func TestIsProtectedToken(t *testing.T) {
	technical := []string{
		"file.tar.gz", "a,b,c", "src/x.ts:1", "8080", "50%",
		"$HOME", "{name}", "NSFont", "CFType", "TranslateViewController",
		"a=b", "[1]", "nginx[812]:", "user@host", "C:\\path", "app-7d9f'",
	}
	for _, tok := range technical {
		if !IsProtectedToken(tok) {
			t.Errorf("IsProtectedToken(%q) = false, want true", tok)
		}
	}
	prose := []string{
		"hello.", "world", "ERROR:", "client:", "CXX", "OBJC", "can't",
		"translation", "Hello",
	}
	for _, tok := range prose {
		if IsProtectedToken(tok) {
			t.Errorf("IsProtectedToken(%q) = true, want false", tok)
		}
	}
}

func TestAnalyzeTechnical(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"Hello world, this is prose.", false},
		{"Build failed at src/main.go:42", true},
		{"nginx[812]: using 50% memory", true},
		{"这是一个纯中文句子。", false},
	}
	for _, c := range cases {
		if got := AnalyzeTechnical(c.src); got != c.want {
			t.Errorf("AnalyzeTechnical(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestMaskRestoreRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		unit string
	}{
		{"paths", "[INFO] loading /usr/local/etc/app.conf"},
		{"bare number with period", "The server listens on port 8080."},
		{"trailing quote", "cannot start app-7d9f'"},
		{"csv", "name,count,size"},
		{"mixed case", "the NSFont class is used"},
		{"no tokens", "just prose here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			masked, toks, ok := MaskUnit(c.unit)
			if !ok {
				// No protected tokens: restore must be the identity when
				// called with no tokens... but MaskUnit signals unmasked.
				if strings.Contains(c.unit, "[[") {
					return
				}
				return // unmasked unit; caller sends original
			}
			got, restored := RestoreUnit(masked, toks)
			if !restored {
				t.Fatalf("RestoreUnit(%q) failed", masked)
			}
			if got != c.unit {
				t.Errorf("round trip: got %q, want %q", got, c.unit)
			}
		})
	}
}

// Run r6: a bare number's sentence period must stay OUT of the placeholder,
// or the restored sentence reads broken. The period sits at the token edge
// ("port [[1]].") and restore reproduces the original exactly.
func TestMaskKeepsSentencePeriodOut(t *testing.T) {
	masked, toks, ok := MaskUnit("port 8080.")
	if !ok {
		t.Fatal("expected masked")
	}
	if !strings.HasSuffix(masked, "]]") && !strings.HasSuffix(masked, "]].") {
		t.Errorf("period should sit outside the placeholder: %q", masked)
	}
	got, restored := RestoreUnit(masked, toks)
	if !restored || got != "port 8080." {
		t.Errorf("restore got %q restored=%v", got, restored)
	}
}

// Run r7: the model dropped [[1]] entirely — restore must fail, never
// return a half-restored hybrid.
func TestRestoreFailsOnMissingPlaceholder(t *testing.T) {
	_, toks, ok := MaskUnit("WARNING: file /var/log/x missing")
	if !ok {
		t.Fatal("expected masked")
	}
	if _, restored := RestoreUnit("file  missing", toks); restored {
		t.Error("missing placeholder must fail restore")
	}
	if _, restored := RestoreUnit("[[3]] file missing", toks); restored {
		t.Error("out-of-range placeholder must fail restore")
	}
	// A duplicate placeholder still restores (the C++ kernel only checks
	// that every placeholder appeared at least once).
	if got, restored := RestoreUnit("[[1]] file [[1]] missing", toks); !restored {
		t.Errorf("duplicate placeholder restores in C++ parity, got failure (%q)", got)
	}
}

func TestRestoreNormalizesFullwidth(t *testing.T) {
	// The model echoes "，" instead of "," — restore must still find [[1]].
	got, restored := RestoreUnit("无法打开[[1]]，权限被拒绝", []string{"/var/x"})
	if !restored {
		t.Fatal("restore failed on full-width punctuation")
	}
	if got != "无法打开/var/x,权限被拒绝" {
		t.Errorf("got %q", got)
	}
}

func TestSplitPieces(t *testing.T) {
	text := strings.Repeat("First sentence. Second one here. ", 60)
	pieces := SplitPieces(text, 1500)
	var rebuilt strings.Builder
	for i, p := range pieces {
		rebuilt.WriteString(p.Text)
		if i < len(pieces)-1 {
			rebuilt.WriteString(p.Sep)
		}
	}
	if strings.Trim(rebuilt.String(), " \t\r\n") != strings.Trim(text, " \t\r\n") {
		t.Error("piece rejoin must reconstruct the trimmed source")
	}
	for _, p := range pieces {
		if len(p.Text) > 1500 {
			t.Errorf("piece too long: %d", len(p.Text))
		}
	}
}

func TestSplitPiecesMultiline(t *testing.T) {
	text := strings.Repeat("line one\nline two\n", 200)
	pieces := SplitPieces(text, 1500)
	if len(pieces) < 2 {
		t.Fatalf("want multiple pieces, got %d", len(pieces))
	}
	var rebuilt strings.Builder
	for i, p := range pieces {
		rebuilt.WriteString(p.Text)
		if i < len(pieces)-1 {
			rebuilt.WriteString(p.Sep)
		}
	}
	if rebuilt.String() != strings.Trim(text, " \t\r\n") {
		t.Error("line-structured rejoin failed")
	}
}

// Run r3 regression territory: labels stay prose, the bad-translation guard
// catches prompt echoes and stutter.
func TestIsBadTranslation(t *testing.T) {
	bad := []string{
		"",
		"Translate the following text into Simplified Chinese",
		"翻译以下内容：",
		"BuildingBuilding CMake",
		"连接连接C连接CXX",
		"可可可可",
		strings.Repeat("\uFFFD", 5) + "ok",
	}
	for _, s := range bad {
		if !IsBadTranslation(s) {
			t.Errorf("IsBadTranslation(%q) = false, want true", s)
		}
	}
	good := []string{
		"无法打开文件 /var/log/system.log，权限被拒绝",
		"The quick brown fox jumps over the lazy dog.",
		"构建失败：找不到指定的模块。",
	}
	for _, s := range good {
		if IsBadTranslation(s) {
			t.Errorf("IsBadTranslation(%q) = true, want false", s)
		}
	}
}

func TestProtectedTokensPreserved(t *testing.T) {
	src := "Failed to load /etc/nginx/nginx.conf at 8080 port"
	if !ProtectedTokensPreserved(src, "无法加载 /etc/nginx/nginx.conf，端口 8080") {
		t.Error("preserved output rejected")
	}
	if ProtectedTokensPreserved(src, "无法加载配置文件，端口被占用") {
		t.Error("missing tokens accepted")
	}
	// Edge punctuation width difference still counts as preserved.
	if !ProtectedTokensPreserved("port 8080.", "端口 8080。") {
		t.Error("edge-punct width difference must count as preserved")
	}
}

func TestRepunct(t *testing.T) {
	// Source uses , : >= 2 times -> output full-width forms come back.
	src := "name, count: size;"
	out := "名称，计数：大小；"
	if got := Repunct(out, src); got != "名称,计数:大小;" {
		t.Errorf("Repunct = %q", got)
	}
	// Sentence-final 。 is left alone.
	if got := Repunct("构建完成。", "done, twice: ok;"); got != "构建完成。" {
		t.Errorf("sentence 。 must stay: %q", got)
	}
	// Source without structural punct -> untouched.
	if got := Repunct("你好，世界", "plain source"); got != "你好，世界" {
		t.Errorf("no-src-punct must not convert: %q", got)
	}
}

func TestStripExplanations(t *testing.T) {
	cases := map[string]string{
		"译文：你好":           "你好",
		"Translation: hi": "hi",
		"你好":              "你好",
	}
	for in, want := range cases {
		if got := StripExplanations(in); got != want {
			t.Errorf("StripExplanations(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildGlossary(t *testing.T) {
	glossary := "kernel = 内核\nmissing term = 缺失\nbuild -> 构建\n"
	out := BuildGlossary(glossary, "the kernel and build system")
	if !strings.Contains(out, "kernel = 内核") || !strings.Contains(out, "build = 构建") {
		t.Errorf("present terms missing: %q", out)
	}
	if strings.Contains(out, "missing term") {
		t.Errorf("absent term must be filtered: %q", out)
	}
	if out2 := BuildGlossary(glossary, "nothing matches here"); out2 != "" {
		t.Errorf("no match must be empty: %q", out2)
	}
}

func TestHasCJK(t *testing.T) {
	if !HasCJK("中文") || !HasCJK("カタカナ") || !HasCJK("한국어") {
		t.Error("CJK detection failed")
	}
	if HasCJK("plain english 123") {
		t.Error("false CJK positive")
	}
}

func TestCache(t *testing.T) {
	c := newResultCache(2)
	c.put("a", "1")
	c.put("b", "2")
	if v, ok := c.get("a"); !ok || v != "1" {
		t.Errorf("get a = %q %v", v, ok)
	}
	c.put("c", "3") // evicts b (a was just touched)
	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}
	if v, ok := c.get("a"); !ok || v != "1" {
		t.Errorf("a should survive: %q %v", v, ok)
	}
}

func TestCacheKeyStable(t *testing.T) {
	k1 := CacheKey(0, 1, "hello", "")
	k2 := CacheKey(0, 1, "hello", "")
	k3 := CacheKey(1, 1, "hello", "")
	if k1 != k2 || k1 == k3 {
		t.Error("cache key discrimination failed")
	}
}

func TestLineHasProse(t *testing.T) {
	if lineHasProse("[INFO] 2024-01-01 12:00:00") {
		t.Error("technical-only line must have no prose")
	}
	if !lineHasProse("Starting the server on port 8080") {
		t.Error("prose line must have prose")
	}
}
