// TurboPlata — общие JS-утилиты

/** Выполнить API-запрос */
async function api(method, path, body) {
    const opts = { method, headers: { 'Content-Type': 'application/json' } };
    if (body) opts.body = JSON.stringify(body);
    const res = await fetch(path, opts);
    return res.json();
}

/** GET-запрос */
async function apiGet(path) { return api('GET', path); }

/** POST-запрос */
async function apiPost(path, body) { return api('POST', path, body); }

/** PUT-запрос */
async function apiPut(path, body) { return api('PUT', path, body); }

/** DELETE-запрос */
async function apiDel(path) { return api('DELETE', path); }

/** Показать уведомление */
function showStatus(el, msg, type) {
    el.textContent = msg;
    el.className = type === 'ok' ? 'success' : 'error';
    el.style.display = 'block';
    setTimeout(() => { el.style.display = 'none'; }, 3000);
}

/** Выход (сброс сессии) */
async function logout() {
    await apiPost('/api/logout', {});
    window.location.href = '/login';
}

/** Проверить сессию при загрузке страницы */
async function checkSession() {
    const data = await apiGet('/api/session');
    if (data.status !== 'ok') {
        window.location.href = '/login';
        return null;
    }
    return data.data;
}

/** Получить hostname компа через PC-агент (127.0.0.1:9999) */
async function getPCHostname() {
    try {
        const res = await fetch('http://127.0.0.1:9999', { signal: AbortSignal.timeout(2000) });
        const data = await res.json();
        return data.hostname || null;
    } catch (e) {
        console.warn('PC-агент недоступен:', e);
        return null;
    }
}

/** Подключиться к WebSocket сервера с hostname компа.
 *  onMessage(data) — вызывается при получении сообщения.
 *  Возвращает WebSocket или null. */
async function connectWS(onMessage) {
    let hostname = await getPCHostname();
    if (!hostname) {
        console.warn('Без hostname компа WebSocket не подключён');
        return null;
    }
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = proto + '//' + window.location.host + '/api/ws/browser?hostname=' + encodeURIComponent(hostname);
    const ws = new WebSocket(url);

    ws.onopen = () => console.log('WebSocket подключён как', hostname);
    ws.onclose = () => console.log('WebSocket отключён');
    ws.onerror = e => console.error('WebSocket ошибка', e);
    ws.onmessage = e => {
        try {
            const msg = JSON.parse(e.data);
            if (onMessage) onMessage(msg);
        } catch (err) {
            console.error('Ошибка парсинга WebSocket:', err);
        }
    };
    return ws;
}

/**
 * Универсальный сканер для веб-страниц.
 * Использование:
 *   const scan = createBarcodeInput('#scanInput', '#scanResult', 'admin');
 *   scan.onScan = async (barcode) => { ... };
 *   scan.onUser = async (tabNo, fio, role) => { ... };
 *
 * @param {string} inputSelector - CSS-селектор поля ввода
 * @param {string} resultSelector - CSS-селектор для вывода результата
 * @param {string} mode - 'admin' | 'otk' | 'vydacha' | 'worker'
 * @param {object} callbacks - { onScan, onUser, onDone }
 */
function createBarcodeInput(inputSelector, resultSelector, callbacks) {
    const inp = document.querySelector(inputSelector);
    const resultEl = document.querySelector(resultSelector);
    if (!inp) return null;

    // Обёртка для вывода
    const out = {
        ok: (msg, data) => {
            if (!resultEl) return;
            resultEl.style.display = 'block';
            resultEl.className = 'success';
            resultEl.textContent = '✅ ' + msg;
            if (callbacks.onScan) callbacks.onScan(msg, data);
        },
        err: (msg) => {
            if (!resultEl) return;
            resultEl.style.display = 'block';
            resultEl.className = 'error';
            resultEl.textContent = '❌ ' + msg;
        },
        clear: () => {
            if (!resultEl) return;
            resultEl.style.display = 'none';
        }
    };

    inp.addEventListener('keydown', async e => {
        if (e.key !== 'Enter') return;
        const barcode = inp.value.trim();
        if (!barcode) return;
        inp.value = '';
        inp.focus();

        // USER_XXX — авторизация
        if (barcode.startsWith('USER_')) {
            const res = await apiPost('/api/scan', { barcode });
            if (res.status === 'ok') {
                const data = res.data || {};
                const fio = data.фио || data.fio || data.worker_name || '—';
                const role = data.роль || data.role || data.rights || '—';
                out.ok(fio + ' [' + role + '] вошёл');
                if (callbacks.onUser) callbacks.onUser(barcode, fio, role, res);
            } else {
                out.err(res.message || 'Неизвестный код');
            }
            return;
        }

        // DONE — завершение
        if (barcode === 'DONE') {
            const res = await apiPost('/api/scan', { barcode });
            if (res.status === 'ok') {
                out.ok(res.message || 'Операция завершена');
            } else {
                out.err(res.message || 'Нет активной операции');
            }
            if (callbacks.onDone) callbacks.onDone(res);
            return;
        }

        // Операция КОРОБКА-ОПЕРАЦИЯ или просто номер коробки
        const res = await apiPost('/api/scan', { barcode });
        if (res.status === 'ok') {
            out.ok(res.message || 'OK');
        } else {
            out.err(res.message || 'Ошибка');
        }
    });

    return out;
}
