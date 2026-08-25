package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Employee struct {
	ID       int    `json:"ид"`
	FIO      string `json:"фио"`
	TabNo    string `json:"табельный_номер"`
	Password string `json:"пароль"`
	Role     string `json:"роль"`
	Active   int    `json:"активный"`
}

// parseTimeFlexible парсит время из SQLite в нескольких распространённых форматах
func parseTimeFlexible(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02T15:04:05.000",
		"2006-01-02 15:04:05Z",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("не удалось распарсить время: %s", s)
}

// kopecksToRubles конвертирует копейки в рубли для отдачи на фронтенд
func kopecksToRubles(kop float64) float64 {
	return kop / 100.0
}

// ---------------------------------------------------------------------------
// ОБРАБОТКА СКАНИРОВАНИЙ
// ---------------------------------------------------------------------------

// handleScan обрабатывает сканирование штрихкода (HTTP entry point)
// Для веб-сканов (с сайта) берёт рабочего из сессии.
func handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не допущен"})
		return
	}

	var scan ScanMessage
	if err := json.NewDecoder(r.Body).Decode(&scan); err != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	result := processScan(r, scan.Barcode)
	respondJSON(w, http.StatusOK, result)
}

// processScan обрабатывает сканирование (HTTP версия с сайта).
// Только USER_XXX авторизует и синхронизирует currentWorker["web"].
// Без USER_XXX — операции и DONE требуют предварительной авторизации.
func processScan(r *http.Request, barcode string) APIResponse {
	if strings.HasPrefix(barcode, "USER_") {
		result := handleUserAuthWithContext(barcode, "web")
		if result.Status == "ok" {
			log.Printf("✅ Веб-скан: авторизован %s", barcode)
		}
		return result
	}

	// Для DONE и операций — проверяем что currentWorker["web"] есть
	currentWorkerMu.RLock()
	_, hasWorker := currentWorker["web"]
	currentWorkerMu.RUnlock()
	if !hasWorker {
		return APIResponse{Status: "error", Message: "Сначала отсканируйте USER_ табельный номер"}
	}

	return processScanWithContext(barcode, "web")
}

// processScanWithContext обрабатывает сканирование с привязкой к столу
func processScanWithContext(barcode, espHostname string) APIResponse {
	log.Printf("📡 Скан: %s (esp=%s)", barcode, espHostname)

	if strings.HasPrefix(barcode, "USER_") {
		return handleUserAuthWithContext(barcode, espHostname)
	}

	if barcode == "DONE" {
		return handleDoneScan(espHostname)
	}

	return handleOperationScanWithContext(barcode, espHostname)
}

// handleUserAuthWithContext обрабатывает авторизацию с привязкой к столу
func handleUserAuthWithContext(barcode, espHostname string) APIResponse {
	tabNumber := strings.TrimPrefix(barcode, "USER_")

	var empID int
	var fio, role string
	var matchedTabNo string
	err := dbEmployees.QueryRow(`SELECT ид, фио, права, табельный_номер FROM сотрудники WHERE табельный_номер = ? AND активный = 1`, tabNumber).Scan(&empID, &fio, &role, &matchedTabNo)
	if err != nil {
		// Пробуем оригинальный баркод (с USER_)
		err = dbEmployees.QueryRow(`SELECT ид, фио, права, табельный_номер FROM сотрудники WHERE табельный_номер = ? AND активный = 1`, barcode).Scan(&empID, &fio, &role, &matchedTabNo)
	}
	if err != nil {
		log.Printf("❌ Неизвестный код: %s", barcode)
		return APIResponse{Status: "error", Message: "Неизвестный табельный номер"}
	}

	// Проверяем рабочее место только для ESP, для веб-сканов пропускаем
	if espHostname != "web" {
		var wpRole string
		err = dbWorkplaces.QueryRow(`SELECT роль_места FROM рабочие_места WHERE esp_hostname = ? AND активное = 1`, espHostname).Scan(&wpRole)
		if err != nil {
			return APIResponse{Status: "error", Message: "Рабочее место не зарегистрировано"}
		}
	}

	currentWorkerMu.Lock()
	// Используем табельный_номер как в БД (с USER_ префиксом если есть)
	currentWorker[espHostname] = matchedTabNo
	currentWorkerMu.Unlock()

	log.Printf("✅ Авторизация: %s (%s) на %s, роль=%s", fio, matchedTabNo, espHostname, role)

	return APIResponse{
		Status:  "ok",
		Message: fmt.Sprintf("Добро пожаловать, %s!", fio),
		Data: map[string]interface{}{
			"ид_сотрудника": empID,
			"фио":           fio,
			"роль":          role,
			"табельный":     matchedTabNo,
			"esp":           espHostname,
		},
	}
}

