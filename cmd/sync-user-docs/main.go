package main

import (
	"fmt"
	"os"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/database"
	"tokenhub-server/internal/pkg/logger"
)

func main() {
	cfgFile := "configs/config.yaml"
	if envCfg := os.Getenv("CONFIG_FILE"); envCfg != "" {
		cfgFile = envCfg
	}
	if err := config.Load(cfgFile); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if err := logger.Init(logger.Config{
		Level:      config.Global.Log.Level,
		Dir:        config.Global.Log.Dir,
		MaxSize:    config.Global.Log.MaxSize,
		MaxAge:     config.Global.Log.MaxAge,
		MaxBackups: config.Global.Log.MaxBackups,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	if err := database.Init(config.Global.Database, logger.L); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	database.RunSeedDocs(database.DB)
	database.RunCleanPlaceholderDocCategories(database.DB)
	database.RunSeedCustomParamDocs(database.DB)
	database.RunSeedModelAPIDocs(database.DB)
	fmt.Println("user docs synchronized")
}
