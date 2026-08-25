package main

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// API: СТАТИСТИКА — ПООПЕРАЦИОННЫЙ ЛОГ
// ---------------------------------------------------------------------------

func handleStatLog(w http.ResponseWriter, r *http.Request) {
	dateFrom := r.URL.Query().Get("from")
	dateTo := r.URL.Query().Get("to")
	orderFilter := r.URL.Query().Get("order_id")
	workerFilter := r.URL.Query().Get("worker_id")
	opFilter := r.URL.Query().Get("operation")

	if dateFrom == "" {
		dateFrom = time.Now().Format("2006-01-02")
	}
	if dateTo == "" {
		dateTo = time.Now().Format("2006-01-02")
	}

	query := `
		SELECT жв.ид, жв.ид_операции, ок.ид_коробки, к.ид_заказа, з.название as изделие,
		       ок.шаг, ок.название as операция, ок.норма_времени_сек,
		       жв.ид_рабочего, с.фио as рабочий,
		       жв.время_начала, жв.время_окончания,
		       жв.сумма_начислена, жв.штраф, жв.статус_выплаты,
		       ок.брак, к.статус as статус_коробки
		FROM журнал_выработки жв
		JOIN операции_коробки ок ON жв.ид_операции = ок.ид
		JOIN коробки к ON ок.ид_коробки = к.ид
		JOIN заказы з ON к.ид_заказа = з.ид
		LEFT JOIN сотрудники с ON жв.ид_рабочего = с.ид
		WHERE date(жв.время_начала) >= ? AND date(жв.время_начала) <= ?
	`
	var args []interface{}
	args = append(args, dateFrom, dateTo)

	if orderFilter != "" {
		query += ` AND к.ид_заказа = ?`
		args = append(args, orderFilter)
	}
	if workerFilter != "" {
		query += ` AND жв.ид_рабочего = ?`
		args = append(args, workerFilter)
	}
	if opFilter != "" {
		query += ` AND ок.название LIKE '%' || ? || '%'`
		args = append(args, opFilter)
	}
	query += ` ORDER BY жв.время_начала DESC`

	rows, err := dbMain.Query(query, args...)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	var entries []map[string]interface{}
	for rows.Next() {
		var id, opID, boxID, orderID, step, workerID, brak, penalty int
		var itemName, opName, workerName, statusPay string
		var normSec, sum float64
		var startTime, endTime sql.NullString
		var boxStatus string
		if rows.Scan(&id, &opID, &boxID, &orderID, &itemName, &step, &opName, &normSec,
			&workerID, &workerName, &startTime, &endTime, &sum, &penalty, &statusPay, &brak, &boxStatus) != nil {
			continue
		}

		factSec := 0.0
		if startTime.Valid && endTime.Valid {
			st, _ := parseTimeFlexible(startTime.String)
			et, _ := parseTimeFlexible(endTime.String)
			factSec = et.Sub(st).Seconds()
		}
		speedPct := 0.0
		if factSec > 0 && normSec > 0 {
			speedPct = (normSec / factSec) * 100
		}

		entry := map[string]interface{}{
			"ид":               id,
			"ид_операции":      opID,
			"ид_коробки":       boxID,
			"ид_заказа":        orderID,
			"изделие":          itemName,
			"шаг":              step,
			"операция":         opName,
			"норма_сек":        normSec,
			"ид_рабочего":      workerID,
			"рабочий":          workerName,
			"время_начала":     startTime.String,
			"время_окончания":  endTime.String,
			"факт_сек":         factSec,
			"скорость_процент": speedPct,
			"начислено":        kopecksToRubles(sum),
			"начислено_коп":    sum,
			"штраф":            penalty,
			"статус_выплаты":   statusPay,
			"брак":             brak,
			"статус_коробки":   boxStatus,
		}
		entries = append(entries, entry)
	}

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: entries})
}

// ---------------------------------------------------------------------------
// API: СТАТИСТИКА — СКОРОСТИ ШВЕЙ (агрегированные)
// ---------------------------------------------------------------------------

