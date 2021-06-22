package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

func main() {
	http.HandleFunc("/order", Order())
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func Order() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		baseURL := fmt.Sprintf("http://%s", os.Getenv("PAYMENT_SERVICE_HOST"))
		res, err := http.Post(baseURL+"/payments/id/api/v1/", "application/json", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		dec := json.NewDecoder(res.Body)

		var p PaymentResponse
		err = dec.Decode(&p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Write([]byte(fmt.Sprintf("merchant %s received payment %f USD", p.MerchantID, p.Value)))
	}
}

type PaymentResponse struct {
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ID         uuid.UUID `gorm:"type:uuid;not null" json:"id"`
	Value      float64   `gorm:"type:float" json:"value"`
	MerchantID uuid.UUID `gorm:"type:uuid;not null" json:"merchant_id"`
	Region     string    `gorm:"type:text" json:"region"`
}
