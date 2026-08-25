package main

import (
	"strconv"
	"strings"
)

// parsedBarcode содержит результат разбора штрихкода
type parsedBarcode struct {
	BoxID    int    // ID коробки
	OpName   string // название операции (для старого формата)
	OpCode   string // код операции (для нового формата)
	IsBrak   bool   // бракованная коробка
	FullCode string // исходный код для логирования
}

// parseBarcode разбирает штрихкод — поддерживает старый и новый форматы
// Старый: ЧИСЛО-ТЕКСТ                        → BoxID=5, OpName="Стачивание"
// Новый:  zkz00001Xbx00001Zop00001          → BoxID=1, OpCode="op00001"
//
//	zkz00001Xbr00001Zop00002          → BoxID=1, OpCode="op00002", IsBrak=true
func parseBarcode(barcode string) *parsedBarcode {
	// Code39 добавляет * в начале и конце — обрезаем
	barcode = strings.Trim(barcode, "*")
	result := &parsedBarcode{FullCode: barcode}

	// 1) Пробуем новый формат с разделителем Z (код операции)
	zParts := strings.Split(barcode, "Z")
	if len(zParts) >= 2 {
		suffix := zParts[len(zParts)-1]
		if strings.HasPrefix(suffix, "op") || strings.HasPrefix(suffix, "br") {
			result.OpCode = suffix
			first := zParts[0]
			xIdx := strings.LastIndex(first, "X")
			if xIdx >= 0 {
				tail := first[xIdx+1:]
				if len(tail) >= 2 {
					prefix := tail[:2]
					numStr := tail[2:]
					if id, err := strconv.Atoi(numStr); err == nil {
						result.BoxID = id
						result.IsBrak = (prefix == "br")
						return result
					}
				}
			}
		}
	}

	// 2) BRAK#N
	if strings.HasPrefix(barcode, "BRAK#") {
		if id, err := strconv.Atoi(barcode[5:]); err == nil {
			result.BoxID = id
			result.IsBrak = true
			return result
		}
	}

	// 3) Старый формат: ЧИСЛО-ТЕКСТ
	dashIdx := strings.Index(barcode, "-")
	if dashIdx > 0 {
		if id, err := strconv.Atoi(barcode[:dashIdx]); err == nil {
			result.BoxID = id
			result.OpName = barcode[dashIdx+1:]
			return result
		}
	}

	// 4) Просто число
	if id, err := strconv.Atoi(barcode); err == nil {
		result.BoxID = id
		return result
	}

	return result
}

// extractBoxIDFromBarcode извлекает ID коробки из любого формата
func extractBoxIDFromBarcode(barcode string) (int, bool) {
	barcode = strings.Trim(barcode, "*")
	if strings.HasPrefix(barcode, "BRAK#") {
		if id, err := strconv.Atoi(barcode[5:]); err == nil {
			return id, true
		}
	}
	// Новый формат
	zParts := strings.Split(barcode, "Z")
	if len(zParts) >= 2 {
		first := zParts[0]
		xIdx := strings.LastIndex(first, "X")
		if xIdx >= 0 {
			tail := first[xIdx+3:] // после Xbx или Xbr
			if id, err := strconv.Atoi(tail); err == nil {
				return id, true
			}
		}
	}
	// Старый формат
	dashIdx := strings.Index(barcode, "-")
	if dashIdx > 0 {
		if id, err := strconv.Atoi(barcode[:dashIdx]); err == nil {
			return id, true
		}
	}
	// Просто число
	if id, err := strconv.Atoi(barcode); err == nil {
		return id, true
	}
	return 0, false
}
