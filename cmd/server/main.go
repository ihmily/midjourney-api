package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/trae/midjourney-api/internal/app"
	"github.com/trae/midjourney-api/pkg/redact"

	_ "github.com/trae/midjourney-api/docs"
)

// @title Midjourney Proxy API
// @version 1.0
// @description Midjourney Proxy API Service
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:8080
// @BasePath /
// @schemes http https
func main() {
	configPath := flag.String("config", defaultConfigPath(), "path to config file")
	flag.Parse()

	app, err := app.New(*configPath)
	if err != nil {
		log.Fatalf("Failed to create application: %s", redact.Text(err.Error()))
	}

	if err := app.Run(); err != nil {
		log.Fatalf("Failed to run application: %s", redact.Text(err.Error()))
	}
}

func defaultConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("MJ_CONFIG")); value != "" {
		return value
	}
	return "config/config.yaml"
}
