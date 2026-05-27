// обработка HTTP-запросов.
package handlers

import (
	"html/template"
	"net/http"
	"strconv"
	"wishlist/internal/models"
	"wishlist/internal/storage"
)

func IndexHandler(w http.ResponseWriter, r *http.Request) {
	var total float64
	for _, item := range storage.Items {
		total += item.Price
	}
	tmpl := template.Must(template.ParseFiles("templates/index.html"))
	tmpl.Execute(w, map[string]interface{}{"Items": storage.Items, "Total": total})
}

func AddHandler(w http.ResponseWriter, r *http.Request) {
	price, _ := strconv.ParseFloat(r.FormValue("price"), 64)
	storage.Add(models.Gift{
		Name: r.FormValue("name"), Price: price, ImageURL: r.FormValue("image_url"),
		Link: r.FormValue("link"), Priority: r.FormValue("priority"),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func DeleteHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	storage.Delete(id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
