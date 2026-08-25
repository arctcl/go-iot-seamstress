package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"
)

// Глобальные переменные
var (
	DEBUG_LOGGING = 1

	// Три базы данных
	dbMain       *sql.DB // основная: заказы, техкарты, коробки, операции, журнал
	dbEmployees  *sql.DB // сотрудники
	dbWorkplaces *sql.DB // рабочие места

	// Загружаемые конфиги
	formulas   map[string]interface{}
	formulasMu sync.RWMutex

	employees   map[string]interface{}
	employeesMu sync.RWMutex

	schema   map[string]interface{}
	schemaMu sync.RWMutex

	// MQTT
	mqttClient mqtt.Client

	// Привязка стол → рабочий (esp_hostname → табельный_номер)
	currentWorker   = make(map[string]string)
	currentWorkerMu sync.RWMutex

	// WebSocket-соединения для Go-клиентов (pc_hostname → соединение)
	wsClients   = make(map[string]*websocket.Conn)
	wsClientsMu sync.RWMutex

	// Терминальные сессии
	termSessions   = make(map[string]interface{})
	termSessionsMu sync.RWMutex
)

// Структуры данных

// ScanMessage — входящее сканирование с ESP32 или веб-терминала
type ScanMessage struct {
	Barcode string `json:"barcode"`
}

// APIResponse — стандартный ответ API
type APIResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// CreateBoxRequest — запрос на создание коробки от выдачи
type CreateBoxRequest struct {
	OrderID  int `json:"ид_заказа"`
	Quantity int `json:"количество"`
	WorkerID int `json:"ид_рабочего"`
}

// WSMessage — сообщение WebSocket
type WSMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// Workplace — запись из таблицы рабочие_места
type Workplace struct {
	ID          int
	ESPHostname string
	PCHostname  string
	Role        string
	Description string
	Active      int
}

func main() {
	// Настройка отладочного логирования
	setupLogging()

	// Загрузка конфигурационных файлов
	if err := loadConfigs(); err != nil {
		log.Fatalf("Ошибка загрузки конфигов: %v", err)
	}

	// Загрузка HTML шаблонов
	if err := loadHTMLTemplates(); err != nil {
		log.Printf("Предупреждение: не удалось загрузить HTML шаблоны: %v", err)
	}

	// Инициализация БД
	if err := initDatabase(); err != nil {
		log.Fatalf("Ошибка инициализации БД: %v", err)
	}
	// Импортируем сотрудников из JSON в БД (если их там ещё нет)
	seedEmployeesFromJSON()

	// Инициализация MQTT
	if err := initMQTT(); err != nil {
		log.Printf("Предупреждение: MQTT не инициализирован: %v", err)
	}

	// Запуск фоновых горутин
	go backupRoutine()
	go cleanupRoutine()
	go nightlyExportRoutine()

	// Статика
	http.HandleFunc("/static/", serveStatic)

	// API
	http.HandleFunc("/api/scan", handleScan)
	http.HandleFunc("/api/orders", handleOrders)
	http.HandleFunc("/api/techcards", handleTechCards)
	http.HandleFunc("/api/workplaces", handleWorkplaces)
	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/auth", handleAuth)
	http.HandleFunc("/api/ws", handleWebSocket)
	http.HandleFunc("/api/ws/browser", handleBrowserWS)
	http.HandleFunc("/api/employees", handleEmployees)
	http.HandleFunc("/api/otk/box", handleOTKBoxDetails)
	http.HandleFunc("/api/otk/approve", handleOTKApprove)
	http.HandleFunc("/api/otk/reject", handleOTKReject)
	http.HandleFunc("/api/otk/split", handleOTKSplit)
	http.HandleFunc("/api/master/penalty", handleMasterPenalty)
	http.HandleFunc("/api/master/approve", handleMasterApprove)
	http.HandleFunc("/api/admin/force-done", handleAdminForceDone)
	http.HandleFunc("/api/operations/reassign", handleOperationReassign)
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/logout", handleLogout)
	http.HandleFunc("/api/session", handleSession)
	http.HandleFunc("/api/boxes", handleBoxes)
	http.HandleFunc("/api/orders/stats", handleOrderStats)
	http.HandleFunc("/api/worker/stats", handleWorkerStats)
	http.HandleFunc("/api/production", handleProductionStats)
	http.HandleFunc("/api/audit", handleAudit)
	http.HandleFunc("/api/live-speed", handleLiveSpeed)
	http.HandleFunc("/api/techcards/reorder", handleTechcardReorder)
	http.HandleFunc("/api/stat/log", handleStatLog)
	http.HandleFunc("/api/stat/speed", handleStatSpeed)
	http.HandleFunc("/api/stat/export", handleStatExport)
	http.HandleFunc("/api/stat/filters", handleStatFilters)

	// Веб интерфейсы
	http.HandleFunc("/login", serveLogin)
	http.HandleFunc("/admin", servePage("admin.html", "admin"))
	http.HandleFunc("/vydacha", servePage("vydacha.html", "vydacha", "admin"))
	http.HandleFunc("/master", servePage("master.html", "master", "admin"))
	http.HandleFunc("/shveya", servePage("shveya.html", "worker", "admin"))
	http.HandleFunc("/print", servePage("print.html", "admin", "vydacha"))
	http.HandleFunc("/otk", servePage("otk.html", "otk", "admin"))
	http.HandleFunc("/statistics", servePage("statistics.html", "admin", "master"))
	http.HandleFunc("/", serve404)

	// Порт из окружения или 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// HTTP-сервер с graceful shutdown
	srv := &http.Server{Addr: ":" + port}

	// Канал для сигналов ОС
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("🚀 Сервер запущен на http://localhost:%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Ошибка сервера: %v", err)
		}
	}()

	<-quit
	log.Println("🛑 Получен сигнал остановки, завершаем работу...")

	// Закрываем MQTT
	if mqttClient != nil {
		mqttClient.Disconnect(250)
		log.Println("MQTT отключён")
	}

	// Закрываем WebSocket-соединения
	wsClientsMu.Lock()
	for host, conn := range wsClients {
		conn.Close()
		log.Printf("WebSocket %s закрыт", host)
	}
	wsClientsMu.Unlock()

	// Закрываем БД
	if dbMain != nil {
		dbMain.Close()
		log.Println("БД turboplata.db закрыта")
	}
	if dbEmployees != nil {
		dbEmployees.Close()
	}
	if dbWorkplaces != nil {
		dbWorkplaces.Close()
	}

	// Graceful shutdown HTTP
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Ошибка при остановке сервера: %v", err)
	}
	log.Println("✅ Сервер остановлен")
}