func handleStatSpeed(w http.ResponseWriter, r *http.Request) {
	dateFrom := r.URL.Query().Get("from")
	dateTo := r.URL.Query().Get("to")
	workerFilter := r.URL.Query().Get("worker_id")
	orderFilter := r.URL.Query().Get("order_id")
	opFilter := r.URL.Query().Get("operation")

	if dateFrom == "" {
		dateFrom = time.Now().Format("2006-01-02")
	}
	if dateTo == "" {
		dateTo = time.Now().Format("2006-01-02")
	}

	query := `
		SELECT жв.ид_рабочего, с.фио as рабочий,
		       ок.ид_коробки, к.ид_заказа, з.название as изделие,
		       ок.ид, ок.шаг, ок.название as операция, ок.норма_времени_сек,
		       жв.время_начала, жв.время_окончания, жв.сумма_начислена
		FROM журнал_выработки жв
		JOIN операции_коробки ок ON жв.ид_операции = ок.ид
		JOIN коробки к ON ок.ид_коробки = к.ид
		JOIN заказы з ON к.ид_заказа = з.ид
		LEFT JOIN сотрудники с ON жв.ид_рабочего = с.ид
		WHERE date(жв.время_начала) >= ? AND date(жв.время_начала) <= ?
	`
	var args []interface{}
	args = append(args, dateFrom, dateTo)

	if workerFilter != "" {
		query += ` AND жв.ид_рабочего = ?`
		args = append(args, workerFilter)
	}
	if orderFilter != "" {
		query += ` AND к.ид_заказа = ?`
		args = append(args, orderFilter)
	}
	if opFilter != "" {
		query += ` AND ок.название LIKE '%' || ? || '%'`
		args = append(args, opFilter)
	}
	query += ` ORDER BY жв.ид_рабочего, жв.время_начала`

	rows, err := dbMain.Query(query, args...)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	type SpeedPoint struct {
		Date     string  `json:"дата"`
		Time     string  `json:"время"`
		SpeedPct float64 `json:"скорость_процент"`
		NormSec  float64 `json:"норма_сек"`
		FactSec  float64 `json:"факт_сек"`
		OpName   string  `json:"операция"`
		OrderID  int     `json:"ид_заказа"`
		ItemName string  `json:"изделие"`
		Earned   float64 `json:"заработано"`
	}
	type WorkerSpeed struct {
		WorkerID   int          `json:"ид"`
		FIO        string       `json:"фио"`
		TotalOps   int          `json:"операций"`
		AvgSpeed   float64      `json:"средняя_скорость"`
		TotalMoney float64      `json:"итого"`
		Points     []SpeedPoint `json:"точки"`
	}

	byWorker := make(map[int]*WorkerSpeed)
	for rows.Next() {
		var wID, boxID, orderID, opDBID, step int
		var workerName, itemName, opName string
		var normSec, sum float64
		var startTime, endTime sql.NullString
		if rows.Scan(&wID, &workerName, &boxID, &orderID, &itemName, &opDBID, &step, &opName, &normSec, &startTime, &endTime, &sum) != nil {
			continue
		}
		if byWorker[wID] == nil {
			byWorker[wID] = &WorkerSpeed{WorkerID: wID, FIO: workerName}
		}
		ws := byWorker[wID]
		ws.TotalOps++
		ws.TotalMoney += kopecksToRubles(sum)

		factSec := 0.0
		if startTime.Valid && endTime.Valid {
			st, _ := parseTimeFlexible(startTime.String)
			et, _ := parseTimeFlexible(endTime.String)
			factSec = et.Sub(st).Seconds()
		}
		speedPct := 0.0
		if factSec > 0 && normSec > 0 {
			speedPct = (normSec / factSec) * 100
		}
		ws.AvgSpeed += speedPct
		ws.Points = append(ws.Points, SpeedPoint{
			Date:     startTime.String[:10],
			Time:     startTime.String,
			SpeedPct: speedPct,
			NormSec:  normSec,
			FactSec:  factSec,
			OpName:   opName,
			OrderID:  orderID,
			ItemName: itemName,
			Earned:   kopecksToRubles(sum),
		})
	}

	var result []*WorkerSpeed
	for _, ws := range byWorker {
		if ws.TotalOps > 0 {
			ws.AvgSpeed = ws.AvgSpeed / float64(ws.TotalOps)
		}
		result = append(result, ws)
	}

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: result})
}

// ---------------------------------------------------------------------------
// API: СТАТИСТИКА — ВЫГРУЗКА CSV
// ---------------------------------------------------------------------------

