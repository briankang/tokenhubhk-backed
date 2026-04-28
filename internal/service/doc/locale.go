package doc

import "strings"

func NormalizeLocale(locale string) string {
	v := strings.ToLower(strings.TrimSpace(locale))
	v = strings.ReplaceAll(v, "_", "-")
	switch {
	case v == "en" || strings.HasPrefix(v, "en-"):
		return "en"
	case v == "zh-tw" || v == "zh-hk" || v == "zh-hant":
		return "zh-TW"
	default:
		return "zh"
	}
}
