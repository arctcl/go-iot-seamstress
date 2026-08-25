package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// УТИЛИТЫ
// ---------------------------------------------------------------------------

// respondJSON отправляет JSON ответ
func respondJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

// ---------------------------------------------------------------------------
// MQTT
// ---------------------------------------------------------------------------

// getMQTTPort возвращает порт MQTT из конфигурации
func getMQTTPort() int {
	formulasMu.RLock()
	defer formulasMu.RUnlock()
	if mqttRaw, ok := formulas["mqtt"].(map[string]interface{}); ok {
		if port, ok := mqttRaw["порт"].(float64); ok {
			return int(port)
		}
	}
	return 1883
}

// initMQTT инициализирует подключение к MQTT брокеру
func initMQTT() error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://localhost:%d", getMQTTPort()))
	opts.SetClientID("turboplata-server")
	opts.SetDefaultPublishHandler(mqttMessageHandler)
	opts.OnConnect = mqttConnectHandler
	opts.OnConnectionLost = mqttConnectLostHandler

	mqttClient = mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}

	// Подписываемся на топики всех столов
	topic := "factory/table/+/scan"
	formulasMu.RLock()
	if formulasRaw, ok := formulas["mqtt"].(map[string]interface{}); ok {
		if t, ok := formulasRaw["топик_сканирования"].(string); ok {
			topic = t
		}
	}
	formulasMu.RUnlock()
	token := mqttClient.Subscribe(topic, 1, mqttMessageHandler)
	token.Wait()

	log.Println("MQTT подключен, топик:", topic)
	return nil
}

// mqttMessageHandler обрабатывает входящие MQTT сообщения
func mqttMessageHandler(client mqtt.Client, msg mqtt.Message) {
	var scan ScanMessage
	if err := json.Unmarshal(msg.Payload(), &scan); err != nil {
		log.Printf("Ошибка парсинга MQTT: %v", err)
		return
	}

	topicParts := strings.Split(msg.Topic(), "/")
	var espHostname string
	if len(topicParts) >= 3 {
		espHostname = topicParts[2]
	}

	log.Printf("MQTT сканирование: %s на %s (esp=%s)", scan.Barcode, msg.Topic(), espHostname)

	result := processScanWithContext(scan.Barcode, espHostname)

	if espHostname != "" {
		buzzerTopic := fmt.Sprintf("factory/table/%s/buzzer", espHostname)
		formulasMu.RLock()
		if formulasRaw, ok := formulas["mqtt"].(map[string]interface{}); ok {
			if tpl, ok := formulasRaw["топик_зуммера"].(string); ok {
				buzzerTopic = strings.Replace(tpl, "{hostname}", espHostname, 1)
			}
		}
		formulasMu.RUnlock()
		beepPayload := `{"beep_ms":200}`
		mqttClient.Publish(buzzerTopic, 1, false, beepPayload)
	}

	if result.Status == "ok" && result.Data != nil {
		var pcHostname string
		dbWorkplaces.QueryRow(`SELECT pc_hostname FROM рабочие_места WHERE esp_hostname = ?`, espHostname).Scan(&pcHostname)
		if pcHostname != "" {
			pushToClient(pcHostname, "scan_result", result)
		}
	}
}

// mqttConnectHandler вызывается при успешном подключении к MQTT
func mqttConnectHandler(client mqtt.Client) {
	log.Println("MQTT подключен")
}

// mqttConnectLostHandler вызывается при потере соединения с MQTT
func mqttConnectLostHandler(client mqtt.Client, err error) {
	log.Printf("MQTT соединение потеряно: %v", err)
}

// ---------------------------------------------------------------------------
// WEBSOCKET
// ---------------------------------------------------------------------------

// pushToClient отправляет данные через WebSocket (Go-клиент или браузер)
func pushToClient(pcHostname, msgType string, payload interface{}) {
	wsClientsMu.RLock()
	conn, ok := wsClients[pcHostname]
	wsClientsMu.RUnlock()
	if !ok {
		log.Printf("WebSocket-клиент %s не подключён, push пропущен", pcHostname)
		return
	}
	msg := WSMessage{Type: msgType, Payload: payload}
	if err := conn.WriteJSON(msg); err != nil {
		log.Printf("Ошибка отправки WebSocket для %s: %v", pcHostname, err)
	}
}

