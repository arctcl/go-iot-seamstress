package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// УПРАВЛЕНИЕ СЕССИЯМИ (cookie-based)
// ---------------------------------------------------------------------------

type Session struct {
	WorkerID  int
	TabNo     string
	FIO       string
	Role      string
	CreatedAt time.Time
}

var (
	sessions   = make(map[string]*Session)
	sessionsMu sync.RWMutex
)

func generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func getSession(r *http.Request) *Session {
	c, err := r.Cookie("session_id")
	if err != nil {
		return nil
	}
	sessionsMu.RLock()
	s, ok := sessions[c.Value]
	sessionsMu.RUnlock()
	if !ok {
		return nil
	}
	if time.Since(s.CreatedAt) > 24*time.Hour {
		sessionsMu.Lock()
		delete(sessions, c.Value)
		sessionsMu.Unlock()
		return nil
	}
	return s
}

func setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name: "session_id", Value: sessionID, Path: "/",
		MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: "session_id", Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true,
	})
}

// ---------------------------------------------------------------------------
// СТАТИКА И СТРАНИЦЫ
// ---------------------------------------------------------------------------

func serveStatic(w http.ResponseWriter, r *http.Request) {
	// http.ServeFile определяет MIME по расширению, но у нас свои пути
	filePath := "HTML" + r.URL.Path[len("/static"):]
	// Определяем Content-Type по расширению
	switch {
	case strings.HasSuffix(filePath, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(filePath, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(filePath, ".woff2"):
		w.Header().Set("Content-Type", "font/woff2")
	case strings.HasSuffix(filePath, ".ttf"):
		w.Header().Set("Content-Type", "font/ttf")
	}
	http.ServeFile(w, r, filePath)
}

func serveLogin(w http.ResponseWriter, r *http.Request) {
	s := getSession(r)
	if s != nil {
		redirectToRole(w, r, s.Role)
		return
	}
	http.ServeFile(w, r, "HTML/login.html")
}

func serve404(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>404 — TurboPlata</title>
	<link rel="stylesheet" href="/static/style.css"></head><body>
	<div class="container text-center" style="padding:80px 0;">
	<h1>404</h1><p style="color:#666;margin:16px 0;">Страница не найдена.</p>
	<a href="/login" style="color:#4a6cf7;">🔑 Войти</a>
	</div></body></html>`))
}

// requireRole — middleware, проверяет что сессия есть и роль внутри списка.
// Если нет — пишет 403 и возвращает false.
func requireRole(r *http.Request, w http.ResponseWriter, roles ...string) *Session {
	s := getSession(r)
	if s == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	if len(roles) > 0 {
		hasAccess := false
		for _, role := range roles {
			if s.Role == role {
				hasAccess = true
				break
			}
		}
		if !hasAccess {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return nil
		}
	}
	return s
}

func servePage(htmlFile string, allowedRoles ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := getSession(r)
		if s == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if len(allowedRoles) > 0 {
			hasAccess := false
			for _, role := range allowedRoles {
				if s.Role == role {
					hasAccess = true
					break
				}
			}
			if !hasAccess {
				http.Error(w, "Доступ запрещён", http.StatusForbidden)
				return
			}
		}
		http.ServeFile(w, r, "HTML/"+htmlFile)
	}
}

func redirectToRole(w http.ResponseWriter, r *http.Request, role string) {
	switch role {
	case "admin":
		http.Redirect(w, r, "/admin", http.StatusFound)
	case "master":
		http.Redirect(w, r, "/master", http.StatusFound)
	case "otk":
		http.Redirect(w, r, "/otk", http.StatusFound)
	case "vydacha":
		http.Redirect(w, r, "/vydacha", http.StatusFound)
	case "worker":
		http.Redirect(w, r, "/shveya", http.StatusFound)
	default:
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// ---------------------------------------------------------------------------
// API: LOGIN / LOGOUT / SESSION
// ---------------------------------------------------------------------------

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSON(w, http.StatusMethodNotAllowed, APIResponse{Status: "error", Message: "Метод не поддерживается"})
		return
	}
	var req struct {
		Barcode string `json:"barcode"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondJSON(w, http.StatusBadRequest, APIResponse{Status: "error", Message: "Неверный формат"})
		return
	}

	tabNo := req.Barcode
	if len(tabNo) > 5 && tabNo[:5] == "USER_" {
		tabNo = tabNo[5:]
	}

	var workerID int
	var fio, role string
	err := dbEmployees.QueryRow(
		`SELECT ид, фио, права FROM сотрудники WHERE табельный_номер = ? AND активный = 1`,
		tabNo,
	).Scan(&workerID, &fio, &role)
	if err != nil {
		err = dbEmployees.QueryRow(
			`SELECT ид, фио, права FROM сотрудники WHERE табельный_номер = ? AND активный = 1`,
			req.Barcode,
		).Scan(&workerID, &fio, &role)
	}
	if err != nil {
		log.Printf("❌ Неизвестный код: %s", req.Barcode)
		respondJSON(w, http.StatusOK, APIResponse{Status: "error", Message: "Неизвестный табельный номер"})
		return
	}

	sid := generateSessionID()
	sessionsMu.Lock()
	sessions[sid] = &Session{
		WorkerID: workerID, TabNo: tabNo,
		FIO: fio, Role: role, CreatedAt: time.Now(),
	}
	sessionsMu.Unlock()
	setSessionCookie(w, sid)

	// Синхронизируем с currentWorker для веб-сканов
	currentWorkerMu.Lock()
	currentWorker["web"] = tabNo
	currentWorkerMu.Unlock()

	redirectMap := map[string]string{
		"admin": "/admin", "master": "/master",
		"otk": "/otk", "vydacha": "/vydacha", "worker": "/shveya",
	}
	page, ok := redirectMap[role]
	if !ok {
		page = "/vydacha"
	}

	log.Printf("✅ Вход: %s (%s) как %s", fio, tabNo, role)
	respondJSON(w, http.StatusOK, APIResponse{
		Status: "ok", Message: "Добро пожаловать, " + fio + "!",
		Data: map[string]interface{}{"redirect": page, "role": role, "fio": fio},
	})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("session_id")
	if err == nil {
		sessionsMu.Lock()
		delete(sessions, c.Value)
		sessionsMu.Unlock()
	}
	// Чистим currentWorker["web"]
	currentWorkerMu.Lock()
	delete(currentWorker, "web")
	currentWorkerMu.Unlock()

	clearSessionCookie(w)
	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Message: "Выход выполнен"})
}

