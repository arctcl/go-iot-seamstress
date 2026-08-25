package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"
)

// nightlyExportRoutine — ежедневный экспорт XML выплат в 20:00
func nightlyExportRoutine() {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, now.Location())
		if now.After(next) {
			next = next.Add(24 * time.Hour)
		}
		time.Sleep(next.Sub(now))

		log.Println("Начинаю экспорт XML выплат...")
		if err := exportPaymentsXML(); err != nil {
			log.Printf("Ошибка экспорта XML: %v", err)
		}
	}
}

// exportPaymentsXML экспортирует данные о выплатах в XML файл
func exportPaymentsXML() error {
	// Создаём директорию exports
	os.MkdirAll("./exports", 0755)

	// Собираем все записи со статусом 'готово_к_выплате'
	rows, err := dbMain.Query(`
        SELECT жв.ид_рабочего, SUM(жв.сумма_начислена) as итого
        FROM журнал_выработки жв
        WHERE жв.статус_выплаты = 'готово_к_выплате'
        GROUP BY жв.ид_рабочего
    `)
	if err != nil {
		return fmt.Errorf("запрос выплат: %w", err)
	}
	defer rows.Close()

	type PaymentLine struct {
		WorkerID int
		FIO      string
		TabNo    string
		Total    float64
	}
	var payments []PaymentLine

	for rows.Next() {
		var workerID int
		var total float64
		if rows.Scan(&workerID, &total) != nil {
			continue
		}
		var fio, tabNo string
		dbEmployees.QueryRow(`SELECT фио, табельный_номер FROM сотрудники WHERE ид = ?`, workerID).Scan(&fio, &tabNo)
		payments = append(payments, PaymentLine{WorkerID: workerID, FIO: fio, TabNo: tabNo, Total: total})
	}

	if len(payments) == 0 {
		log.Println("Нет записей для экспорта")
		return nil
	}

	// Формируем XML
	dateStr := time.Now().Format("2006-01-02")
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<выплаты дата="%s">
`, dateStr)

	for _, p := range payments {
		xml += fmt.Sprintf(`  <сотрудник табельный_номер="%s" фио="%s" сумма_руб="%.2f" сумма_коп="%.0f" />`+"\n", p.TabNo, p.FIO, p.Total/100.0, p.Total)
	}
	xml += `</выплаты>`

	// Сохраняем файл
	filename := fmt.Sprintf("./exports/payments_%s.xml", dateStr)
	if err := os.WriteFile(filename, []byte(xml), 0644); err != nil {
		return fmt.Errorf("запись XML: %w", err)
	}

	// Помечаем записи как 'выплачено'
	dbMain.Exec(`UPDATE журнал_выработки SET статус_выплаты = 'выплачено' WHERE статус_выплаты = 'готово_к_выплате'`)

	log.Printf("XML экспорт завершён: %s (%d сотрудников)", filename, len(payments))
	return nil
}

// backupRoutine — резервное копирование баз данных каждые 15 минут
// + удаление бэкапов старше 7 дней
func backupRoutine() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		backupDatabaseFile(dbMain, "turboplata")
		backupDatabaseFile(dbEmployees, "employees")
		backupDatabaseFile(dbWorkplaces, "workplaces")
		cleanOldBackups()
	}
}

// cleanOldBackups удаляет бэкапы старше 7 дней
func cleanOldBackups() {
	files, err := os.ReadDir("./backups")
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	removed := 0
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove("./backups/" + f.Name())
			removed++
		}
	}
	if removed > 0 {
		log.Printf("🧹 Удалено старых бэкапов: %d", removed)
	}
}

// backupDatabaseFile создает резервную копию базы данных
func backupDatabaseFile(db *sql.DB, name string) {
	os.MkdirAll("./backups", 0755)
	backupName := fmt.Sprintf("./backups/%s_%s.db", name, time.Now().Format("20060102_150405"))
	if _, err := db.Exec(fmt.Sprintf("VACUUM INTO '%s'", backupName)); err != nil {
		log.Printf("Ошибка резервного копирования %s: %v", name, err)
	} else {
		log.Printf("Резервная копия %s создана", name)
	}
}

// cleanupRoutine — очистка старых записей в 03:00
func cleanupRoutine() {
	// Очистка в 03:00
	for {
		now := time.Now()
		next := now.Add(time.Hour * 24)
		next = time.Date(next.Year(), next.Month(), next.Day(), 3, 0, 0, 0, next.Location())
		time.Sleep(next.Sub(now))

		if err := cleanOldRecords(); err != nil {
			log.Printf("⚠️ Ошибка очистки: %v", err)
		}
	}
}

// cleanOldRecords удаляет старые записи из базы данных
func cleanOldRecords() error {
	// Удаляем только записи, которые уже выплачены
	if _, err := dbMain.Exec("DELETE FROM журнал_выработки WHERE время_окончания < datetime('now', '-30 days') AND статус_выплаты = 'выплачено'"); err != nil {
		return err
	}

	// Сжимаем БД — VACUUM перестраивает файл целиком
	if _, err := dbMain.Exec("VACUUM"); err != nil {
		log.Printf("⚠️ VACUUM не удался: %v", err)
		// Не фатально
	}

	log.Println("🧹 Очистка старых записей завершена")
	return nil
}