// handleOperationScanWithContext обрабатывает сканирование операции с привязкой к столу
func handleOperationScanWithContext(barcode, espHostname string) APIResponse {
	p := parseBarcode(barcode)
	if p.BoxID == 0 {
		return APIResponse{Status: "error", Message: "Неверный формат штрихкода"}
	}

	boxID := p.BoxID

	var opID, step, techCardID int
	var normSec float64
	var status string
	var err error

	if p.OpCode != "" {
		// Новый формат — ищем по коду операции
		err = dbMain.QueryRow(`SELECT ид, шаг, ид_техкарты, норма_времени_сек, статус FROM операции_коробки WHERE ид_коробки = ? AND код = ?`, boxID, p.OpCode).Scan(&opID, &step, &techCardID, &normSec, &status)
	} else if p.OpName != "" {
		// Старый формат — ищем по названию
		err = dbMain.QueryRow(`SELECT ид, шаг, ид_техкарты, норма_времени_сек, статус FROM операции_коробки WHERE ид_коробки = ? AND название = ?`, boxID, p.OpName).Scan(&opID, &step, &techCardID, &normSec, &status)
	} else {
		return APIResponse{Status: "error", Message: "Штрихкод не содержит код операции"}
	}
	if err != nil {
		return APIResponse{Status: "error", Message: fmt.Sprintf("Операция не найдена для коробки %d", boxID)}
	}
	if status != "ожидает" {
		return APIResponse{Status: "error", Message: fmt.Sprintf("Операция уже в статусе '%s'", status)}
	}

	var boxStatus string
	dbMain.QueryRow(`SELECT статус FROM коробки WHERE ид = ?`, boxID).Scan(&boxStatus)
	if boxStatus != "в_работе" {
		return APIResponse{Status: "error", Message: fmt.Sprintf("Коробка #%d в статусе '%s'", boxID, boxStatus)}
	}

	currentWorkerMu.RLock()
	workerTabNo, hasWorker := currentWorker[espHostname]
	currentWorkerMu.RUnlock()
	if !hasWorker || workerTabNo == "" {
		return APIResponse{Status: "error", Message: "Нет авторизованного рабочего. Отсканируйте USER_ табельный номер."}
	}

	var workerID int
	dbEmployees.QueryRow(`SELECT ид FROM сотрудники WHERE табельный_номер = ?`, workerTabNo).Scan(&workerID)

	opNameDisplay := p.OpName
	if opNameDisplay == "" {
		dbMain.QueryRow(`SELECT название FROM операции_коробки WHERE ид = ?`, opID).Scan(&opNameDisplay)
	}

	dbMain.Exec(`UPDATE операции_коробки SET статус = 'в_работе', ид_исполнителя = ? WHERE ид = ?`, workerID, opID)
	dbMain.Exec(`INSERT INTO журнал_выработки (ид_операции, ид_рабочего, ид_стола, время_начала) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, opID, workerID, espHostname)

	log.Printf("📦 Операция #%d '%s' начата рабочим %d на столе %s", opID, opNameDisplay, workerID, espHostname)
	return APIResponse{Status: "ok", Message: fmt.Sprintf("Операция '%s' начата.", opNameDisplay), Data: map[string]interface{}{"ид_операции": opID, "ид_коробки": boxID, "название": opNameDisplay, "шаг": step}}
}

// handleDoneScan — завершение операции в единой транзакции.
// Атомарно: закрывает журнал, считает оплату, проверяет идемпотентность,
// пишет аудит, переходит на следующую операцию или закрывает коробку.
func handleDoneScan(tableID string) APIResponse {
	currentWorkerMu.RLock()
	workerTabNo, hasWorker := currentWorker[tableID]
	currentWorkerMu.RUnlock()
	if !hasWorker || workerTabNo == "" {
		return APIResponse{Status: "error", Message: "Нет авторизованного рабочего."}
	}

	var workerID int
	err := dbEmployees.QueryRow(`SELECT ид FROM сотрудники WHERE табельный_номер = ?`, workerTabNo).Scan(&workerID)
	if err != nil {
		return APIResponse{Status: "error", Message: "Рабочий не найден в БД"}
	}

	// Открываем транзакцию
	tx, err := dbMain.Begin()
	if err != nil {
		log.Printf("Ошибка начала транзакции: %v", err)
		return APIResponse{Status: "error", Message: "Ошибка БД"}
	}
	defer tx.Rollback() // откат, если не сделали Commit

	// Ищем последнюю незакрытую запись
	var journalID, opID int
	var startTime string
	err = tx.QueryRow(`
		SELECT ид, ид_операции, время_начала
		FROM журнал_выработки
		WHERE ид_рабочего = ? AND ид_стола = ? AND время_окончания IS NULL
		ORDER BY ид DESC LIMIT 1`, workerID, tableID).Scan(&journalID, &opID, &startTime)
	if err != nil {
		return APIResponse{Status: "error", Message: "Нет незавершённых операций."}
	}

	// Идемпотентность: проверяем что за эту запись уже не начисляли
	var existingPayment float64
	err = tx.QueryRow(`SELECT COALESCE(сумма_начислена, 0) FROM журнал_выработки WHERE ид = ?`, journalID).Scan(&existingPayment)
	if err == nil && existingPayment > 0 {
		log.Printf("⚠️ Повторный DONE для выработки #%d, уже начислено %.2f", journalID, existingPayment)
		return APIResponse{Status: "error", Message: "Операция уже завершена"}
	}

	// Данные операции
	var boxID, step int
	var opName string
	var normSec float64
	var techRazryad int
	err = tx.QueryRow(`
		SELECT ид_коробки, шаг, название, норма_времени_сек, разряд
		FROM операции_коробки WHERE ид = ?`, opID).Scan(&boxID, &step, &opName, &normSec, &techRazryad)
	if err != nil {
		log.Printf("Ошибка получения операции %d: %v", opID, err)
		normSec = 60
		techRazryad = 1
	}

	// Количество изделий в коробке
	var boxQty int
	tx.QueryRow(`SELECT количество FROM коробки WHERE ид = ?`, boxID).Scan(&boxQty)
	if boxQty < 1 {
		boxQty = 1
	}

	// Закрываем запись
	_, err = tx.Exec(`UPDATE журнал_выработки SET время_окончания = CURRENT_TIMESTAMP WHERE ид = ?`, journalID)
	if err != nil {
		log.Printf("Ошибка закрытия выработки #%d: %v", journalID, err)
		return APIResponse{Status: "error", Message: "Не удалось завершить операцию"}
	}

	// Фактическое время — только для лога/мониторинга, на ЗП не влияет
	var endTimeStr string
	tx.QueryRow(`SELECT время_окончания FROM журнал_выработки WHERE ид = ?`, journalID).Scan(&endTimeStr)
	startParsed, _ := parseTimeFlexible(startTime)
	endParsed, _ := parseTimeFlexible(endTimeStr)
	actualSec := endParsed.Sub(startParsed).Seconds()
	if actualSec <= 0 {
		actualSec = 1
	}

	// Расчёт оплаты в КОПЕЙКАХ: баз_ставка_коп × норма_1шт × количество × коэфф_разряда
	formulasMu.RLock()
	baseRateKop := 50.0
	if конст, ok := formulas["глобальные_константы"].(map[string]interface{}); ok {
		if b, ok := конст["оплата_в_секунду_базовая_коп"].(float64); ok {
			baseRateKop = b
		}
	}
	razryadCoeff := 1.0
	if конст, ok := formulas["глобальные_константы"].(map[string]interface{}); ok {
		if коэфф, ok := конст["коэффициенты_разрядов"].(map[string]interface{}); ok {
			if c, ok := коэфф[strconv.Itoa(techRazryad)].(float64); ok {
				razryadCoeff = c
			}
		}
	}
	formulasMu.RUnlock()

	// Оплата в копейках
	paymentKop := baseRateKop * normSec * float64(boxQty) * razryadCoeff

	// Подозрение только по фактическому времени (для мониторинга)
	suspicious := 0
	if actualSec > 0 {
		formulasMu.RLock()
		if проверка, ok := formulas["проверка_реальности"].(map[string]interface{}); ok {
			if minTime, ok := проверка["минимальное_время_на_изделие_сек"].(float64); ok && actualSec < minTime {
				suspicious = 1
			}
		}
		formulasMu.RUnlock()
	}

	// Обновляем запись — сумма в копейках
	_, err = tx.Exec(`UPDATE журнал_выработки SET сумма_начислена = ?, штраф = ?, статус_выплаты = 'начислено' WHERE ид = ?`,
		paymentKop, suspicious, journalID)
	if err != nil {
		log.Printf("Ошибка начисления: %v", err)
		return APIResponse{Status: "error", Message: "Ошибка начисления"}
	}

	// Операция завершена
	_, err = tx.Exec(`UPDATE операции_коробки SET статус = 'завершена' WHERE ид = ?`, opID)
	if err != nil {
		log.Printf("Ошибка закрытия операции: %v", err)
		return APIResponse{Status: "error", Message: "Ошибка закрытия операции"}
	}

	// Аудит
	fio := ""
	dbEmployees.QueryRow(`SELECT фио FROM сотрудники WHERE ид = ?`, workerID).Scan(&fio)
	auditJSON, _ := json.Marshal(map[string]interface{}{
		"ид_выработки": journalID, "ид_операции": opID,
		"ид_коробки": boxID, "рабочий": fio,
		"операция": opName, "норма_сек": normSec,
		"факт_сек": actualSec, "начислено_коп": paymentKop, "разряд": techRazryad,
	})
	tx.Exec(`INSERT INTO аудит (тип, ид_выработки, ид_рабочего, сумма, данные, создано)
		VALUES ('начисление', ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		journalID, workerID, paymentKop, string(auditJSON))

	// Проверяем следующую операцию
	var nextOpID int
	err = tx.QueryRow(`
		SELECT ид FROM операции_коробки
		WHERE ид_коробки = ? AND шаг > ? AND статус = 'ожидает'
		ORDER BY шаг ASC LIMIT 1`, boxID, step).Scan(&nextOpID)

	var nextOpMsg string
	if err == nil && nextOpID > 0 {
		// Не запускаем следующую операцию автоматически — швея сама отсканирует КОРОБКА-ОПЕРАЦИЯ
		nextOpMsg = fmt.Sprintf(", следующая #%d ожидает сканирования", nextOpID)
	} else {
		var pending int
		tx.QueryRow(`SELECT COUNT(*) FROM операции_коробки WHERE ид_коробки = ? AND статус != 'завершена'`, boxID).Scan(&pending)
		if pending == 0 {
			tx.Exec(`UPDATE коробки SET статус = 'ожидает_отк', дата_завершения = CURRENT_TIMESTAMP WHERE ид = ?`, boxID)
			nextOpMsg = ", коробка на ОТК"
		}
	}

	// Коммитим транзакцию
	if err := tx.Commit(); err != nil {
		log.Printf("❌ Ошибка COMMIT транзакции: %v", err)
		return APIResponse{Status: "error", Message: "Ошибка сохранения данных"}
	}

	// Обновляем кеш скоростей
	refreshSpeedCache()

	log.Printf("💰 DONE: '%s' на #%d, рабочий %d, %.1fс / норма %.1fс, %.0f коп%s",
		opName, boxID, workerID, actualSec, normSec, paymentKop, nextOpMsg)

	return APIResponse{
		Status:  "ok",
		Message: fmt.Sprintf("Операция «%s» завершена. Начислено: %.2f руб%s", opName, kopecksToRubles(paymentKop), nextOpMsg),
		Data: map[string]interface{}{
			"ид_выработки":   journalID,
			"ид_операции":    opID,
			"время_факт_сек": actualSec,
			"начислено":      kopecksToRubles(paymentKop),
			"начислено_коп":  paymentKop,
			"подозрение":     suspicious,
		},
	}
}

// authenticateWorker авторизует пользователя (из utils.go)
func authenticateWorker(tableID string, userID string) APIResponse {
	if userID == "" {
		return APIResponse{Status: "error", Message: "ID не может быть пустым"}
	}

	var workerID int
	var workerName, workerRights string
	tabNumber := userID
	err := dbEmployees.QueryRow(`
		SELECT ид, фио, права
		FROM сотрудники
		WHERE табельный_номер = ? AND активный = 1
	`, tabNumber).Scan(&workerID, &workerName, &workerRights)

	if err != nil {
		log.Printf("❌ Неизвестный табельный номер: %s", tabNumber)
		return APIResponse{Status: "error", Message: "Неизвестный табельный номер"}
	}

	currentWorkerMu.Lock()
	currentWorker[tableID] = tabNumber
	currentWorkerMu.Unlock()
	log.Printf("✅ Рабочий %s (%d) авторизован на столе %s с правами %s", workerName, workerID, tableID, workerRights)

	allowedPage := "vydacha"

	employeesMu.RLock()
	if rightsMap, ok := employees["права"].(map[string]interface{}); ok {
		if rights, ok := rightsMap[workerRights].(map[string]interface{}); ok {
			if pages, ok := rights["доступные_страницы"].([]interface{}); ok && len(pages) > 0 {
				allowedPage = pages[0].(string)
			}
		}
	}
	employeesMu.RUnlock()

	return APIResponse{
		Status:  "ok",
		Message: fmt.Sprintf("Добро пожаловать, %s!", workerName),
		Data: map[string]interface{}{
			"worker_id":   workerID,
			"worker_name": workerName,
			"rights":      workerRights,
			"page":        allowedPage,
		},
	}
}

// ---------------------------------------------------------------------------
// УПРАВЛЕНИЕ СОТРУДНИКАМИ
// ---------------------------------------------------------------------------

// handleEmployees — CRUD для сотрудников
func handleEmployees(w http.ResponseWriter, r *http.Request) {
	if s := requireRole(r, w, "admin"); s == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		getEmployees(w, r)
	case http.MethodPost:
		createEmployee(w, r)
	case http.MethodPut:
		updateEmployee(w, r)
	case http.MethodDelete:
		deleteEmployee(w, r)
	default:
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{
			Status:  "error",
			Message: "Метод не поддерживается",
		})
	}
}