func handleStatExport(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode") // "log" или "speed"
	if mode == "" {
		mode = "log"
	}

	// Копируем параметры из запроса
	dateFrom := r.URL.Query().Get("from")
	dateTo := r.URL.Query().Get("to")
	orderFilter := r.URL.Query().Get("order_id")
	workerFilter := r.URL.Query().Get("worker_id")
	opFilter := r.URL.Query().Get("operation")

	// Собираем данные тем же запросом что и handleStatLog
	query := `
		SELECT жв.ид, ок.ид_коробки, к.ид_заказа, з.название as изделие,
		       ок.шаг, ок.название as операция, ок.норма_времени_сек,
		       с.фио as рабочий,
		       жв.время_начала, жв.время_окончания,
		       жв.сумма_начислена, жв.штраф, ок.брак, к.статус
		FROM журнал_выработки жв
		JOIN операции_коробки ок ON жв.ид_операции = ок.ид
		JOIN коробки к ON ок.ид_коробки = к.ид
		JOIN заказы з ON к.ид_заказа = з.ид
		LEFT JOIN сотрудники с ON жв.ид_рабочего = с.ид
		WHERE date(жв.время_начала) >= ? AND date(жв.время_начала) <= ?
	`
	var args []interface{}
	args = append(args, dateFrom, dateTo)

	if orderFilter != "" {
		query += ` AND к.ид_заказа = ?`
		args = append(args, orderFilter)
	}
	if workerFilter != "" {
		query += ` AND жв.ид_рабочего = ?`
		args = append(args, workerFilter)
	}
	if opFilter != "" {
		query += ` AND ок.название LIKE '%' || ? || '%'`
		args = append(args, opFilter)
	}
	query += ` ORDER BY жв.время_начала DESC`

	rows, err := dbMain.Query(query, args...)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, APIResponse{Status: "error", Message: err.Error()})
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=statistic_%s_%s.csv", dateFrom, dateTo))
	// BOM для Excel
	w.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"ID", "Коробка", "Заказ", "Изделие", "Шаг", "Операция",
		"Норма,с", "Рабочий", "Начало", "Конец", "Факт,с", "Скорость%",
		"Начислено,руб", "Штраф", "Брак", "Статус коробки"})

	for rows.Next() {
		var id, boxID, orderID, step, penalty, brak int
		var itemName, opName, workerName, boxStatus string
		var normSec, sum float64
		var startTime, endTime sql.NullString
		if rows.Scan(&id, &boxID, &orderID, &itemName, &step, &opName, &normSec,
			&workerName, &startTime, &endTime, &sum, &penalty, &brak, &boxStatus) != nil {
			continue
		}
		factSec := 0.0
		if startTime.Valid && endTime.Valid {
			st, _ := parseTimeFlexible(startTime.String)
			et, _ := parseTimeFlexible(endTime.String)
			factSec = et.Sub(st).Seconds()
		}
		speedPct := 0.0
		if factSec > 0 && normSec > 0 {
			speedPct = (normSec / factSec) * 100
		}

		writer.Write([]string{
			strconv.Itoa(id),
			strconv.Itoa(boxID),
			strconv.Itoa(orderID),
			itemName,
			strconv.Itoa(step),
			opName,
			fmt.Sprintf("%.1f", normSec),
			workerName,
			startTime.String,
			endTime.String,
			fmt.Sprintf("%.1f", factSec),
			fmt.Sprintf("%.1f", speedPct),
			fmt.Sprintf("%.2f", kopecksToRubles(sum)),
			strconv.Itoa(penalty),
			strconv.Itoa(brak),
			boxStatus,
		})
	}
}

// ---------------------------------------------------------------------------
// API: СПИСОК ЗАКАЗОВ И РАБОЧИХ ДЛЯ ФИЛЬТРОВ
// ---------------------------------------------------------------------------

func handleStatFilters(w http.ResponseWriter, r *http.Request) {
	// Заказы
	orderRows, err := dbMain.Query(`SELECT ид, название FROM заказы WHERE активный = 1 ORDER BY ид`)
	if err == nil {
		defer orderRows.Close()
	}
	var orders []map[string]interface{}
	if err == nil {
		for orderRows.Next() {
			var id int
			var name string
			if orderRows.Scan(&id, &name) == nil {
				orders = append(orders, map[string]interface{}{"ид": id, "название": name})
			}
		}
	}

	// Рабочие
	empRows, err := dbEmployees.Query(`SELECT ид, фио FROM сотрудники WHERE активный = 1 ORDER BY фио`)
	if err == nil {
		defer empRows.Close()
	}
	var workers []map[string]interface{}
	if err == nil {
		for empRows.Next() {
			var id int
			var fio string
			if empRows.Scan(&id, &fio) == nil {
				workers = append(workers, map[string]interface{}{"ид": id, "фио": fio})
			}
		}
	}

	respondJSON(w, http.StatusOK, APIResponse{Status: "ok", Data: map[string]interface{}{
		"заказы":  orders,
		"рабочие": workers,
	}})
}
