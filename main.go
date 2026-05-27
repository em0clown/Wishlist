package main

import (
	"fmt"
	"net/http"
	"wishlist/internal/handlers"
)

func main() {
	// Раздача статики
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))

	// Роуты
	http.HandleFunc("/", handlers.IndexHandler)
	http.HandleFunc("/add", handlers.AddHandler)
	http.HandleFunc("/delete", handlers.DeleteHandler)

	fmt.Println("Сервер запущен на http://localhost:9090")
	err := http.ListenAndServe(":9090", nil)
	if err != nil {
		fmt.Println("Ошибка запуска сервера:", err)
	}
}
