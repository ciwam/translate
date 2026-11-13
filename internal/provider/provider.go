// Package provider defines the model adapter layer: the translation
// pipeline knows languages, the adapter knows transport. A port of
// core/include/translate/itranslator.hpp.
package provider

import "context"

// Provider selects an OpenAI-compatible endpoint. Order mirrors the C++
// enum and the endpoint table in openai.go.
type Provider int

const (
	Zhipu Provider = iota
	SiliconFlow
)

// Target names a target language. Order mirrors translate.h.
type Target int

const (
	English Target = iota
	Chinese
	Japanese
	Korean
	French
	German
	Spanish

	// AutoBidirectional is the "中英互译" pseudo-target: the pipeline
	// detects the input language per call — CJK input goes to English,
	// everything else to Simplified Chinese — instead of a fixed target.
	AutoBidirectional Target = -1
)

// Request is a fully-prepared model request. The pipeline owns prompt
// construction so the adapter is transport-only.
type Request struct {
	APIKey       string
	SystemPrompt string
	UserPrompt   string
}

// Result carries either Text or Error, never both.
type Result struct {
	OK    bool
	Text  string
	Error string
}

// Event is one item on the stream returned by Translate. Delta carries the
// ENTIRE result so far (valid UTF-8, overwrite semantics); Done and Error
// are terminal and mutually exclusive.
type Event struct {
	Kind  EventKind
	Text  string
	Error string
}

type EventKind int

const (
	EventDelta EventKind = iota
	EventDone
	EventError
)

// Translator is a blocking streaming model adapter.
type Translator interface {
	// Translate streams events on the returned channel and closes it after
	// exactly one Done or Error. Canceling ctx aborts the request.
	Translate(ctx context.Context, req Request) (<-chan Event, context.CancelFunc)
}

// ProviderCount and display-name helpers mirror the C UI helpers in
// translate.h so the UI and the debug CLI share one table.
func ProviderCount() int { return len(endpoints) }

func ProviderDisplayName(i int) string {
	if i < 0 || i >= len(endpoints) {
		return ""
	}
	return endpoints[i].DisplayName
}

func TargetCount() int { return len(targetNames) }

func TargetDisplayName(i int) string {
	if i < 0 || i >= len(targetNames) {
		return ""
	}
	return targetNames[i].Display
}

// TargetCode is the lowercase code used by the debug CLI ("en", "zh", ...).
func TargetCode(i int) string {
	if i < 0 || i >= len(targetNames) {
		return ""
	}
	return targetNames[i].Code
}

func DefaultTargetIndex() int { return int(AutoBidirectional) }

var targetNames = []struct{ Code, Display string }{
	{"en", "English"},
	{"zh", "简体中文"},
	{"ja", "日本語"},
	{"ko", "한국어"},
	{"fr", "Français"},
	{"de", "Deutsch"},
	{"es", "Español"},
}
