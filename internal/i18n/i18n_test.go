package i18n

import "testing"

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"": DefaultLocale, "en_US.UTF-8": "en-US", "pt_BR.UTF-8": "pt-BR",
		"es_ES.UTF-8@custom": "es-ES", "zh_CN.UTF-8": "en-US",
		"de_DE.UTF-8": DefaultLocale, "../../pt_BR": DefaultLocale,
		"pt": DefaultLocale, "pt_BR_extra": DefaultLocale,
	}
	for input, want := range tests {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCatalogsAndFallback(t *testing.T) {
	for _, locale := range []string{"en-US", "pt-BR", "es-ES"} {
		if got := T(locale, "common.completed"); got == "" || got == "common.completed" {
			t.Errorf("catalog %s is missing common.completed", locale)
		}
	}
	if got := T("unknown", "common.completed"); got != "Completed" {
		t.Fatalf("unknown locale did not fall back to English: %q", got)
	}
	if got := T("pt-BR", "missing.key"); got != "missing.key" {
		t.Fatalf("missing key fallback = %q", got)
	}
}