func getEmployees(w http.ResponseWriter, r *http.Request) {
	rows, err := dbEmployees.Query(`SELECT ид, фио, табельный_номер, права, активный FROM сотрудники ORDER BY ид`)
	if err != nil {
		log.Printf("Ошибка запроса сотрудников: %v", err)
		respondJSON(w, http.StatusInternalServerError, APIResponse{
			Status:  "error",
			Message: "Ошибка базы данных",
		})
		return
	}
	defer rows.Close()

	var employeesList []Employee
	for rows.Next() {
		var emp Employee
		err := rows.Scan(&emp.ID, &emp.FIO, &emp.TabNo, &emp.Role, &emp.Active)
		if err != nil {
			log.Printf("Ошибка чтения строки сотрудника: %v", err)
			continue
		}
		emp.Password = ""
		employeesList = append(employeesList, emp)
	}
	if err := rows.Err(); err != nil {
		log.Printf("Ошибка итерации по сотрудникам: %v", err)
		respondJSON(w, http.StatusInternalServerError, APIResponse{
			Status:  "error",
			Message: "Ошибка обработки данных",
		})
		return
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Status: "ok",
		Data:   employeesList,
	})
}

func createEmployee(w http.ResponseWriter, r *http.Request) {
	var newEmp Employee
	if err := json.NewDecoder(r.Body).Decode(&newEmp); err != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{
			Status:  "error",
			Message: "Неверный формат данных",
		})
		return
	}

	if newEmp.Role == "admin" {
		respondJSON(w, http.StatusForbidden, APIResponse{
			Status:  "error",
			Message: "Создание администраторов через интерфейс запрещено",
		})
		return
	}

	result, err := dbEmployees.Exec(`INSERT INTO сотрудники (фио, табельный_номер, пароль, права, активный) VALUES (?, ?, ?, ?, ?)`,
		newEmp.FIO, newEmp.TabNo, newEmp.Password, newEmp.Role, newEmp.Active)
	if err != nil {
		log.Printf("Ошибка вставки сотрудника: %v", err)
		respondJSON(w, http.StatusInternalServerError, APIResponse{
			Status:  "error",
			Message: "Ошибка базы данных",
		})
		return
	}

	id, _ := result.LastInsertId()
	newEmp.ID = int(id)

	if err := syncEmployeesToJSON(); err != nil {
		log.Printf("Ошибка синхронизации сотрудников в JSON: %v", err)
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Status:  "ok",
		Message: "Сотрудник добавлен",
		Data:    newEmp,
	})
}

func updateEmployee(w http.ResponseWriter, r *http.Request) {
	var updatedEmp Employee
	if err := json.NewDecoder(r.Body).Decode(&updatedEmp); err != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{
			Status:  "error",
			Message: "Неверный формат данных",
		})
		return
	}

	var currentRole string
	err := dbEmployees.QueryRow(`SELECT права FROM сотрудники WHERE ид = ?`, updatedEmp.ID).Scan(&currentRole)
	if err != nil {
		if err == sql.ErrNoRows {
			respondJSON(w, http.StatusNotFound, APIResponse{
				Status:  "error",
				Message: "Сотрудник не найден",
			})
		} else {
			log.Printf("Ошибка запроса сотрудника: %v", err)
			respondJSON(w, http.StatusInternalServerError, APIResponse{
				Status:  "error",
				Message: "Ошибка базы данных",
			})
		}
		return
	}

	if currentRole == "admin" {
		respondJSON(w, http.StatusForbidden, APIResponse{
			Status:  "error",
			Message: "Изменение администраторов запрещено",
		})
		return
	}

	_, err = dbEmployees.Exec(`UPDATE сотрудники SET фио = ?, табельный_номер = ?, пароль = ?, права = ?, активный = ? WHERE ид = ?`,
		updatedEmp.FIO, updatedEmp.TabNo, updatedEmp.Password, updatedEmp.Role, updatedEmp.Active, updatedEmp.ID)
	if err != nil {
		log.Printf("Ошибка обновления сотрудника: %v", err)
		respondJSON(w, http.StatusInternalServerError, APIResponse{
			Status:  "error",
			Message: "Ошибка базы данных",
		})
		return
	}

	if err := syncEmployeesToJSON(); err != nil {
		log.Printf("Ошибка синхронизации сотрудников в JSON: %v", err)
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Status:  "ok",
		Message: "Данные сотрудника обновлены",
	})
}

func deleteEmployee(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{
			Status:  "error",
			Message: "Неверный идентификатор сотрудника",
		})
		return
	}

	var currentRole string
	err = dbEmployees.QueryRow(`SELECT права FROM сотрудники WHERE ид = ?`, id).Scan(&currentRole)
	if err != nil {
		if err == sql.ErrNoRows {
			respondJSON(w, http.StatusNotFound, APIResponse{
				Status:  "error",
				Message: "Сотрудник не найден",
			})
		} else {
			log.Printf("Ошибка запроса сотрудника: %v", err)
			respondJSON(w, http.StatusInternalServerError, APIResponse{
				Status:  "error",
				Message: "Ошибка базы данных",
			})
		}
		return
	}

	if currentRole == "admin" {
		respondJSON(w, http.StatusForbidden, APIResponse{
			Status:  "error",
			Message: "Удаление администраторов запрещено",
		})
		return
	}

	_, err = dbEmployees.Exec(`UPDATE сотрудники SET активный = 0 WHERE ид = ?`, id)
	if err != nil {
		log.Printf("Ошибка деактивации сотрудника: %v", err)
		respondJSON(w, http.StatusInternalServerError, APIResponse{
			Status:  "error",
			Message: "Ошибка базы данных",
		})
		return
	}

	if err := syncEmployeesToJSON(); err != nil {
		log.Printf("Ошибка синхронизации сотрудников в JSON: %v", err)
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Status:  "ok",
		Message: "Сотрудник деактивирован",
	})
}

