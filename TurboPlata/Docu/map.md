TurboPlata/
├── GO/                    # Сервер + PC-агент
│   ├── main.go            # Запускатор + graceful shutdown
│   ├── logger.go          # Логирование
│   ├── database.go        # SQLite подключение + миграции
│   ├── config.go          # Загрузка JSON-конфигов
│   ├── backend_html.go    # Сессии, HTML-страницы, логин
│   ├── api.go             # MQTT, WebSocket, REST API
│   ├── logic.go           # Вся бизнес-логика + аудит
│   ├── routines.go        # Фоновые задачи (бэкап, очистка, экспорт)
│   ├── PC/                # PC-агент (localhost:9999)
│   │   ├── main.go, server.go
│   │   ├── service_windows.go, service_unix.go
│   │   └── go.mod
│   └── scanner/           # Дебаг-сканер (консоль)
│
├── HTML/                  # Веб-интерфейс
│   ├── login.html, admin.html, vydacha.html
│   ├── master.html, shveya.html, otk.html
│   ├── print.html         # Печать маршрутников
│   ├── style.css, app.js
│
├── JSON/                  # Конфиги
│   ├── schema.json        # Схема БД
│   ├── formulas.json      # Формулы оплаты
│   ├── employees.json     # Сотрудники + права
│   └── terminals.json     # Привязка ESP32 ↔ ПК
│
├── Docu/docu.md           # Документация
├── firmware/scanner.ino   # Прошивка ESP32 (MQTT-сканер)
└── TEST/                  # Пре-релизные билды