// upgrader для WebSocket
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// handleWebSocket обрабатывает WebSocket соединения (Go-клиенты)
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var regMsg WSMessage
	if conn.ReadJSON(&regMsg) != nil {
		return
	}
	if regMsg.Type != "register" {
		return
	}
	hostname, _ := regMsg.Payload.(map[string]interface{})["hostname"].(string)
	if hostname == "" {
		return
	}

	wsClientsMu.Lock()
	wsClients[hostname] = conn
	wsClientsMu.Unlock()
	log.Printf("WebSocket-клиент %s подключён", hostname)

	for {
		var msg WSMessage
		if conn.ReadJSON(&msg) != nil {
			break
		}
	}

	wsClientsMu.Lock()
	delete(wsClients, hostname)
	wsClientsMu.Unlock()
	log.Printf("WebSocket-клиент %s отключён", hostname)
}

// handleBrowserWS — WebSocket для браузера: hostname из query, проверка сессии
func handleBrowserWS(w http.ResponseWriter, r *http.Request) {
	// Проверяем сессию
	s := getSession(r)
	if s == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	hostname := r.URL.Query().Get("hostname")
	if hostname == "" {
		conn.Close()
		return
	}

	wsClientsMu.Lock()
	wsClients[hostname] = conn
	wsClientsMu.Unlock()
	log.Printf("🖥️ Браузер %s подключён (сессия: %s)", hostname, s.FIO)

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	wsClientsMu.Lock()
	delete(wsClients, hostname)
	wsClientsMu.Unlock()
	log.Printf("🖥️ Браузер %s отключён", hostname)
}

// ---------------------------------------------------------------------------
// REST API
// ---------------------------------------------------------------------------

// handleOrders обрабатывает запросы к заказам
func handleOrders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// GET — все могут смотреть
		rows, err := dbMain.Query(`SELECT ид, название, код, количество_план FROM заказы WHERE активный = 1`)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		defer rows.Close()
		var orders []map[string]interface{}
		for rows.Next() {
			var id, qty int
			var name string
			var code sql.NullString
			if rows.Scan(&id, &name, &code, &qty) == nil {
				codeStr := code.String
				if codeStr == "" {
					codeStr = generateOrderCode(id)
				}
				orders = append(orders, map[string]interface{}{"ид": id, "название": name, "код": codeStr, "количество_план": qty})
			}
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: orders})

	case http.MethodPost:
		if s := requireRole(r, w, "admin"); s == nil {
			return
		}
		var orderData struct {
			Name string `json:"название"`
			Qty  int    `json:"количество_план"`
		}
		if json.NewDecoder(r.Body).Decode(&orderData) != nil {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат JSON"})
			return
		}
		res, err := dbMain.Exec(`INSERT INTO заказы (название, количество_план, активный) VALUES (?, ?, 1)`, orderData.Name, orderData.Qty)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		lastID, _ := res.LastInsertId()
		// Обновляем код с реальным ID
		code := generateOrderCode(int(lastID))
		dbMain.Exec(`UPDATE заказы SET код = ? WHERE ид = ?`, code, lastID)
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Заказ создан", Data: map[string]interface{}{"ид": lastID, "код": code}})

	case http.MethodDelete:
		if s := requireRole(r, w, "admin"); s == nil {
			return
		}
		orderID := r.URL.Query().Get("id")
		dbMain.Exec(`UPDATE заказы SET активный = 0 WHERE ид = ?`, orderID)
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Заказ деактивирован"})

	default:
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не поддерживается"})
	}
}