// syncEmployeesToJSON синхронизирует сотрудников из БД в JSON файл
func syncEmployeesToJSON() error {
	// Сохраняем текущую секцию прав из файла
	var rightsSection interface{}
	if data, err := os.ReadFile("JSON/employees.json"); err == nil {
		var existing map[string]interface{}
		if json.Unmarshal(data, &existing) == nil {
			if r, ok := existing["права"]; ok {
				rightsSection = r
			}
		}
	}

	// Читаем сотрудников из БД
	rows, err := dbEmployees.Query(`SELECT ид, фио, табельный_номер, пароль, права, активный FROM сотрудники WHERE активный = 1`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var employeesList []map[string]interface{}
	for rows.Next() {
		var id, active int
		var fio, tabNo, password, role string
		if rows.Scan(&id, &fio, &tabNo, &password, &role, &active) != nil {
			continue
		}
		employeesList = append(employeesList, map[string]interface{}{
			"ид":              id,
			"фио":             fio,
			"табельный_номер": tabNo,
			"пароль":          password,
			"права":           role,
			"активный":        active,
		})
	}

	toSave := map[string]interface{}{
		"сотрудники": employeesList,
	}
	if rightsSection != nil {
		toSave["права"] = rightsSection
	}

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("JSON/employees.json", data, 0644)
}

// ---------------------------------------------------------------------------
// ОБРАБОТЧИКИ ОТК И МАСТЕРА
// ---------------------------------------------------------------------------

func handleOTKBoxDetails(w http.ResponseWriter, r *http.Request) {
	barcode := r.URL.Query().Get("barcode")
	if barcode == "" {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Требуется параметр barcode"})
		return
	}

	// Поддерживаем BRAK#-префикс для поиска
	cleanBarcode := strings.TrimPrefix(barcode, "BRAK#")

	dashIdx := strings.Index(cleanBarcode, "-")
	if dashIdx == -1 {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат штрихкода"})
		return
	}
	boxIDStr := cleanBarcode[:dashIdx]

	boxID, err := strconv.Atoi(boxIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный ID коробки"})
		return
	}

	var boxQty, boxNumInOrder int
	var boxStatus string
	var orderID int
	err = dbMain.QueryRow(`SELECT ид_заказа, номер_в_заказе, количество, статус FROM коробки WHERE ид = ?`, boxID).Scan(&orderID, &boxNumInOrder, &boxQty, &boxStatus)
	if err != nil {
		respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Коробка не найдена"})
		return
	}

	var orderName, orderCode string
	dbMain.QueryRow(`SELECT название, код FROM заказы WHERE ид = ?`, orderID).Scan(&orderName, &orderCode)

	rows, err := dbMain.Query(`
		SELECT ок.ид, ок.шаг, ок.название, ок.код, ок.статус, ок.ид_исполнителя, ок.норма_времени_сек, ок.брак
		FROM операции_коробки ок
		WHERE ок.ид_коробки = ?
		ORDER BY ок.шаг ASC
	`, boxID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	var operations []map[string]interface{}
	for rows.Next() {
		var opID, step int
		var opName, opCode, status string
		var executorID sql.NullInt64
		var normSec float64
		var brak int
		if rows.Scan(&opID, &step, &opName, &opCode, &status, &executorID, &normSec, &brak) != nil {
			continue
		}
		op := map[string]interface{}{"ид": opID, "шаг": step, "название": opName, "код": opCode, "статус": status, "норма_сек": normSec, "брак": brak}

		if executorID.Valid {
			var fio string
			dbEmployees.QueryRow(`SELECT фио FROM сотрудники WHERE ид = ?`, executorID.Int64).Scan(&fio)
			op["исполнитель"] = fio
		} else {
			op["исполнитель"] = ""
		}
		operations = append(operations, op)
	}

	// Список всех швей для выбора виновного
	empRows, err := dbEmployees.Query(`SELECT ид, фио FROM сотрудники WHERE активный = 1 AND права IN ('worker','master') ORDER BY фио`)
	var employees []map[string]interface{}
	if err == nil {
		defer empRows.Close()
		for empRows.Next() {
			var id int
			var fio string
			if empRows.Scan(&id, &fio) == nil {
				employees = append(employees, map[string]interface{}{"ид": id, "фио": fio})
			}
		}
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Status: "ok",
		Data: map[string]interface{}{
			"ид_коробки":     boxID,
			"изделие":        orderName,
			"количество":     boxQty,
			"статус":         boxStatus,
			"код_заказа":     orderCode,
			"номер_в_заказе": boxNumInOrder,
			"операции":       operations,
			"сотрудники":     employees,
		},
	})
}

func handleOTKApprove(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "otk", "admin")
	if s == nil {
		return
	}
	var req struct {
		BoxID int `json:"ид_коробки"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	var status string
	err := dbMain.QueryRow(`SELECT статус FROM коробки WHERE ид = ?`, req.BoxID).Scan(&status)
	if err != nil {
		respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Коробка не найдена"})
		return
	}
	if status != "ожидает_отк" && status != "брак" {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: fmt.Sprintf("Коробка в статусе '%s', приёмка невозможна", status)})
		return
	}

	dbMain.Exec(`UPDATE коробки SET статус = 'принята' WHERE ид = ?`, req.BoxID)
	dbMain.Exec(`
		UPDATE журнал_выработки
		SET статус_выплаты = 'готово_к_выплате'
		WHERE ид_операции IN (
			SELECT ид FROM операции_коробки WHERE ид_коробки = ?
		) AND статус_выплаты = 'начислено'
	`, req.BoxID)

	log.Printf("ОТК: коробка #%d принята, выплаты готовы", req.BoxID)

	// Аудит
	auditOTK, _ := json.Marshal(map[string]interface{}{
		"ид_коробки": req.BoxID, "статус": "принята", "кем": s.FIO,
	})
	dbMain.Exec(`INSERT INTO аудит (тип, ид_рабочего, данные, создано)
		VALUES ('отк_принята', ?, ?, CURRENT_TIMESTAMP)`,
		s.WorkerID, string(auditOTK))

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Коробка принята. Выплаты готовы."})
}

func handleOTKReject(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "otk", "admin")
	if s == nil {
		return
	}
	var req struct {
		BoxID int `json:"ид_коробки"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	dbMain.Exec(`UPDATE коробки SET статус = 'брак' WHERE ид = ?`, req.BoxID)
	log.Printf("ОТК: коробка #%d забракована", req.BoxID)

	// Аудит
	auditRej, _ := json.Marshal(map[string]interface{}{
		"ид_коробки": req.BoxID, "статус": "брак", "кем": s.FIO,
	})
	dbMain.Exec(`INSERT INTO аудит (тип, ид_рабочего, данные, создано)
		VALUES ('отк_брак', ?, ?, CURRENT_TIMESTAMP)`,
		s.WorkerID, string(auditRej))

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Коробка забракована. Мастер уведомлён."})
}

// handleOTKSplit — разделение коробки на годную и брак
// Принимает: { ид_коробки, годные, брак, виновные: { ид_операции: ид_рабочего } }
func handleOTKSplit(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "otk", "admin")
	if s == nil {
		return
	}
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не допущен"})
		return
	}

	var req struct {
		BoxID  int         `json:"ид_коробки"`
		Good   int         `json:"годные"`
		Defect int         `json:"брак"`
		Blame  map[int]int `json:"виновные,omitempty"` // ид_операции → ид_рабочего
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}
	if req.Good+req.Defect == 0 {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Сумма годных и брака должна быть > 0"})
		return
	}

	// Читаем коробку
	var boxQty, orderID int
	var boxStatus string
	err := dbMain.QueryRow(`SELECT ид_заказа, количество, статус FROM коробки WHERE ид = ?`, req.BoxID).Scan(&orderID, &boxQty, &boxStatus)
	if err != nil {
		respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Коробка не найдена"})
		return
	}
	if req.Good+req.Defect > boxQty {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: fmt.Sprintf("Сумма годных(%d)+брак(%d)=%d превышает количество в коробке %d", req.Good, req.Defect, req.Good+req.Defect, boxQty)})
		return
	}

	tx, err := dbMain.Begin()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка БД"})
		return
	}
	defer tx.Rollback()

	// 1. Годная часть — уменьшаем текущую коробку или оставляем как есть
	if req.Defect > 0 && req.Good > 0 {
		tx.Exec(`UPDATE коробки SET количество = ? WHERE ид = ?`, req.Good, req.BoxID)
	} else if req.Defect > 0 && req.Good == 0 {
		tx.Exec(`UPDATE коробки SET статус = 'брак', количество = ? WHERE ид = ?`, req.Defect, req.BoxID)
	}

	// 2. Начисление: единый проход по всем записям выработки коробки
	if req.Good > 0 || req.Defect > 0 {
		// Читаем все записи выработки ДО изменений
		payRows, err := tx.Query(`SELECT ид, сумма_начислена FROM журнал_выработки WHERE ид_операции IN (SELECT ид FROM операции_коробки WHERE ид_коробки = ?) AND статус_выплаты = 'начислено'`, req.BoxID)
		if err == nil {
			defer payRows.Close()
			for payRows.Next() {
				var jID int
				var sumKop float64
				if payRows.Scan(&jID, &sumKop) != nil {
					continue
				}
				// Распределяем сумму: годные + брак пропорционально, брак с коэфф из formulas.json
				goodPart := 0.0
				brakPart := 0.0
				brakCoeff := 1.0
				formulasMu.RLock()
				if конст, ok := formulas["глобальные_константы"].(map[string]interface{}); ok {
					if pct, ok := конст["оплата_брака_процент"].(float64); ok && pct > 0 {
						brakCoeff = pct / 100.0
					}
				}
				formulasMu.RUnlock()
				if req.Good > 0 {
					goodPart = sumKop * float64(req.Good) / float64(boxQty)
				}
				if req.Defect > 0 {
					brakPart = sumKop * float64(req.Defect) / float64(boxQty) * brakCoeff
				}
				totalPayment := goodPart + brakPart
				tx.Exec(`UPDATE журнал_выработки SET сумма_начислена = ?, статус_выплаты = 'готово_к_выплате' WHERE ид = ?`, math.Round(totalPayment), jID)
			}
		}

		// 2a. Статус коробки для годной части
		if req.Good > 0 {
			tx.Exec(`UPDATE коробки SET статус = 'принята' WHERE ид = ? AND статус != 'брак'`, req.BoxID)
		}
	}

	var orderCode string
	dbMain.QueryRow(`SELECT код FROM заказы WHERE ид = ?`, orderID).Scan(&orderCode)

	// 3. Создаём коробку брака
	var brakBoxID int
	var brakBoxNum int
	if req.Defect > 0 && req.Good > 0 {
		tx.QueryRow(`SELECT COALESCE(MAX(номер_в_заказе), 0) + 1 FROM коробки WHERE ид_заказа = ?`, orderID).Scan(&brakBoxNum)
		resBrak, errBrak := tx.Exec(`INSERT INTO коробки (ид_заказа, количество, статус) VALUES (?, ?, 'в_работе')`,
			orderID, req.Defect)
		if errBrak == nil {
			brakID, _ := resBrak.LastInsertId()
			brakBoxID = int(brakID)
			brakCode := generateBoxCode(orderCode, brakBoxNum, true)
			tx.Exec(`UPDATE коробки SET номер_в_заказе = ?, код = ? WHERE ид = ?`, brakBoxNum, brakCode, brakBoxID)
		}
		// Копируем операции со статусом 'брак' и кодами
		rows, err := tx.Query(`SELECT ид, шаг, название, код, ид_техкарты, норма_времени_сек, разряд FROM операции_коробки WHERE ид_коробки = ? ORDER BY шаг`, req.BoxID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var opDBID, step, tcID, razryad int
				var opName, opCode string
				var normSec float64
				if rows.Scan(&opDBID, &step, &opName, &opCode, &tcID, &normSec, &razryad) == nil {
					if opCode == "" {
						opCode = generateOpCodeFromTc(tcID)
					} else {
						// Вложенный код для брак-коробки
						opCode = generateOpCode(orderCode+"br"+fmt.Sprintf("%05d", brakBoxNum), step)
					}
					tx.Exec(`INSERT INTO операции_коробки (ид_коробки, шаг, название, код, ид_техкарты, норма_времени_сек, разряд, статус) VALUES (?,?,?,?,?,?,?,'брак')`,
						brakBoxID, step, opName, opCode, tcID, normSec, razryad)
				}
			}
		}
	}

	// 4. Отмечаем виновных на операциях
	for opID, workerID := range req.Blame {
		tx.Exec(`UPDATE операции_коробки SET брак = 1, ид_исполнителя = ? WHERE ид = ?`, workerID, opID)
		// Пишем в аудит
		blameJSON, _ := json.Marshal(map[string]interface{}{
			"ид_коробки": req.BoxID, "ид_операции": opID, "виновный": workerID, "отк": s.FIO,
		})
		tx.Exec(`INSERT INTO аудит (тип, ид_рабочего, данные, создано)
			VALUES ('брак_виновный', ?, ?, CURRENT_TIMESTAMP)`, workerID, string(blameJSON))
	}

	// Аудит split
	splitAudit, _ := json.Marshal(map[string]interface{}{
		"ид_коробки": req.BoxID, "годные": req.Good, "брак": req.Defect,
		"брак_коробка": brakBoxID, "кем": s.FIO,
	})
	tx.Exec(`INSERT INTO аудит (тип, ид_рабочего, данные, создано)
		VALUES ('отк_split', ?, ?, CURRENT_TIMESTAMP)`, s.WorkerID, string(splitAudit))

	if err := tx.Commit(); err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка сохранения"})
		return
	}

	log.Printf("🔀 ОТК split: коробка #%d → годные=%d, брак=%d (нов.коробка #%d)", req.BoxID, req.Good, req.Defect, brakBoxID)
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: fmt.Sprintf("Годных: %d, брак: %d отправлен в цех (коробка #%d)", req.Good, req.Defect, brakBoxID), Data: map[string]interface{}{
		"брак_коробка": brakBoxID,
	}})
}

