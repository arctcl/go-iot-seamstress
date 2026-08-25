package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	server := "http://localhost:8080"
	flag.StringVar(&server, "url", server, "server base URL (example: http://localhost:8080)")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Second}
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Debug scanner console — команды: scan <barcode>, end")
	for {
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка чтения ввода:", err)
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		cmd := strings.ToLower(parts[0])
		switch cmd {
		case "end", "exit", "quit":
			fmt.Println("Выход")
			return
		case "scan":
			if len(parts) < 2 {
				fmt.Println("Использование: scan <barcode>")
				continue
			}
			barcode := strings.Join(parts[1:], " ")
			payload := map[string]string{"barcode": barcode}
			b, _ := json.Marshal(payload)
			req, _ := http.NewRequest("POST", server+"/api/scan", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				fmt.Println("Ошибка запроса:", err)
				continue
			}
			buf := new(bytes.Buffer)
			buf.ReadFrom(resp.Body)
			resp.Body.Close()
			fmt.Printf("Status: %s\nBody: %s\n", resp.Status, buf.String())
		default:
			fmt.Println("Неизвестная команда. Используйте: scan <barcode> | end")
		}
	}
}
