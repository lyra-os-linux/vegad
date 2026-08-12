// Package i18n localizes text that crosses vegad's D-Bus boundary.
// Technical logs and errors must not use this package.
package i18n

import (
	"embed"
	"encoding/json"
	"regexp"
	"strings"
)

const DefaultLocale = "en-US"

var supported = map[string]string{
	"en-us": "en-US",
	"pt-br": "pt-BR",
	"es-es": "es-ES",
	"zh-cn": "zh-CN",
}

var localePattern = regexp.MustCompile(`^[A-Za-z]{2}[-_][A-Za-z]{2}$`)

// Normalize validates a caller-provided locale without consulting the daemon
// process environment. Missing, malformed, and unsupported locales fall back
// to en-US. Encoding suffixes and modifiers are safely discarded first.
func Normalize(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.IndexByte(value, '@'); i >= 0 {
		value = value[:i]
	}
	if i := strings.IndexByte(value, '.'); i >= 0 {
		value = value[:i]
	}
	if !localePattern.MatchString(value) {
		return DefaultLocale
	}
	value = strings.ReplaceAll(value, "_", "-")
	if normalized, ok := supported[strings.ToLower(value)]; ok {
		return normalized
	}
	return DefaultLocale
}

//go:embed catalog/*.json
var catalogFiles embed.FS

var catalogs = loadCatalogs()

func loadCatalogs() map[string]map[string]string {
	result := make(map[string]map[string]string, len(supported))
	for _, locale := range supported {
		data, err := catalogFiles.ReadFile("catalog/" + locale + ".json")
		if err != nil {
			continue
		}
		messages := map[string]string{}
		if json.Unmarshal(data, &messages) == nil {
			result[locale] = messages
		}
	}
	return result
}

// T returns a localized user-facing message. A missing key falls back to the
// English catalog, and a key missing there is returned verbatim so failures
// stay deterministic and never depend on the service locale.
func T(locale, key string) string {
	locale = Normalize(locale)
	if message, ok := catalogs[locale][key]; ok {
		return message
	}
	if message, ok := catalogs[DefaultLocale][key]; ok {
		return message
	}
	return key
}