func handleMasterPenalty(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "master", "admin")
	if s == nil {
		return
	}
	var req struct {
		JournalID int `json:"ид_выработки"`
		Penalty   int `json:"штраф"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	// Блокируем UPDATE в транзакции
	tx, err := dbMain.Begin()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка БД"})
		return
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE журнал_выработки SET штраф = ? WHERE ид = ?`, req.Penalty, req.JournalID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Не удалось установить штраф"})
		return
	}

	sumChange := 0.0
	if req.Penalty == 1 {
		var oldSum float64
		tx.QueryRow(`SELECT сумма_начислена FROM журнал_выработки WHERE ид = ?`, req.JournalID).Scan(&oldSum)
		newSum := oldSum * 0.5
		sumChange = oldSum - newSum
		tx.Exec(`UPDATE журнал_выработки SET сумма_начислена = ROUND(сумма_начислена * 0.5, 0) WHERE ид = ?`, req.JournalID)
	}

	// Пишем в аудит — сумма в копейках
	tx.Exec(`INSERT INTO аудит (тип, ид_выработки, ид_рабочего, сумма, данные, создано)
		VALUES ('штраф', ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		req.JournalID, s.WorkerID, -sumChange,
		fmt.Sprintf(`{"штраф":%d,"сумма_списано":%.2f}`, req.Penalty, sumChange))

	if err := tx.Commit(); err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка сохранения"})
		return
	}

	log.Printf("Мастер: штраф=%d для выработки #%d", req.Penalty, req.JournalID)
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Штраф применён"})
}

func handleMasterApprove(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "master", "admin")
	if s == nil {
		return
	}
	var req struct {
		BoxID int `json:"ид_коробки"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	var status string
	dbMain.QueryRow(`SELECT статус FROM коробки WHERE ид = ?`, req.BoxID).Scan(&status)
	if status != "брак" {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Коробка не в браке"})
		return
	}

	dbMain.Exec(`UPDATE коробки SET статус = 'принята' WHERE ид = ?`, req.BoxID)
	dbMain.Exec(`
		UPDATE журнал_выработки
		SET статус_выплаты = 'готово_к_выплате'
		WHERE ид_операции IN (
			SELECT ид FROM операции_коробки WHERE ид_коробки = ?
		) AND статус_выплаты = 'начислено'
	`, req.BoxID)

	log.Printf("Мастер: коробка #%d принята после исправления брака", req.BoxID)

	// Аудит
	auditMasterApprove, _ := json.Marshal(map[string]interface{}{
		"ид_коробки": req.BoxID, "статус": "принята_мастером", "кем": s.FIO,
	})
	dbMain.Exec(`INSERT INTO аудит (тип, ид_рабочего, данные, создано)
		VALUES ('мастер_принял', ?, ?, CURRENT_TIMESTAMP)`,
		s.WorkerID, string(auditMasterApprove))

	respondJSON(w, http.StatusOK, APIResponse{
		Status: "ok", Message: "Коробка принята после исправления. Выплаты готовы."})
}

// ---------------------------------------------------------------------------
// API: FORCE DONE (админ/мастер принудительно завершает операцию)
// ---------------------------------------------------------------------------

