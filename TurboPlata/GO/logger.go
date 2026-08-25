package main

import (
	"io"
	"log"
	"os"
	"os/exec"
)

// setupLogging настраивает вывод логов: файл + stdout, опционально окно tail
func setupLogging() {
	if os.Getenv("DEBUG_LOGGING") == "1" {
		DEBUG_LOGGING = 1
	}

	logFile, err := os.OpenFile("turboplata.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Не удалось открыть файл логов: %v", err)
		return
	}

	// Если DEBUG_LOGGING == 1 — открыть отдельное окно PowerShell с tail -Wait
	if DEBUG_LOGGING == 1 {
		go func() {
			cmd := exec.Command("cmd", "/C", "start", "powershell", "-NoExit", "-Command", "Get-Content -Path .\\turboplata.log -Wait")
			_ = cmd.Start()
		}()
	}

	// Логи — и в stdout, и в файл
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
}
