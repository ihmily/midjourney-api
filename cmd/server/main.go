package main

import (
	"log"

	"github.com/trae/midjourney-api/internal/app"

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
	app, err := app.New("config/config.yaml")
	if err != nil {
		log.Fatalf("Failed to create application: %v", err)
	}

	if err := app.Run(); err != nil {
		log.Fatalf("Failed to run application: %v", err)
	}
}
