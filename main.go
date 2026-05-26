package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
<!DOCTYPE html>
<html>
<head><title>Вишлист</title></head>
<body>
<h1>🎁 Мой Вишлист</h1>
<div style="display: flex; gap: 20px;">
	<div style="border:1px solid #ddd; padding:15px">
		<h3>Мышь</h3>
		<p>3500 ₽</p>
		<button>Купить</button>
	</div>
	<div style="border:1px solid #ddd; padding:15px">
		<h3>Клавиатура</h3>
		<p>8000 ₽</p>
		<button>Купить</button>
	</div>
</div>
</body>
</html>
		`))
	})

	port := "9090" // другой порт
	fmt.Printf("Сервер запущен на http://localhost:%s\n", port)
	fmt.Println("Нажмите Ctrl+C для остановки")

	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		fmt.Println("Ошибка:", err)
	}
}
