package i18n

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// Bundle 全局i18n国际化包实例
var Bundle *i18n.Bundle

// Config i18n国际化配置
type Config struct {
	DefaultLang string
	LocalesDir  string
}

// Init 加载所有语言JSON文件到全局Bundle
func Init(cfg Config) error {
	if cfg.DefaultLang == "" {
		cfg.DefaultLang = "en"
	}
	if cfg.LocalesDir == "" {
		cfg.LocalesDir = "./internal/pkg/i18n/locales"
	}

	tag, err := language.Parse(cfg.DefaultLang)
	if err != nil {
		tag = language.English
	}
	Bundle = i18n.NewBundle(tag)
	Bundle.RegisterUnmarshalFunc("json", json.Unmarshal)

	entries, err := os.ReadDir(cfg.LocalesDir)
	if err != nil {
		return fmt.Errorf("failed to read locales dir %s: %w", cfg.LocalesDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(cfg.LocalesDir, entry.Name())
		if _, err := Bundle.LoadMessageFile(path); err != nil {
			return fmt.Errorf("failed to load locale file %s: %w", path, err)
		}
	}
	return nil
}

// Translate 根据指定语言和消息键返回本地化文本
func Translate(lang, msgID string) string {
	if Bundle == nil || msgID == "" {
		return msgID
	}
	localizer := i18n.NewLocalizer(Bundle, lang)
	msg, err := localizer.Localize(&i18n.LocalizeConfig{MessageID: msgID})
	if err != nil || msg == "" {
		return msgID
	}
	return msg
}

// TranslateWithData 返回带模板数据的本地化消息
func TranslateWithData(lang, msgID string, data map[string]interface{}) string {
	if Bundle == nil || msgID == "" {
		return msgID
	}
	localizer := i18n.NewLocalizer(Bundle, lang)
	msg, err := localizer.Localize(&i18n.LocalizeConfig{
		MessageID:    msgID,
		TemplateData: data,
	})
	if err != nil || msg == "" {
		return msgID
	}
	return msg
}

// NewTranslator 返回指定语言的翻译函数
func NewTranslator(lang string) func(string) string {
	return func(msgID string) string {
		return Translate(lang, msgID)
	}
}
