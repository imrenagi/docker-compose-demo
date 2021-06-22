package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"
)

type resp struct {
	UUID string `json:"uuid"`
}

func main() {

	dsn := fmt.Sprintf("host=%s port=%s user=%s DB.name=%s password=%s sslmode=disable", 
		os.Getenv("POSTGRES_HOST"), 
		"5432", 
		os.Getenv("POSTGRES_USER"), 
		os.Getenv("POSTGRES_DB"), 
		os.Getenv("POSTGRES_PASSWORD"))
	log.Debug().Msg(dsn)
	gormDB, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{})
	if err != nil {
		log.Fatal().Err(err).Msg("unable to create db connection")
	}

	replicaIPAddresses := os.Getenv("POSTGRES_REPLICA_IPS")
	ips := strings.Split(replicaIPAddresses, ",")

	err = gormDB.AutoMigrate(&Payment{})

	var dialectors []gorm.Dialector
	for _, ip := range ips {

		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}

		rdsn := fmt.Sprintf("host=%s port=%s user=%s DB.name=%s password=%s sslmode=disable", ip, "5432", os.Getenv("POSTGRES_USER"), os.Getenv("POSTGRES_DB"), os.Getenv("POSTGRES_PASSWORD"))
		config := postgres.Config{
			DSN:                  rdsn,
		}
		dialectors = append(dialectors, postgres.New(config))
	}

	if len(ips) > 0 {
		err := gormDB.Use(dbresolver.Register(dbresolver.Config{
			Replicas: dialectors,
			Policy:   dbresolver.RandomPolicy{},
		}),
		)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to setup gorm plugin")
		}
		log.Debug().Msg("replicas are registered")
	}

	router := mux.NewRouter()
	srv := &Server{
		Router: router,
		db: gormDB,
	}

	srv.routesV1()

	srv.Run(context.Background(), 8080)
}

// Server ...
type Server struct {
	Router *mux.Router
	stopCh chan struct{}

	db *gorm.DB
}

// Run ...
func (g *Server) Run(ctx context.Context, port int) {

	httpS := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: g.Router,
	}

	// Start listener
	conn, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatal().Err(err).Msgf("failed listen")
	}

	log.Info().Msgf("payment service serving on port %d ", port)

	go func() { g.checkServeErr("httpS", httpS.Serve(conn)) }()

	g.stopCh = make(chan struct{})
	<-g.stopCh
	if err := conn.Close(); err != nil {
		panic(err)
	}
}

// checkServeErr checks the error from a .Serve() call to decide if it was a graceful shutdown
func (g *Server) checkServeErr(name string, err error) {
	if err != nil {
		if g.stopCh == nil {
			// a nil stopCh indicates a graceful shutdown
			log.Info().Msgf("graceful shutdown %s: %v", name, err)
		} else {
			log.Fatal().Msgf("%s: %v", name, err)
		}
	} else {
		log.Info().Msgf("graceful shutdown %s", name)
	}
}

func (g *Server) routesV1() {

	g.Router.HandleFunc("/", hcHandler())

	// serve api
	api := g.Router.PathPrefix(fmt.Sprintf("/payments/%s/api/v1/", os.Getenv("COUNTRY_CODE"))).Subrouter()
	api.HandleFunc("/", listPayment(g.db)).Methods("GET")
	api.HandleFunc("/", createPayment(g.db)).Methods("POST")
}

func region() ([]byte, error) {

	req, err := http.NewRequest(http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/instance/region", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Metadata-Flavor", "Google")
	httpRes, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	bodyBytes, err := ioutil.ReadAll(httpRes.Body)
	if err != nil {
		return nil, nil
	}

	return bodyBytes, nil
}

func hcHandler() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		region, err := region()
		if err != nil {
			rw.Write([]byte(err.Error()))
			return
		}
		rw.Write(region)
	}
}

func listPayment(db *gorm.DB) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {

		if os.Getenv("FAIL") == "true" {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("failed to handle the request"))			
			return
		}

		var payments []Payment
		err := db.WithContext(r.Context()).Order("created_at desc").
			Limit(20).
			Find(&payments).Error
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte(err.Error()))			
			return
		}

		bytes, err := json.Marshal(payments)
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte(err.Error()))
			return
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.Write(bytes)
	}
}

type Payment struct {	
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	ID              uuid.UUID     `gorm:"type:uuid;not null" json:"id"`
	Value						float64 			`gorm:"type:float" json:"value"`
	MerchantID			uuid.UUID 		`gorm:"type:uuid;not null" json:"merchant_id"`
	Region 					string				`gorm:"type:text" json:"region"`
}

func (p *Payment) BeforeCreate(tx *gorm.DB) (err error) {
	uid, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	p.ID = uid
	return nil
}

func createPayment(db *gorm.DB) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {

		if os.Getenv("FAIL") == "true" {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte("failed to handle the request"))			
			return
		}

		region, err := region()
		if err != nil {
			region = []byte(`12345`)
		}

		payment := &Payment{
			ID: uuid.New(),
			Value: 1000,
			MerchantID: uuid.New(),
			Region: string(region),
		}

		err = db.WithContext(r.Context()).Save(payment).Error
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte(err.Error()))
			return
		}

		bytes, err := json.Marshal(payment)
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			rw.Write([]byte(err.Error()))
			return
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.Write(bytes)
	}
}
