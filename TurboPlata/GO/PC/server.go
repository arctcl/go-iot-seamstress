// Package main — универсальный PC-агент TurboPlata
// Запускает HTTP-сервер на 127.0.0.1:9999, отдаёт hostname компа.
// Поддерживает установку как служба Windows или systemd-юнит Linux.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

// HostnameResponse — ответ на GET /
type HostnameResponse struct {
	Hostname string `json:"hostname"`
}

// httpServer запускает HTTP-сервер на указанном порту.
// Адрес всегда 127.0.0.1 — только localhost.
func httpServer(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HostnameResponse{Hostname: hostname})
	})

	addr := fmt.Sprintf("127.0.0.1:%s", port)
	log.Printf("🚀 PC-агент запущен на %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Ошибка HTTP-сервера: %v", err)
	}
}

func printUsage() {
	fmt.Println(`TurboPlata PC-агент — раздаёт hostname компа браузеру через localhost:9999

Использование:
  pc-agent                    Запустить HTTP-сервер (на переднем плане)
  pc-agent install            Установить как службу/демон
  pc-agent remove             Удалить службу/демон
  pc-agent --help             Эта справка`)
	os.Exit(0)
}