func handleAdminForceDone(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "admin", "master")
	if s == nil {
		return
	}
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не допущен"})
		return
	}

	var req struct {
		BoxID    int `json:"ид_коробки"`
		OpID     int `json:"ид_операции,omitempty"`
		WorkerID int `json:"ид_рабочего,omitempty"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	if req.BoxID == 0 {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Требуется ид_коробки"})
		return
	}

	// Если операция не указана — берём первую незавершённую
	if req.OpID == 0 {
		err := dbMain.QueryRow(`
			SELECT ид FROM операции_коробки
			WHERE ид_коробки = ? AND статус IN ('в_работе','ожидает')
			ORDER BY шаг ASC LIMIT 1`, req.BoxID).Scan(&req.OpID)
		if err != nil {
			respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Нет незавершённых операций"})
			return
		}
	}

	// Получаем данные операции
	var opStatus, opName string
	var normSec float64
	var boxID, step, techRazryad int
	var executorID sql.NullInt64
	err := dbMain.QueryRow(`
		SELECT ид_коробки, шаг, название, статус, норма_времени_сек, разряд, ид_исполнителя
		FROM операции_коробки WHERE ид = ?`, req.OpID).Scan(&boxID, &step, &opName, &opStatus, &normSec, &techRazryad, &executorID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Операция не найдена"})
		return
	}

	// Определяем исполнителя: если указан — используем его, иначе текущего в операции, иначе админа
	workerID := req.WorkerID
	if workerID == 0 {
		if executorID.Valid {
			workerID = int(executorID.Int64)
		} else {
			workerID = s.WorkerID
		}
	}

	tx, err := dbMain.Begin()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка БД"})
		return
	}
	defer tx.Rollback()

	// Если операция была 'в_работе' — закрываем журнал
	if opStatus == "в_работе" {
		var journalID int
		var startTime string
		err = tx.QueryRow(`
			SELECT ид, время_начала FROM журнал_выработки
			WHERE ид_операции = ? AND время_окончания IS NULL
			ORDER BY ид DESC LIMIT 1`, req.OpID).Scan(&journalID, &startTime)
		if err == nil {
			tx.Exec(`UPDATE журнал_выработки SET время_окончания = CURRENT_TIMESTAMP WHERE ид = ?`, journalID)

			// Множим норму на количество изделий в коробке
			var boxQtyFD int
			tx.QueryRow(`SELECT количество FROM коробки WHERE ид = ?`, boxID).Scan(&boxQtyFD)
			if boxQtyFD < 1 {
				boxQtyFD = 1
			}

			// Закрываем время — только для лога
			var endTimeStr string
			tx.QueryRow(`SELECT время_окончания FROM журнал_выработки WHERE ид = ?`, journalID).Scan(&endTimeStr)
			startParsed, _ := parseTimeFlexible(startTime)
			endParsed, _ := parseTimeFlexible(endTimeStr)
			actualSec := endParsed.Sub(startParsed).Seconds()
			if actualSec <= 0 {
				actualSec = 1
			}

			// Расчёт оплаты в КОПЕЙКАХ
			formulasMu.RLock()
			baseRateKop := 50.0
			if конст, ok := formulas["глобальные_константы"].(map[string]interface{}); ok {
				if b, ok := конст["оплата_в_секунду_базовая_коп"].(float64); ok {
					baseRateKop = b
				}
			}
			razryadCoeff := 1.0
			if конст, ok := formulas["глобальные_константы"].(map[string]interface{}); ok {
				if коэфф, ok := конст["коэффициенты_разрядов"].(map[string]interface{}); ok {
					if c, ok := коэфф[strconv.Itoa(techRazryad)].(float64); ok {
						razryadCoeff = c
					}
				}
			}
			formulasMu.RUnlock()

			paymentKopFD := baseRateKop * normSec * float64(boxQtyFD) * razryadCoeff

			tx.Exec(`UPDATE журнал_выработки SET сумма_начислена = ?, статус_выплаты = 'начислено' WHERE ид = ?`,
				paymentKopFD, journalID)
		}
	}

	// Завершаем операцию
	tx.Exec(`UPDATE операции_коробки SET статус = 'завершена', ид_исполнителя = COALESCE(NULLIF(ид_исполнителя, 0), ?) WHERE ид = ?`, workerID, req.OpID)

	// Аудит
	auditJSON, _ := json.Marshal(map[string]interface{}{
		"ид_коробки": boxID, "операция": opName, "force_done": true, "админ": s.FIO,
	})
	tx.Exec(`INSERT INTO аудит (тип, ид_выработки, ид_рабочего, данные, создано)
		VALUES ('force_done', ?, ?, ?, CURRENT_TIMESTAMP)`, req.OpID, s.WorkerID, string(auditJSON))

	// Переключаем на следующую операцию или закрываем коробку
	var pending int
	tx.QueryRow(`SELECT COUNT(*) FROM операции_коробки WHERE ид_коробки = ? AND статус != 'завершена'`, boxID).Scan(&pending)
	if pending == 0 {
		tx.Exec(`UPDATE коробки SET статус = 'ожидает_отк', дата_завершения = CURRENT_TIMESTAMP WHERE ид = ?`, boxID)
	}

	if err := tx.Commit(); err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка сохранения"})
		return
	}

	// Обновляем кеш скоростей
	refreshSpeedCache()

	log.Printf("⚡ Force DONE: операция #%d коробки #%d, админ/мастер %s", req.OpID, boxID, s.FIO)
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: fmt.Sprintf("Операция '%s' принудительно завершена.", opName)})
}

// ---------------------------------------------------------------------------
// API: ПЕРЕНАЗНАЧЕНИЕ ИСПОЛНИТЕЛЯ НА ОПЕРАЦИЮ
// ---------------------------------------------------------------------------

func handleOperationReassign(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "admin", "master")
	if s == nil {
		return
	}
	if r.Method != http.MethodPut {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не допущен"})
		return
	}

	var req struct {
		OpID     int `json:"ид_операции"`
		WorkerID int `json:"ид_рабочего"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}
	if req.OpID == 0 || req.WorkerID == 0 {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Требуются ид_операции и ид_рабочего"})
		return
	}

	// Проверяем, что сотрудник существует
	var fio string
	err := dbEmployees.QueryRow(`SELECT фио FROM сотрудники WHERE ид = ? AND активный = 1`, req.WorkerID).Scan(&fio)
	if err != nil {
		respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Рабочий не найден"})
		return
	}

	// Получаем данные операции
	var opStatus string
	var boxID int
	err = dbMain.QueryRow(`SELECT статус, ид_коробки FROM операции_коробки WHERE ид = ?`, req.OpID).Scan(&opStatus, &boxID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Операция не найдена"})
		return
	}

	// Меняем исполнителя — если операция в работе, обновляем и в журнале
	tx, err := dbMain.Begin()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка БД"})
		return
	}
	defer tx.Rollback()

	tx.Exec(`UPDATE операции_коробки SET ид_исполнителя = ? WHERE ид = ?`, req.WorkerID, req.OpID)

	if opStatus == "в_работе" {
		// Переназначаем в журнале — закрываем старую запись, создаём новую от имени нового рабочего
		var journalID int
		err = tx.QueryRow(`
			SELECT ид FROM журнал_выработки
			WHERE ид_операции = ? AND время_окончания IS NULL
			ORDER BY ид DESC LIMIT 1`, req.OpID).Scan(&journalID)
		if err == nil {
			tx.Exec(`UPDATE журнал_выработки SET время_окончания = CURRENT_TIMESTAMP, ид_рабочего = ? WHERE ид = ?`, req.WorkerID, journalID)
			tx.Exec(`INSERT INTO журнал_выработки (ид_операции, ид_рабочего, ид_стола, время_начала)
				VALUES (?, ?, 'admin', CURRENT_TIMESTAMP)`, req.OpID, req.WorkerID)
		} else {
			tx.Exec(`INSERT INTO журнал_выработки (ид_операции, ид_рабочего, ид_стола, время_начала)
				VALUES (?, ?, 'admin', CURRENT_TIMESTAMP)`, req.OpID, req.WorkerID)
		}
	}

	auditJSON, _ := json.Marshal(map[string]interface{}{
		"ид_операции": req.OpID, "ид_коробки": boxID, "новый_рабочий": fio, "админ": s.FIO,
	})
	tx.Exec(`INSERT INTO аудит (тип, ид_рабочего, данные, создано)
		VALUES ('reassign', ?, ?, CURRENT_TIMESTAMP)`, s.WorkerID, string(auditJSON))

	if err := tx.Commit(); err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка сохранения"})
		return
	}

	log.Printf("🔄 Переназначение: операция #%d → рабочий %d (%s), админ/мастер %s", req.OpID, req.WorkerID, fio, s.FIO)
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: fmt.Sprintf("Исполнитель операции изменён на %s", fio)})
}

// ---------------------------------------------------------------------------
// API: LIVE SPEED — Скорость швей онлайн (обнуляется раз в день)
// ---------------------------------------------------------------------------

// speedSnapshot содержит скорость одной операции
type SpeedEntry struct {
	WorkerID   int     `json:"ид_рабочего"`
	FIO        string  `json:"фио"`
	SpeedPct   float64 `json:"скорость_процент"`
	NormSec    float64 `json:"норма_сек"`
	FactSec    float64 `json:"факт_сек"`
	BoxID      int     `json:"ид_коробки"`
	OpName     string  `json:"операция"`
	OpID       int     `json:"ид_операции"`
	UpdatedAt  string  `json:"обновлено"`
	BoxQty     int     `json:"количество"`
	OrderName  string  `json:"заказ"`
	NormPerOne float64 `json:"норма_на_единицу"`
	FactPerOne float64 `json:"факт_на_единицу"`
}

var (
	lastSpeedReset = time.Now().Truncate(24 * time.Hour)
	speedCache     []SpeedEntry
	speedCacheMu   sync.RWMutex
)

// resetSpeedCacheIfNeeded сбрасывает кеш скоростей раз в день
func resetSpeedCacheIfNeeded() {
	now := time.Now().Truncate(24 * time.Hour)
	if now.After(lastSpeedReset) {
		speedCacheMu.Lock()
		lastSpeedReset = now
		speedCache = nil
		speedCacheMu.Unlock()
		log.Println("📊 Кеш скоростей сброшен (новый день)")
	}
}

