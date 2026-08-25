package main

import (
	"fmt"
)

// generateOrderCode генерирует полный код заказа: zkz00001
func generateOrderCode(id int) string {
	return fmt.Sprintf("%s%05d", getBarcodePrefix(), id)
}

// generateBoxCode генерирует вложенный код коробки: zkz00001bx00005 или zkz00001br00005
// orderCode — код заказа (zkz00001), boxNum — номер коробки внутри заказа
func generateBoxCode(orderCode string, boxNum int, isBrak bool) string {
	prefix := getFormulasString("печать", "префикс_коробки")
	if isBrak {
		prefix = getFormulasString("печать", "префикс_брак")
	}
	if prefix == "" {
		prefix = "bx"
		if isBrak {
			prefix = "br"
		}
	}
	return fmt.Sprintf("%s%s%05d", orderCode, prefix, boxNum)
}

// generateOpCode генерирует вложенный код операции: zkz00001bx00005op00003
// boxCode — полный код коробки (zkz00001bx00005), opNum — шаг операции
func generateOpCode(boxCode string, opNum int) string {
	pref := getFormulasString("печать", "префикс_операции")
	if pref == "" {
		pref = "op"
	}
	return fmt.Sprintf("%s%s%05d", boxCode, pref, opNum)
}

// generateOpCodeFromTc генерирует код операции из id техкарты (для начального создания)
func generateOpCodeFromTc(tcID int) string {
	return fmt.Sprintf("op%05d", tcID)
}

// generateBarcode собирает полный ШК из готовых кодов (для печати)
func generateBarcode(orderCode, boxCode, opCode string) string {
	sep1 := getFormulasString("печать", "разделитель1")
	if sep1 == "" {
		sep1 = "X"
	}
	sep2 := getFormulasString("печать", "разделитель2")
	if sep2 == "" {
		sep2 = "Z"
	}
	return "*" + orderCode + sep1 + boxCode + sep2 + opCode + "*"
}

func getBarcodePrefix() string {
	formulasMu.RLock()
	defer formulasMu.RUnlock()
	if p := getFormulasString("печать", "префикс_заказа"); p != "" {
		return p
	}
	return "zkz"
}

func getFormulasString(section, key string) string {
	if m, ok := formulas[section].(map[string]interface{}); ok {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

func getFormulasIntDefault(section, key string, def int) int {
	if m, ok := formulas[section].(map[string]interface{}); ok {
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
	}
	return def
}
