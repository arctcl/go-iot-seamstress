package main

import (
	"log"
	"os"
	"path/filepath"
)

func main() {
	// Определяем команду
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "install":
		if err := installService(); err != nil {
			log.Fatalf("❌ Ошибка установки: %v", err)
		}

	case "remove", "uninstall":
		if err := removeService(); err != nil {
			log.Fatalf("❌ Ошибка удаления: %v", err)
		}

	case "run":
		// Работаем из своей папки
		dir := filepath.Dir(os.Args[0])
		os.Chdir(dir)
		runService()

	case "--help", "-h", "":
		printUsage()

	default:
		log.Fatalf("Неизвестная команда: %s", cmd)
	}
}
