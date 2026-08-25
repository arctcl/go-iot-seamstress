//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

const systemdUnit = `[Unit]
Description=TurboPlata PC Agent — Hostname server
After=network.target

[Service]
Type=simple
ExecStart=%s run
WorkingDirectory=%s
Restart=always
RestartSec=5
User=nobody

[Install]
WantedBy=multi-user.target
`

// installService устанавливает systemd-юнит
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не удалось определить путь: %w", err)
	}
	dir := filepath.Dir(exe)

	unitPath := "/etc/systemd/system/turboplata-pc.service"
	if os.Geteuid() != 0 {
		unitPath = filepath.Join(os.Getenv("HOME"), ".config/systemd/user/turboplata-pc.service")
	}

	// Генерируем unit-файл
	unitContent := fmt.Sprintf(systemdUnit, exe, dir)
	if err := os.WriteFile(unitPath, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("не удалось записать %s: %w", unitPath, err)
	}

	// Перезагружаем systemd
	cmd := exec.Command("systemctl", "daemon-reload")
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("⚠️ systemctl daemon-reload: %s", output)
	}

	// Включаем автозапуск
	cmd = exec.Command("systemctl", "enable", "turboplata-pc.service")
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("⚠️ systemctl enable: %s", output)
	}

	log.Printf("✅ systemd-юнит установлен: %s", unitPath)
	log.Printf("   Запустите: systemctl start turboplata-pc")
	return nil
}

// removeService удаляет systemd-юнит
func removeService() error {
	unitPath := "/etc/systemd/system/turboplata-pc.service"
	if os.Geteuid() != 0 {
		unitPath = filepath.Join(os.Getenv("HOME"), ".config/systemd/user/turboplata-pc.service")
	}

	// Останавливаем
	exec.Command("systemctl", "stop", "turboplata-pc").Run()

	// Отключаем
	exec.Command("systemctl", "disable", "turboplata-pc").Run()

	// Удаляем файл
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("не удалось удалить %s: %w", unitPath, err)
	}

	exec.Command("systemctl", "daemon-reload").Run()

	log.Printf("✅ systemd-юнит удалён")
	return nil
}

// runService запускает HTTP-сервер на переднем плане (в демоне — через systemd)
func runService() {
	httpServer("9999")
}

func systemdMain() {
	// Обработка сигналов для graceful shutdown при запуске вручную
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("🛑 Останавливаемся...")
		os.Exit(0)
	}()

	runService()
}