// refreshSpeedCache обновляет кеш скоростей (вызывается после каждого DONE / force-done)
func refreshSpeedCache() {
	resetSpeedCacheIfNeeded()

	rows, err := dbMain.Query(`
		SELECT жв.ид_рабочего, жв.ид_операции, ок.ид_коробки, ок.название,
		       ок.норма_времени_сек, жв.время_начала, жв.время_окончания
		FROM журнал_выработки жв
		JOIN операции_коробки ок ON жв.ид_операции = ок.ид
		WHERE date(жв.время_начала) = date('now') AND жв.время_окончания IS NOT NULL
		ORDER BY жв.время_окончания DESC
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	var entries []SpeedEntry
	for rows.Next() {
		var wID, opID, boxID int
		var opName string
		var normPerUnit float64
		var startTime, endTime sql.NullString
		if rows.Scan(&wID, &opID, &boxID, &opName, &normPerUnit, &startTime, &endTime) != nil {
			continue
		}
		// Получаем количество в коробке и название заказа
		var boxQtySC int
		var orderID int
		dbMain.QueryRow(`SELECT количество, ид_заказа FROM коробки WHERE ид = ?`, boxID).Scan(&boxQtySC, &orderID)
		var orderName string
		dbMain.QueryRow(`SELECT название FROM заказы WHERE ид = ?`, orderID).Scan(&orderName)

		// Время на коробку целиком
		totalFactSec := 0.0
		if startTime.Valid && endTime.Valid {
			st, _ := parseTimeFlexible(startTime.String)
			et, _ := parseTimeFlexible(endTime.String)
			totalFactSec = et.Sub(st).Seconds()
		}
		// Время на 1 изделие
		factPerUnit := totalFactSec
		if boxQtySC > 1 {
			factPerUnit = totalFactSec / float64(boxQtySC)
		}
		// Скорость считаем по норме на 1 изделие относительно времени на 1 изделие
		speedPct := 0.0
		if factPerUnit > 0 && normPerUnit > 0 {
			speedPct = (normPerUnit / factPerUnit) * 100
		}
		fio := ""
		dbEmployees.QueryRow(`SELECT фио FROM сотрудники WHERE ид = ?`, wID).Scan(&fio)
		entries = append(entries, SpeedEntry{
			WorkerID:   wID,
			FIO:        fio,
			SpeedPct:   speedPct,
			NormSec:    normPerUnit * float64(boxQtySC), // норма на всю коробку
			FactSec:    totalFactSec,                    // факт на всю коробку
			BoxID:      boxID,
			OpName:     opName,
			OpID:       opID,
			UpdatedAt:  endTime.String,
			BoxQty:     boxQtySC,
			OrderName:  orderName,
			NormPerOne: normPerUnit, // норма на 1 изделие
			FactPerOne: factPerUnit, // факт на 1 изделие
		})
	}

	speedCacheMu.Lock()
	speedCache = entries
	speedCacheMu.Unlock()
}

// handleLiveSpeed возвращает кеш скоростей
func handleLiveSpeed(w http.ResponseWriter, r *http.Request) {
	speedCacheMu.RLock()
	data := speedCache
	speedCacheMu.RUnlock()
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: data})
}

// ---------------------------------------------------------------------------
// API: АУДИТ
// ---------------------------------------------------------------------------

func handleAudit(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	rows, err := dbMain.Query(`
		SELECT ид, тип, ид_выработки, ид_рабочего, сумма, данные, создано
		FROM аудит
		WHERE date(создано) = ?
		ORDER BY создано DESC
	`, date)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	var entries []map[string]interface{}
	for rows.Next() {
		var id, workerID int
		var typ string
		var journalID sql.NullInt64
		var sum sql.NullFloat64
		var data, created string
		if rows.Scan(&id, &typ, &journalID, &workerID, &sum, &data, &created) != nil {
			continue
		}
		// Ищем фио рабочего
		fio := ""
		dbEmployees.QueryRow(`SELECT фио FROM сотрудники WHERE ид = ?`, workerID).Scan(&fio)

		entry := map[string]interface{}{
			"ид": id, "тип": typ, "сотрудник": fio,
			"сумма": kopecksToRubles(sum.Float64), "сумма_коп": sum.Float64, "данные": data, "время": created,
		}
		if journalID.Valid {
			entry["ид_выработки"] = journalID.Int64
		}
		entries = append(entries, entry)
	}

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: entries})
}

// ---------------------------------------------------------------------------
// API: СТАТИСТИКА ЗАКАЗОВ (сделано, процент)
// ---------------------------------------------------------------------------

func handleOrderStats(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT з.ид, з.название, з.код, з.количество_план,
			COALESCE(SUM(CASE WHEN к.статус IN ('в_работе','ожидает_отк','брак','принята') THEN к.количество ELSE 0 END), 0) as сделано,
			COALESCE(SUM(CASE WHEN к.статус = 'принята' THEN к.количество ELSE 0 END), 0) as принято
		FROM заказы з
		LEFT JOIN коробки к ON к.ид_заказа = з.ид
		WHERE з.активный = 1
		GROUP BY з.ид
		ORDER BY з.ид
	`
	rows, err := dbMain.Query(query)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	var orders []map[string]interface{}
	for rows.Next() {
		var id, qty, madeQty int
		var name, code string
		var acceptedQty int
		if rows.Scan(&id, &name, &code, &qty, &madeQty, &acceptedQty) == nil {
			pct := 0.0
			if qty > 0 {
				pct = float64(acceptedQty) / float64(qty) * 100
			}
			orders = append(orders, map[string]interface{}{
				"ид": id, "название": name, "код": code, "количество_план": qty,
				"сделано": madeQty, "принято": acceptedQty, "процент": pct,
			})
		}
	}
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: orders})
}

// ---------------------------------------------------------------------------
// API: КОРОБКИ
// ---------------------------------------------------------------------------

func handleBoxes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		statusFilter := r.URL.Query().Get("status")
		query := `SELECT к.ид, к.ид_заказа, з.название, к.количество, к.статус, к.дата_создания
			FROM коробки к JOIN заказы з ON к.ид_заказа = з.ид`
		var args []interface{}
		if statusFilter != "" {
			query += ` WHERE к.статус = ?`
			args = append(args, statusFilter)
		} else {
			query += ` WHERE к.статус IN ('в_работе','ожидает_отк','брак')`
		}
		query += ` ORDER BY к.ид DESC`

		rows, err := dbMain.Query(query, args...)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		defer rows.Close()

		var boxes []map[string]interface{}
		for rows.Next() {
			var id, orderID, qty int
			var name, status, created string
			if rows.Scan(&id, &orderID, &name, &qty, &status, &created) == nil {
				// Проверяем, начаты ли операции
				var startedOps int
				dbMain.QueryRow(`SELECT COUNT(*) FROM операции_коробки WHERE ид_коробки = ? AND статус IN ('в_работе','завершена')`, id).Scan(&startedOps)
				boxes = append(boxes, map[string]interface{}{
					"ид": id, "ид_заказа": orderID, "изделие": name,
					"количество": qty, "статус": status, "дата": created,
					"операции_начаты": startedOps > 0,
				})
			}
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: boxes})

	case http.MethodPost:
		if s := requireRole(r, w, "vydacha", "admin"); s == nil {
			return
		}
		var req struct {
			OrderID int `json:"ид_заказа"`
			Qty     int `json:"количество"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
			return
		}

		var orderName string
		var orderQty int
		var orderCode string
		err := dbMain.QueryRow(`SELECT название, количество_план, код FROM заказы WHERE ид = ? AND активный = 1`, req.OrderID).Scan(&orderName, &orderQty, &orderCode)
		if err != nil {
			respondJSON(w, http.StatusNotFound, APIResponse{Status: "error", Message: "Заказ не найден"})
			return
		}

		// Проверяем что есть техкарта
		var techCount int
		dbMain.QueryRow(`SELECT COUNT(*) FROM техкарты WHERE ид_заказа = ? AND активный = 1`, req.OrderID).Scan(&techCount)
		if techCount == 0 {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "У заказа нет техкарты. Сначала создайте техкарту."})
			return
		}

		// Проверяем остаток: сумма уже созданных коробок + новая ≤ план
		var madeQty int
		dbMain.QueryRow(`SELECT COALESCE(SUM(количество), 0) FROM коробки WHERE ид_заказа = ?`, req.OrderID).Scan(&madeQty)
		if madeQty+req.Qty > orderQty {
			respondJSON(w, http.StatusBadRequest, APIResponse{
				Status:  "error",
				Message: fmt.Sprintf("Превышение плана заказа. Уже создано %d из %d, нельзя добавить %d", madeQty, orderQty, req.Qty),
			})
			return
		}

		result, err := dbMain.Exec(`INSERT INTO коробки (ид_заказа, количество, статус) VALUES (?, ?, 'в_работе')`,
			req.OrderID, req.Qty)
		if err != nil {
			log.Printf("Ошибка создания коробки: %v", err)
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка создания коробки"})
			return
		}

		boxID, _ := result.LastInsertId()

		// Номер коробки в заказе + вложенный код
		var boxNum int
		dbMain.QueryRow(`SELECT COALESCE(MAX(номер_в_заказе), 0) + 1 FROM коробки WHERE ид_заказа = ?`, req.OrderID).Scan(&boxNum)
		boxCode := generateBoxCode(orderCode, boxNum, false)
		dbMain.Exec(`UPDATE коробки SET номер_в_заказе = ?, код = ? WHERE ид = ?`, boxNum, boxCode, boxID)

		rows, err := dbMain.Query(
			`SELECT ид, шаг, код, название_операции, норма_времени_сек, разряд FROM техкарты WHERE ид_заказа = ? AND активный = 1 ORDER BY шаг`,
			req.OrderID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var tcID, step, razryad int
				var opCode, opName string
				var normSec float64
				if rows.Scan(&tcID, &step, &opCode, &opName, &normSec, &razryad) == nil {
					opCodeGen := opCode
					if opCodeGen == "" {
						opCodeGen = generateOpCodeFromTc(tcID)
					} else {
						// Заменяем код операции на вложенный: zkz00001bx00001op00005
						opCodeGen = generateOpCode(boxCode, step)
					}
					dbMain.Exec(
						`INSERT INTO операции_коробки (ид_коробки, шаг, название, ид_техкарты, норма_времени_сек, разряд, статус, код) VALUES (?,?,?,?,?,?,'ожидает',?)`,
						boxID, step, opName, tcID, normSec, razryad, opCodeGen)
				}
			}
		}

		log.Printf("📦 Создана коробка #%d: %s x%d", boxID, orderName, req.Qty)

		// Аудит создания коробки
		auditBoxCreate, _ := json.Marshal(map[string]interface{}{
			"ид_коробки": boxID, "ид_заказа": req.OrderID, "изделие": orderName, "количество": req.Qty,
		})
		dbMain.Exec(`INSERT INTO аудит (тип, данные, создано)
			VALUES ('коробка_создана', ?, CURRENT_TIMESTAMP)`, string(auditBoxCreate))

		respondJSON(w, http.StatusOK, APIResponse{
			Status: "ok", Message: "Коробка создана",
			Data: map[string]interface{}{"ид_коробки": boxID, "изделие": orderName, "количество": req.Qty},
		})

	case http.MethodPut:
		if s := requireRole(r, w, "vydacha", "admin"); s == nil {
			return
		}
		var req struct {
			ID  int `json:"ид"`
			Qty int `json:"количество"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
			return
		}
		if req.Qty < 1 {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Количество должно быть больше 0"})
			return
		}
		// Защита: проверяем, что ни одна операция не начата
		var startedOps int
		dbMain.QueryRow(`SELECT COUNT(*) FROM операции_коробки WHERE ид_коробки = ? AND статус IN ('в_работе','завершена')`, req.ID).Scan(&startedOps)
		if startedOps > 0 {
			respondJSON(w, http.StatusForbidden, APIResponse{Status: "error", Message: "Нельзя изменить коробку, по которой уже начаты операции"})
			return
		}
		_, err := dbMain.Exec(`UPDATE коробки SET количество = ? WHERE ид = ? AND статус = 'в_работе'`, req.Qty, req.ID)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка обновления"})
			return
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Количество обновлено"})

	case http.MethodDelete:
		if s := requireRole(r, w, "vydacha", "admin"); s == nil {
			return
		}
		boxID := r.URL.Query().Get("id")
		if boxID == "" {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Требуется id"})
			return
		}
		// Soft-delete: ставим статус 'удалена' вместо физического удаления
		var startedOps int
		dbMain.QueryRow(`SELECT COUNT(*) FROM операции_коробки WHERE ид_коробки = ? AND статус IN ('в_работе','завершена')`, boxID).Scan(&startedOps)
		if startedOps > 0 {
			respondJSON(w, http.StatusForbidden, APIResponse{Status: "error", Message: "Нельзя удалить коробку, по которой уже начаты операции"})
			return
		}
		_, err := dbMain.Exec(`UPDATE коробки SET статус = 'удалена' WHERE ид = ? AND статус = 'в_работе'`, boxID)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка удаления"})
			return
		}
		// Аудит удаления
		auditDel, _ := json.Marshal(map[string]interface{}{"ид_коробки": boxID, "статус": "удалена"})
		dbMain.Exec(`INSERT INTO аудит (тип, данные, создано)
			VALUES ('коробка_удалена', ?, CURRENT_TIMESTAMP)`, string(auditDel))
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Коробка помечена как удалённая"})

	default:
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не поддерживается"})
	}
}

