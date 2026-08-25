//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// installService устанавливает службу Windows
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не удалось определить путь к исполняемому файлу: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("не удалось подключиться к SCM: %w", err)
	}
	defer m.Disconnect()

	name := "TurboPlataPCAgent"
	displayName := "TurboPlata PC Agent - Hostname server"
	desc := "Раздаёт hostname компа браузеру для привязки сканера к рабочему месту"

	s, err := m.CreateService(name, exe, mgr.Config{
		DisplayName:      displayName,
		Description:      desc,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, "run")
	if err != nil {
		return fmt.Errorf("не удалось создать службу: %w", err)
	}
	defer s.Close()

	log.Printf("✅ Служба '%s' установлена и настроена на автозапуск", name)
	log.Printf("   Исполняемый файл: %s", exe)
	log.Printf("   Запустите: sc start %s", name)
	return nil
}

// removeService удаляет службу Windows
func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("не удалось подключиться к SCM: %w", err)
	}
	defer m.Disconnect()

	name := "TurboPlataPCAgent"
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("служба '%s' не найдена: %w", name, err)
	}
	defer s.Close()

	// Останавливаем если запущена
	status, _ := s.Query()
	if status.State != svc.Stopped {
		_, err = s.Control(svc.Stop)
		if err != nil {
			log.Printf("⚠️ Не удалось остановить службу: %v", err)
		}
	}

	err = s.Delete()
	if err != nil {
		return fmt.Errorf("не удалось удалить службу: %w", err)
	}

	log.Printf("✅ Служба '%s' удалена", name)
	return nil
}

// runService запускает HTTP-сервер в режиме службы Windows
func runService() {
	dir := filepath.Dir(os.Args[0])
	os.Chdir(dir) // рабочая папка — там где exe

	err := svc.Run("TurboPlataPCAgent", &winService{})
	if err != nil {
		log.Fatalf("Ошибка запуска службы: %v", err)
	}
}

type winService struct{}

func (ws *winService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	// Запускаем HTTP-сервер в горутине
	go httpServer("9999")

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				log.Println("🛑 Служба останавливается")
				return false, 0
			}
		}
	}
}