func handleSession(w http.ResponseWriter, r *http.Request) {
	s := getSession(r)
	if s == nil {
		respondJSON(w, http.StatusOK, APIResponse{Status: "error", Message: "Не авторизован"})
		return
	}
	respondJSON(w, http.StatusOK, APIResponse{
		Status: "ok",
		Data: map[string]interface{}{
			"ид_сотрудника": s.WorkerID,
			"фио":           s.FIO,
			"табельный":     s.TabNo,
			"роль":          s.Role,
		},
	})
}

// loadHTMLTemplates — заглушка, шаблоны больше не нужны
func loadHTMLTemplates() error {
	return nil
}

// getAdminHTML возвращает HTML для страницы администратора
func getAdminHTML() string {
	return `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Администратор - TurboPlata</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        html, body { height: 100%; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; background: #f5f5f5; padding: 12px; }
        .container { max-width: 1000px; margin: 0 auto; }
        h1 { color: #333; margin: 16px 0; font-size: 24px; }
        h2 { color: #555; margin: 16px 0 12px 0; font-size: 16px; font-weight: 600; }
        .panel { background: white; padding: 16px; margin: 12px 0; border-radius: 6px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .form-group { margin: 14px 0; }
        label { display: block; margin-bottom: 6px; font-weight: 600; font-size: 14px; color: #333; }
        input, textarea, select { width: 100%; padding: 12px; font-size: 16px; border: 2px solid #ddd; border-radius: 4px; transition: border-color 0.3s; -webkit-appearance: none; appearance: none; background: white; }
        textarea { font-family: 'Monaco', 'Menlo', monospace; height: 250px; resize: vertical; }
        select { background-image: url('data:image/svg+xml;charset=UTF-8,%3csvg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 24 24%22 fill=%22none%22 stroke=%22%23333%22%3e%3cpath d=%22M6 9l6 6 6-6%22/%3e%3c/svg%3e'); background-repeat: no-repeat; background-position: right 8px center; background-size: 20px; padding-right: 32px; }
        input:focus, textarea:focus, select:focus { outline: none; border-color: #2196F3; box-shadow: 0 0 0 3px rgba(33, 150, 243, 0.1); }
        button { padding: 12px 20px; background: #2196F3; color: white; border: none; border-radius: 4px; cursor: pointer; margin-right: 10px; margin-top: 10px; font-size: 14px; font-weight: 600; transition: background 0.3s; }
        button:active { background: #1565c0; }
        button:hover { background: #0b7dda; }
        table { width: 100%; border-collapse: collapse; margin: 12px 0; font-size: 13px; }
        th, td { padding: 10px; text-align: left; border-bottom: 1px solid #ddd; }
        th { background: #f2f2f2; font-weight: 600; }
        .success { color: #28a745; margin-top: 10px; font-weight: 600; }
        .error { color: #dc3545; margin-top: 10px; font-weight: 600; }
        @media (max-width: 480px) {
            body { padding: 8px; }
            .container { margin: 0; }
            h1 { font-size: 20px; margin: 12px 0; }
            h2 { font-size: 14px; }
            .panel { padding: 12px; margin: 10px 0; }
            input, textarea, select { padding: 11px; font-size: 16px; }
            button { padding: 11px 16px; margin-top: 8px; font-size: 14px; }
            table { font-size: 12px; }
            th, td { padding: 8px; }
        }
        @media (min-width: 768px) {
            body { padding: 16px; }
            .container { margin: 0 auto; }
            h1 { font-size: 28px; margin: 20px 0; }
            h2 { font-size: 18px; margin-top: 20px; }
            .panel { padding: 20px; margin: 15px 0; }
            input, textarea, select { padding: 12px; }
            button { padding: 12px 24px; }
            table { font-size: 14px; }
            th, td { padding: 12px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>⚙️ Администратор системы</h1>
        
        <div class="panel">
            <h2>📋 Управление заказами (из SQLite)</h2>
            <table id="orders-table">
                <thead>
                    <tr>
                        <th>ID</th>
                        <th>Название</th>
                        <th>Кол-во</th>
                        <th>Цена</th>
                    </tr>
                </thead>
                <tbody></tbody>
            </table>
        </div>

        <div class="panel">
            <h2>🔧 Редактирование конфигурации</h2>
            <div class="form-group">
                <label>JSON (formulas.json):</label>
                <textarea id="configJson" spellcheck="false"></textarea>
            </div>
            <button onclick="saveConfig()">💾 Сохранить</button>
            <div id="configStatus"></div>
        </div>
    </div>
    <script>
        async function loadOrders() {
            try {
                const res = await fetch('/api/orders');
                const data = await res.json();
                const tbody = document.querySelector('#orders-table tbody');
                tbody.innerHTML = '';
                if (data.data && Array.isArray(data.data)) {
                    data.data.forEach(o => {
                        const tr = document.createElement('tr');
                        tr.innerHTML = '<td>' + o.ид + '</td><td>' + o.название + '</td><td>' + o.количество + '</td><td>' + o.цена.toFixed(2) + ' ₽</td>';
                        tbody.appendChild(tr);
                    });
                }
            } catch (e) { console.error('Ошибка:', e); }
        }
        async function loadConfig() {
            try {
                const res = await fetch('/api/config');
                const data = await res.json();
                if (data.data) {
                    document.getElementById('configJson').value = JSON.stringify(data.data, null, 2);
                }
            } catch (e) { console.error('Ошибка:', e); }
        }
        async function saveConfig() {
            try {
                const json = JSON.parse(document.getElementById('configJson').value);
                const res = await fetch('/api/config/update', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(json)
                });
                const data = await res.json();
                const status = document.getElementById('configStatus');
                if (data.status === 'ok') {
                    status.className = 'success';
                    status.textContent = '✅ Сохранено!';
                } else {
                    status.className = 'error';
                    status.textContent = '❌ Ошибка: ' + data.message;
                }
                setTimeout(() => { status.textContent = ''; }, 2500);
            } catch (e) {
                const status = document.getElementById('configStatus');
                status.className = 'error';
                status.textContent = '❌ Ошибка JSON: ' + e.message;
            }
        }
        loadOrders();
        loadConfig();
        setInterval(loadOrders, 15000);
    </script>
</body>
</html>`
}

