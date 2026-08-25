package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

// initDatabase инициализирует подключения к базам данных
func initDatabase() error {
	var err error
	dbMain, err = sql.Open("sqlite", "turboplata.db?mode=rwc&_journal_mode=WAL")
	if err != nil {
		return err
	}

	dbEmployees, err = sql.Open("sqlite", "employees.db?mode=rwc&_journal_mode=WAL")
	if err != nil {
		return err
	}

	dbWorkplaces, err = sql.Open("sqlite", "workplaces.db?mode=rwc&_journal_mode=WAL")
	if err != nil {
		return err
	}

	// Включаем WAL режим для всех БД
	if _, err := dbMain.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	if _, err := dbEmployees.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	if _, err := dbWorkplaces.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return err
	}

	// Включаем foreign keys для всех БД
	if _, err := dbMain.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return err
	}
	if _, err := dbEmployees.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return err
	}
	if _, err := dbWorkplaces.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return err
	}

	log.Println("✅ База данных инициализирована")
	// Инициализируем схему, если таблицы ещё не созданы
	if err := initSchema(); err != nil {
		return fmt.Errorf("ошибка инициализации схемы: %w", err)
	}
	return nil
}

// initSchema читает schema.json и создает таблицы, если они отсутствуют.
func initSchema() error {
	data, err := os.ReadFile("JSON/schema.json")
	if err != nil {
		return fmt.Errorf("не удалось прочитать schema.json: %w", err)
	}
	var schemaDef struct {
		Databases map[string]struct {
			File   string `json:"файл"`
			Tables map[string]struct {
				Columns []struct {
					Name         string      `json:"имя"`
					Type         string      `json:"тип"`
					DefaultValue interface{} `json:"по_умолчанию,omitempty"`
				} `json:"столбцы"`
			} `json:"таблицы"`
		} `json:"базы_данных"`
	}
	if err := json.Unmarshal(data, &schemaDef); err != nil {
		return fmt.Errorf("не удалось распарсить schema.json: %w", err)
	}

	for _, dbDef := range schemaDef.Databases {
		var currentDB *sql.DB
		switch dbDef.File {
		case "turboplata.db":
			currentDB = dbMain
		case "employees.db":
			currentDB = dbEmployees
		case "workplaces.db":
			currentDB = dbWorkplaces
		default:
			log.Printf("Неизвестная база данных в схеме: %s", dbDef.File)
			continue
		}

		for tableName, tbl := range dbDef.Tables {
			var cols []string
			for _, col := range tbl.Columns {
				colDef := fmt.Sprintf("%s %s", col.Name, col.Type)
				if col.DefaultValue != nil {
					switch v := col.DefaultValue.(type) {
					case string:
						colDef += fmt.Sprintf(" DEFAULT '%s'", v)
					default:
						colDef += fmt.Sprintf(" DEFAULT %v", v)
					}
				}
				cols = append(cols, colDef)
			}
			stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", tableName, strings.Join(cols, ", "))
			if _, err := currentDB.Exec(stmt); err != nil {
				return fmt.Errorf("создание таблицы %s в %s: %w", tableName, dbDef.File, err)
			}
		}
	}

	// Миграция: добавляем недостающие колонки в существующие таблицы
	migrations := []struct {
		db    *sql.DB
		table string
		col   string
		typ   string
		def   string
	}{
		{dbMain, "заказы", "код", "TEXT", "''"},
		{dbMain, "техкарты", "код", "TEXT", "''"},
		{dbMain, "коробки", "код", "TEXT", "''"},
		{dbMain, "коробки", "номер_в_заказе", "INTEGER", "0"},
		{dbMain, "операции_коробки", "код", "TEXT", "''"},
	}
	for _, m := range migrations {
		m.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s DEFAULT %s", m.table, m.col, m.typ, m.def))
	}
	return nil
}