// ---------------------------------------------------------------------------
// API: СТАТИСТИКА РАБОЧЕГО ЗА СЕГОДНЯ
// ---------------------------------------------------------------------------

func handleWorkerStats(w http.ResponseWriter, r *http.Request) {
	s := getSession(r)
	if s == nil {
		respondJSON(w, http.StatusUnauthorized, APIResponse{Status: "error", Message: "Не авторизован"})
		return
	}

	rows, err := dbMain.Query(`
		SELECT жв.ид_операции, ок.ид_коробки, ок.название, ок.норма_времени_сек,
		       жв.время_начала, жв.время_окончания, жв.сумма_начислена
		FROM журнал_выработки жв
		JOIN операции_коробки ок ON жв.ид_операции = ок.ид
		WHERE жв.ид_рабочего = ? AND date(жв.время_начала) = date('now')
		ORDER BY жв.время_начала DESC
	`, s.WorkerID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка БД"})
		return
	}
	defer rows.Close()

	var operations []map[string]interface{}
	var total float64
	for rows.Next() {
		var opID, boxID int
		var opName string
		var normSec, sum float64
		var startTime, endTime sql.NullString
		if rows.Scan(&opID, &boxID, &opName, &normSec, &startTime, &endTime, &sum) != nil {
			continue
		}
		total += sum
		// Считаем факт. время
		factSec := 0.0
		if startTime.Valid && endTime.Valid {
			st, _ := parseTimeFlexible(startTime.String)
			et, _ := parseTimeFlexible(endTime.String)
			factSec = et.Sub(st).Seconds()
		}
		operations = append(operations, map[string]interface{}{
			"ид_операции": opID, "ид_коробки": boxID, "название": opName,
			"норма_сек": normSec, "факт_сек": factSec,
			"начислено": kopecksToRubles(sum), "время": startTime.String,
		})
	}

	respondJSON(w, http.StatusOK, APIResponse{
		Status: "ok",
		Data:   map[string]interface{}{"total": kopecksToRubles(total), "operations": operations},
	})
}

// handleProductionStats — выработка всех швей за сегодня (для админа/мастера)
func handleProductionStats(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	rows, err := dbMain.Query(`
		SELECT жв.ид_рабочего, жв.ид_операции, ок.ид_коробки, ок.название,
		       ок.норма_времени_сек, жв.время_начала, жв.время_окончания, жв.сумма_начислена
		FROM журнал_выработки жв
		JOIN операции_коробки ок ON жв.ид_операции = ок.ид
		WHERE date(жв.время_начала) = ?
		ORDER BY жв.ид_рабочего, жв.время_начала DESC
	`, date)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	// Группируем по рабочим
	type opStat struct {
		OpID     int     `json:"ид_операции"`
		BoxID    int     `json:"ид_коробки"`
		OpName   string  `json:"название"`
		NormSec  float64 `json:"норма_сек"`
		FactSec  float64 `json:"факт_сек"`
		Sum      float64 `json:"начислено"`
		SpeedPct float64 `json:"скорость_процент"`
		Time     string  `json:"время"`
	}
	type workerStats struct {
		WorkerID int      `json:"ид"`
		FIO      string   `json:"фио"`
		TabNo    string   `json:"табельный"`
		Total    float64  `json:"итого"`
		AvgSpeed float64  `json:"средняя_скорость"`
		OpCount  int      `json:"операций"`
		Ops      []opStat `json:"операции"`
	}

	byWorker := make(map[int]*workerStats)
	for rows.Next() {
		var wID, opID, boxID int
		var opName, startTime, endTime sql.NullString
		var normSec, sum float64
		if rows.Scan(&wID, &opID, &boxID, &opName, &normSec, &startTime, &endTime, &sum) != nil {
			continue
		}
		// Множим норму на количество изделий в коробке
		var boxQtyPS int
		dbMain.QueryRow(`SELECT количество FROM коробки WHERE ид = ?`, boxID).Scan(&boxQtyPS)
		normForSpeed := normSec
		if boxQtyPS > 1 {
			normForSpeed = normSec * float64(boxQtyPS)
		}
		if byWorker[wID] == nil {
			fio := ""
			tabNo := ""
			dbEmployees.QueryRow(`SELECT фио, табельный_номер FROM сотрудники WHERE ид = ?`, wID).Scan(&fio, &tabNo)
			byWorker[wID] = &workerStats{
				WorkerID: wID, FIO: fio, TabNo: tabNo,
			}
		}
		ws := byWorker[wID]
		ws.Total += sum
		ws.OpCount++

		factSec := 0.0
		speedPct := 0.0
		if startTime.Valid && endTime.Valid {
			st, _ := parseTimeFlexible(startTime.String)
			et, _ := parseTimeFlexible(endTime.String)
			factSec = et.Sub(st).Seconds()
			if factSec > 0 && normForSpeed > 0 {
				speedPct = (normForSpeed / factSec) * 100
			}
		}
		ws.AvgSpeed += speedPct
		ws.Ops = append(ws.Ops, opStat{
			OpID: opID, BoxID: boxID, OpName: opName.String,
			NormSec: normSec, FactSec: factSec, Sum: kopecksToRubles(sum),
			SpeedPct: speedPct, Time: startTime.String,
		})
	}

	var result []*workerStats
	for _, ws := range byWorker {
		if ws.OpCount > 0 {
			ws.AvgSpeed = ws.AvgSpeed / float64(ws.OpCount)
		}
		ws.Total = kopecksToRubles(ws.Total)
		result = append(result, ws)
	}

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: result})
}