// getMasterHTML возвращает HTML для страницы мастера
func getMasterHTML() string {
	return `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Мастер участка - TurboPlata</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        html, body { height: 100%; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; background: #f5f5f5; padding: 12px; }
        .container { max-width: 900px; margin: 0 auto; }
        h1 { color: #333; font-size: 24px; margin: 16px 0; }
        .operation { background: white; padding: 14px; margin: 10px 0; border-left: 4px solid #FF9800; border-radius: 4px; box-shadow: 0 2px 4px rgba(0,0,0,0.08); }
        .operation.error { border-left-color: #f44336; background: #ffebee; }
        .operation.warn { border-left-color: #FFC107; background: #fffbea; }
        .operation.ok { border-left-color: #4CAF50; background: #f1f8e9; }
        .op-title { font-weight: 600; color: #333; margin-bottom: 8px; font-size: 15px; }
        .op-desc { font-size: 13px; color: #666; margin-bottom: 10px; }
        button { padding: 10px 16px; background: #FF5722; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 14px; font-weight: 600; margin-right: 8px; transition: background 0.3s; }
        button:active { background: #D84315; }
        button:hover { background: #E64A19; }
        @media (max-width: 480px) {
            body { padding: 8px; }
            h1 { font-size: 20px; margin: 12px 0; }
            .operation { padding: 12px; margin: 8px 0; }
            .op-title { font-size: 14px; }
            .op-desc { font-size: 12px; }
            button { padding: 9px 12px; font-size: 13px; }
        }
        @media (min-width: 768px) {
            body { padding: 16px; }
            h1 { font-size: 28px; margin: 20px 0; }
            .operation { padding: 16px; margin: 12px 0; }
            button { padding: 11px 18px; font-size: 15px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>👷 Панель мастера участка</h1>
        
        <div id="operations-list"></div>
    </div>
    <script>
        async function loadOperations() {
            try {
                const res = await fetch('/api/boxes');
                const data = await res.json();
                const list = document.getElementById('operations-list');
                list.innerHTML = '';
                if (data.data && Array.isArray(data.data)) {
                    data.data.forEach(op => {
                        const div = document.createElement('div');
                        const status = op.статус === 'работа' ? 'warn' : (op.статус === 'окончена' ? 'ok' : 'error');
                        div.className = 'operation ' + status;
                        div.innerHTML = '<div class="op-title">Коробка #' + op.ид + ': ' + op.изделие + '</div>' +
                            '<div class="op-desc">Количество: ' + op.количество + ' | Норма: ' + op.норма + ' мин</div>' +
                            '<button onclick="setPenalty(' + op.ид + ')">&#128681; Штраф</button>' +
                            '<button onclick="completeOp(' + op.ид + ')">&#10004; На брак</button>';
                        list.appendChild(div);
                    });
                }
            } catch (e) { console.error('Ошибка:', e); }
        }
        function setPenalty(id) { alert('Штраф для коробки ' + id); }
        function completeOp(id) { alert('Операция ' + id + ' завершена'); }
        loadOperations();
        setInterval(loadOperations, 10000);
    </script>
</body>
</html>`
}

