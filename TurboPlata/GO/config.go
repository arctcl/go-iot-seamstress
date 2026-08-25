package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

// TerminalConfig конфигурация терминалов
type TerminalConfig struct {
	WebTerminal struct {
		Enabled bool `json:"enabled"`
		Port    int  `json:"port"`
	} `json:"web_terminal"`
	Workplaces []WorkplaceConfig `json:"workplaces"`
}

// WorkplaceConfig описывает одно рабочее место из JSON
type WorkplaceConfig struct {
	ID         string `json:"id"`
	PCHostname string `json:"pc_hostname"`
	Role       string `json:"role"`
	Active     bool   `json:"active"`
}

var terminalConfig TerminalConfig

// loadConfigs загружает конфигурационные файлы в память
func loadConfigs() error {
	// Загрузка schema.json
	schemaData, err := os.ReadFile("JSON/schema.json")
	if err != nil {
		return fmt.Errorf("ошибка чтения schema.json: %v", err)
	}
	var tempSchema map[string]interface{}
	if err := json.Unmarshal(schemaData, &tempSchema); err != nil {
		return fmt.Errorf("ошибка парсинга schema.json: %v", err)
	}
	schemaMu.Lock()
	schema = tempSchema
	schemaMu.Unlock()

	// Обновляем/создаем БД рабочих мест из JSON
	setupWorkplacesFromConfig()

	// Загрузка formulas.json
	formulasData, err := os.ReadFile("JSON/formulas.json")
	if err != nil {
		return fmt.Errorf("ошибка чтения formulas.json: %v", err)
	}
	var tempFormulas map[string]interface{}
	if err := json.Unmarshal(formulasData, &tempFormulas); err != nil {
		return fmt.Errorf("ошибка парсинга formulas.json: %v", err)
	}
	formulasMu.Lock()
	formulas = tempFormulas
	formulasMu.Unlock()

	// Загрузка employees.json
	employeesData, err := os.ReadFile("JSON/employees.json")
	if err != nil {
		return fmt.Errorf("ошибка чтения employees.json: %v", err)
	}
	var tempEmployees map[string]interface{}
	if err := json.Unmarshal(employeesData, &tempEmployees); err != nil {
		return fmt.Errorf("ошибка парсинга employees.json: %v", err)
	}
	employeesMu.Lock()
	employees = tempEmployees
	employeesMu.Unlock()

	// Загрузка конфигурации терминалов
	terminalData, err := os.ReadFile("JSON/terminals.json")
	if err != nil {
		// Конфиг по умолчанию если файла нет
		terminalConfig.WebTerminal.Enabled = true
		terminalConfig.WebTerminal.Port = 8081
	} else {
		if err := json.Unmarshal(terminalData, &terminalConfig); err != nil {
			return fmt.Errorf("ошибка парсинга terminals.json: %v", err)
		}
	}

	return nil
}

// handleConfig обрабатывает запросы к конфигурации
func handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// GET — все могут смотреть
		formulasMu.RLock()
		data := formulas
		formulasMu.RUnlock()
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: data})

	case http.MethodPost:
		if s := requireRole(r, w, "admin"); s == nil {
			return
		}
		var newConfig map[string]interface{}
		if json.NewDecoder(r.Body).Decode(&newConfig) != nil {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат JSON"})
			return
		}
		// Сохраняем на диск
		raw, _ := json.MarshalIndent(newConfig, "", "  ")
		if err := os.WriteFile("JSON/formulas.json", raw, 0644); err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка сохранения: " + err.Error()})
			return
		}
		// Обновляем в памяти
		formulasMu.Lock()
		formulas = newConfig
		formulasMu.Unlock()
		// Обновляем terminalConfig если есть секция mqtt
		if mqttRaw, ok := newConfig["mqtt"].(map[string]interface{}); ok {
			if port, ok := mqttRaw["порт"].(float64); ok {
				_ = port // будет использовано при следующем initMQTT
			}
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Конфигурация сохранена"})

	default:
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не поддерживается"})
	}
}

// setupWorkplacesFromConfig читает JSON-конфиг и обновляет БД рабочих мест.
// Вызывается в loadConfigs ДО initDatabase, поэтому открывает свою БД.
func setupWorkplacesFromConfig() {
	data, err := os.ReadFile("JSON/terminals.json")
	if err != nil {
		log.Printf("Внимание: не удалось прочитать JSON/terminals.json: %v", err)
		return
	}

	var cfg TerminalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("Ошибка парсинга JSON/terminals.json: %v", err)
		return
	}

	if len(cfg.Workplaces) == 0 {
		log.Printf("В JSON/terminals.json нет workplaces, пропускаем")
		return
	}

	db, err := sql.Open("sqlite", "workplaces.db")
	if err != nil {
		log.Fatalf("Не удалось открыть workplaces.db: %v", err)
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec(`CREATE TABLE IF NOT EXISTS рабочие_места (
		ид INTEGER PRIMARY KEY AUTOINCREMENT,
		esp_hostname TEXT UNIQUE NOT NULL,
		pc_hostname TEXT,
		роль_места TEXT NOT NULL,
		описание TEXT,
		активное INTEGER DEFAULT 1
	);`)

	// web_terminal
	db.Exec("INSERT OR IGNORE INTO рабочие_места (esp_hostname, роль_места, активное) VALUES ('web_terminal', 'web', 1)")

	// Обновляем из конфига
	for _, wp := range cfg.Workplaces {
		db.Exec(`INSERT OR REPLACE INTO рабочие_места (esp_hostname, pc_hostname, роль_места, активное)
			VALUES (?, ?, ?, ?)`, wp.ID, wp.PCHostname, wp.Role, wp.Active)
	}

	log.Printf("✅ Рабочие места обновлены (%d записей)", len(cfg.Workplaces))
}

// seedEmployeesFromJSON читает employees.json и импортирует сотрудников в employees.db.
// Вызывается после initDatabase.
func seedEmployeesFromJSON() {
	employeesMu.RLock()
	rawList, ok := employees["сотрудники"]
	employeesMu.RUnlock()
	if !ok {
		return
	}

	list, ok := rawList.([]interface{})
	if !ok || len(list) == 0 {
		return
	}

	imported := 0
	for _, raw := range list {
		emp, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		fio, _ := emp["фио"].(string)
		tabNo, _ := emp["табельный_номер"].(string)
		role, _ := emp["права"].(string)
		pass, _ := emp["пароль"].(string)

		if fio == "" || tabNo == "" {
			continue
		}

		_, err := dbEmployees.Exec(
			`INSERT OR IGNORE INTO сотрудники (фио, табельный_номер, права, пароль, активный) VALUES (?, ?, ?, ?, 1)`,
			fio, tabNo, role, pass,
		)
		if err == nil {
			imported++
		}
	}

	if imported > 0 {
		log.Printf("✅ Импортировано сотрудников из JSON: %d", imported)
	}
}
