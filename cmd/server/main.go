package main

import (
	"log"
	"net/http"

	"github.com/joho/godotenv"

	"partydiscord/internal/config"
	"partydiscord/internal/web"
)

func main() {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	addr := ":" + cfg.Port
	log.Printf("Open http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, web.NewHandler(cfg)))
}