// handleAuth обрабатывает авторизацию
func handleAuth(w http.ResponseWriter, r *http.Request) {
	tabID := r.URL.Query().Get("tab")

	if tabID == "" {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Требуется идентификатор стола"})
		return
	}

	log.Printf("Авторизация для стола: %s", tabID)

	// web_terminal — встроенный веб-интерфейс, не требует регистрации в БД рабочих мест
	var role string
	var err error
	if tabID == "web_terminal" {
		role = "web"
	} else {
		// Ищем рабочее место в БД
		err = dbWorkplaces.QueryRow(`SELECT роль_места FROM рабочие_места WHERE esp_hostname = ? OR pc_hostname = ?`, tabID, tabID).Scan(&role)
		if err != nil {
			log.Printf("❌ Рабочее место не найдено для %s", tabID)
			respondJSON(w, http.StatusOK, APIResponse{Status: "needs_auth", Message: "Рабочее место не найдено. Авторизуйтесь."})
			return
		}
	}

	log.Printf("Роль рабочего места: %s", role)

	currentWorkerMu.RLock()
	workerTabNo, loggedIn := currentWorker[tabID]
	currentWorkerMu.RUnlock()

	log.Printf("Статус рабочего: logged_in=%v, tab_no=%s", loggedIn, workerTabNo)

	if !loggedIn || workerTabNo == "" {
		respondJSON(w, http.StatusOK, APIResponse{
			Status:  "needs_auth",
			Message: "Пожалуйста, отсканируйте ваш табельный номер.",
		})
		return
	}

	var empID int
	var fio, empRole string
	err = dbEmployees.QueryRow(`SELECT ид, фио, права FROM сотрудники WHERE табельный_номер = ?`, workerTabNo).Scan(&empID, &fio, &empRole)
	if err != nil {
		log.Printf("❌ Ошибка данных сотрудника для %s: %v", workerTabNo, err)
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка данных сотрудника"})
		return
	}

	log.Printf("Сотрудник: id=%d, name=%s, role=%s", empID, fio, empRole)

	// Особое условие для администратора
	if empRole == "admin" || role == "admin" {
		log.Printf("✅ Авторизация администратора %s", fio)
		respondJSON(w, http.StatusOK, APIResponse{
			Status:  "ok",
			Message: "Авторизация администратора успешна",
			Data:    map[string]interface{}{"role": empRole, "fio": fio, "страница": "admin"},
		})
		return
	}

	// Для остальных ролей проверяем соответствие роли рабочего места
	if role != empRole {
		log.Printf("❌ Роль сотрудника %s не соответствует роли рабочего места %s", empRole, role)
		respondJSON(w, http.StatusForbidden, APIResponse{
			Status:  "error",
			Message: "Роль сотрудника не соответствует роли рабочего места",
		})
		return
	}

	// Страница по правам
	allowedPage := "vydacha"
	employeesMu.RLock()
	if rightsMap, ok := employees["права"].(map[string]interface{}); ok {
		if rights, ok := rightsMap[role].(map[string]interface{}); ok {
			if pages, ok := rights["доступные_страницы"].([]interface{}); ok && len(pages) > 0 {
				allowedPage = pages[0].(string)
			}
		}
	}
	employeesMu.RUnlock()

	respondJSON(w, http.StatusOK, APIResponse{
		Status:  "ok",
		Message: fmt.Sprintf("%s авторизован.", fio),
		Data: map[string]interface{}{
			"ид_сотрудника": empID,
			"фио":           fio,
			"роль":          role,
			"страница":      allowedPage,
		},
	})
}

