package prometheus

import (
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
	grace "github.com/matthewmueller/go-grace"
)

// API struct
type API struct {
	handler http.Handler
	metrics metricStore
}

// Settings struct
type Settings struct {
	MetricsPath         string
	PersistenceFile     string
	PersistenceInterval time.Duration
}

// NewPushGateway API
func NewPushGateway(settings *Settings) *API {
	api := &API{}
	handler := &Handler{}
	ms := &handler.metrics

	// handlers for pushing and deleting metrics
	// same API as: https://github.com/prometheus/pushgateway
	r := httprouter.New()
	r.Handler("GET", settings.MetricsPath, handler)
	r.POST("/metrics/job/:job/*labels", api.create(ms))
	r.DELETE("/metrics/job/:job/*labels", api.delete(ms))
	r.PUT("/metrics/job/:job", api.upsert(ms))
	r.POST("/metrics/job/:job", api.create(ms))
	r.DELETE("/metrics/job/:job", api.delete(ms))
	api.handler = r

	return api
}

// Listen to addr
func (a *API) Listen(addr string) error {
	return grace.Listen(addr, a.handler)
}

func (a *API) create(store *metricStore) func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		// job := ps.ByName("job")
		// labelsString := ps.ByName("labels")
		// store.update(metric metric, buckets []float64)
	}
}

func (a *API) upsert(store *metricStore) func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		// job := ps.ByName("job")
		// labelsString := ps.ByName("labels")
		// store.update(metric metric, buckets []float64)
	}
}

func (a *API) delete(store *metricStore) func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		// job := ps.ByName("job")
		// labelsString := ps.ByName("labels")
		// store.update(metric metric, buckets []float64)
	}
}
