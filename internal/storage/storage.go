// логика работы со списком.
package storage

import (
	"wishlist/internal/models"
)

var (
	Items  []models.Gift
	NextID = 1
)

func Add(gift models.Gift) {
	gift.ID = NextID
	Items = append(Items, gift)
	NextID++
}

func Delete(id int) {
	for i, item := range Items {
		if item.ID == id {
			Items = append(Items[:i], Items[i+1:]...)
			break
		}
	}
}