// getOTKHTML возвращает HTML для страницы ОТК
func getOTKHTML() string {
	return `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>ОТК - TurboPlata</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        html, body { height: 100%; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); display: flex; align-items: center; justify-content: center; padding: 12px; min-height: 100vh; }
        .container { max-width: 500px; width: 100%; text-align: center; background: white; padding: 20px; border-radius: 12px; box-shadow: 0 10px 30px rgba(0,0,0,0.2); }
        h1 { color: #333; font-size: 24px; margin-bottom: 16px; }
        h2 { color: #555; font-size: 16px; margin-bottom: 14px; font-weight: 600; }
        .box-info { padding: 16px; margin: 12px 0; background: #f9f9f9; border-radius: 6px; }
        input { width: 100%; padding: 12px; font-size: 16px; border: 2px solid #ddd; border-radius: 6px; transition: border-color 0.3s; }
        input:focus { outline: none; border-color: #667eea; box-shadow: 0 0 0 3px rgba(102, 126, 234, 0.1); }
        #box-details { margin-top: 10px; font-size: 14px; color: #666; }
        .buttons { display: flex; gap: 10px; justify-content: center; flex-wrap: wrap; margin-top: 16px; }
        button { flex: 1; min-width: 140px; padding: 14px; font-size: 15px; cursor: pointer; border: none; border-radius: 6px; font-weight: 600; transition: all 0.3s; -webkit-touch-callout: none; }
        .ok { background: #4CAF50; color: white; }
        .ok:active { background: #388E3C; }
        .ok:hover { background: #45a049; }
        .reject { background: #f44336; color: white; }
        .reject:active { background: #da190b; }
        .reject:hover { background: #da190b; }
        .status { margin-top: 12px; padding: 10px; border-radius: 6px; font-weight: 600; font-size: 13px; }
        .status.success { background: #d4edda; color: #155724; }
        .status.error { background: #f8d7da; color: #721c24; }
        @media (max-width: 480px) {
            body { padding: 10px; }
            .container { padding: 16px; border-radius: 10px; }
            h1 { font-size: 22px; }
            h2 { font-size: 15px; }
            .box-info { padding: 14px; margin: 10px 0; }
            input { padding: 11px; font-size: 16px; }
            button { padding: 13px; font-size: 14px; min-width: 120px; }
        }
        @media (min-width: 768px) {
            body { padding: 20px; }
            .container { padding: 30px; border-radius: 12px; }
            h1 { font-size: 28px; margin-bottom: 20px; }
            h2 { font-size: 18px; }
            .box-info { padding: 20px; }
            input { padding: 13px; font-size: 16px; }
            button { min-width: 160px; padding: 15px; font-size: 16px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔍 Контроль качества (ОТК)</h1>
        
        <div class="box-info">
            <h2>Отсканируйте штрихкод</h2>
            <input type="text" id="barcode" placeholder="Код коробки..." autofocus>
            <p id="box-details" style="margin-top: 10px; font-size: 14px; color: #666;"></p>
        </div>

        <div class="buttons">
            <button class="ok" onclick="approveBox()">✅ Принять</button>
            <button class="reject" onclick="rejectBox()">❌ Брак</button>
        </div>
        <div id="status" style="display:none;"></div>
    </div>
    <script>
        async function approveBox() {
            const barcode = document.getElementById('barcode').value;
            if (!barcode) {
                alert('Введите код');
                return;
            }
            try {
                const res = await fetch('/api/approve', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ barcode: barcode })
                });
                const data = await res.json();
                alert(data.status === 'ok' ? '✅ Коробка принята!' : '❌ Ошибка: ' + data.message);
                document.getElementById('barcode').value = '';
                document.getElementById('box-details').textContent = '';
            } catch (e) { alert('Ошибка: ' + e.message); }
        }
        function rejectBox() {
            const barcode = document.getElementById('barcode').value;
            if (!barcode) {
                alert('Введите код');
                return;
            }
            alert('Коробка ' + barcode + ' отклонена как брак');
            document.getElementById('barcode').value = '';
            document.getElementById('box-details').textContent = '';
        }
    </script>
</body>
</html>`
}