// handleTechCards обрабатывает запросы к техкартам
func handleTechCards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		orderID := r.URL.Query().Get("ид_заказа")
		if orderID == "" {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Требуется параметр ид_заказа"})
			return
		}
		rows, err := dbMain.Query(`SELECT ид, шаг, код, название_операции, норма_времени_сек, разряд FROM техкарты WHERE ид_заказа = ? AND активный = 1 ORDER BY шаг`, orderID)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		defer rows.Close()
		var steps []map[string]interface{}
		for rows.Next() {
			var id, step, razryad int
			var name, code string
			var normSec float64
			if rows.Scan(&id, &step, &code, &name, &normSec, &razryad) == nil {
				steps = append(steps, map[string]interface{}{"ид": id, "шаг": step, "код": code, "название_операции": name, "норма_времени_сек": normSec, "разряд": razryad})
			}
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: steps})

	case http.MethodPost:
		if s := requireRole(r, w, "admin"); s == nil {
			return
		}
		var step struct {
			OrderID     int     `json:"ид_заказа"`
			Step        int     `json:"шаг"`
			OpName      string  `json:"название_операции"`
			TimeNormSec float64 `json:"норма_времени_сек"`
			Razryad     int     `json:"разряд"`
		}
		if json.NewDecoder(r.Body).Decode(&step) != nil {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат JSON"})
			return
		}
		// Проверяем что заказ существует
		var orderExists int
		dbMain.QueryRow(`SELECT COUNT(*) FROM заказы WHERE ид = ? AND активный = 1`, step.OrderID).Scan(&orderExists)
		if orderExists == 0 {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Заказ не найден"})
			return
		}
		// Авто-шаг если не указан
		if step.Step <= 0 {
			dbMain.QueryRow(`SELECT COALESCE(MAX(шаг), 0) + 1 FROM техкарты WHERE ид_заказа = ? AND активный = 1`, step.OrderID).Scan(&step.Step)
		}
		// Валидация разряда — читаем доступные из formulas.json
		formulasMu.RLock()
		validRazryad := false
		if конст, ok := formulas["глобальные_константы"].(map[string]interface{}); ok {
			if коэфф, ok := конст["коэффициенты_разрядов"].(map[string]interface{}); ok {
				if _, ok := коэфф[strconv.Itoa(step.Razryad)]; ok {
					validRazryad = true
				}
			}
		}
		formulasMu.RUnlock()
		if !validRazryad {
			step.Razryad = 1
		}
		if step.TimeNormSec <= 0 {
			step.TimeNormSec = 60
		}
		res, err := dbMain.Exec(`INSERT INTO техкарты (ид_заказа, шаг, код, название_операции, норма_времени_сек, разряд, активный) VALUES (?,?,?,?,?,?,1)`,
			step.OrderID, step.Step, generateOpCodeFromTc(0), step.OpName, step.TimeNormSec, step.Razryad)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		lastID, _ := res.LastInsertId()
		code := generateOpCodeFromTc(int(lastID))
		dbMain.Exec(`UPDATE техкарты SET код = ? WHERE ид = ?`, code, lastID)
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Шаг техкарты добавлен", Data: map[string]interface{}{"ид": lastID}})

	case http.MethodDelete:
		if s := requireRole(r, w, "admin"); s == nil {
			return
		}
		stepID := r.URL.Query().Get("id")
		dbMain.Exec(`UPDATE техкарты SET активный = 0 WHERE ид = ?`, stepID)
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Шаг техкарты удалён"})

	default:
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не поддерживается"})
	}
}

// handleWorkplaces обрабатывает запросы к рабочим местам
func handleWorkplaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := dbWorkplaces.Query(`SELECT ид, esp_hostname, pc_hostname, роль_места, описание, активное FROM рабочие_места`)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		defer rows.Close()
		var places []map[string]interface{}
		for rows.Next() {
			var id int
			var esp, pc, role, desc string
			var active int
			if rows.Scan(&id, &esp, &pc, &role, &desc, &active) == nil {
				places = append(places, map[string]interface{}{"ид": id, "esp_hostname": esp, "pc_hostname": pc, "роль_места": role, "описание": desc, "активное": active})
			}
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: places})

	case http.MethodPost:
		var wp struct {
			ESP  string `json:"esp_hostname"`
			PC   string `json:"pc_hostname"`
			Role string `json:"роль_места"`
			Desc string `json:"описание"`
		}
		if json.NewDecoder(r.Body).Decode(&wp) != nil {
			respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат JSON"})
			return
		}
		_, err := dbWorkplaces.Exec(`INSERT INTO рабочие_места (esp_hostname, pc_hostname, роль_места, описание, активное) VALUES (?,?,?,?,1)`,
			wp.ESP, wp.PC, wp.Role, wp.Desc)
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
			return
		}
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Рабочее место добавлено"})

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		dbWorkplaces.Exec(`DELETE FROM рабочие_места WHERE ид = ?`, id)
		respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Рабочее место удалено"})

	default:
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не поддерживается"})
	}
}

// handleTechcardReorder меняет порядок шагов техкарты (drag-drop)
func handleTechcardReorder(w http.ResponseWriter, r *http.Request) {
	s := requireRole(r, w, "admin")
	if s == nil {
		return
	}
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не допущен"})
		return
	}
	var req struct {
		Steps []int `json:"шаги"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}
	if len(req.Steps) == 0 {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Пустой список шагов"})
		return
	}
	tx, err := dbMain.Begin()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка БД"})
		return
	}
	defer tx.Rollback()
	for i, stepID := range req.Steps {
		newStep := i + 1 // 1,2,3... напрямую
		if _, err := tx.Exec(`UPDATE техкарты SET шаг = ? WHERE ид = ?`, newStep, stepID); err != nil {
			respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка обновления порядка"})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: "Ошибка сохранения"})
		return
	}
	log.Printf("📄 Техкарта пересортирована администратором %s", s.FIO)
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Порядок шагов обновлён"})
}

// ---------------------------------------------------------------------------
// РЕГИСТРАЦИЯ КОРОБКИ В АУДИТЕ
