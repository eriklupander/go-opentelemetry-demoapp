package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/XSAM/otelsql"

	_ "github.com/go-sql-driver/mysql"

	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	"net/http"
)

const app = "supplier-service"

func main() {
	fmt.Println("Hello world")

	var mysqlDSN = "root:my-secret-pw@tcp(127.0.0.1:3306)/test"

	db, err := otelsql.Open("mysql", mysqlDSN, otelsql.WithAttributes(
		semconv.DBSystemMySQL,
	), otelsql.WithSQLCommenter(true))
	if err != nil {
		panic(err)
	}
	defer db.Close()

	pstmt, err := db.Prepare("SELECT s.ID, s.NAME FROM SUPPLIER s WHERE s.ID = ?")
	if err != nil {
		panic(err)
	}

	tp := setupTracing()
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			panic(err.Error())
		}
	}()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(telemetry)

	r.Get("/supplier", func(w http.ResponseWriter, r *http.Request) {

		// get some data from MySQL
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing query param id", http.StatusBadRequest)
			return
		}

		rows, err := pstmt.QueryContext(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		defer rows.Close()
		for rows.Next() {
			if rows.Err() != nil {
				http.Error(w, rows.Err().Error(), http.StatusInternalServerError)
				return
			}
			tpl := struct {
				ID   string
				Name string
			}{}
			if err := rows.Scan(&tpl.ID, &tpl.Name); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			body, err := json.Marshal(tpl)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}

		http.Error(w, "Not found", http.StatusNotFound)
	})

	if err := http.ListenAndServe(":4444", r); err != nil {
		panic(err.Error())
	}
}

func setupTracing() *trace.TracerProvider {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}))
	exp, err := newExporter()
	if err != nil {
		panic(err.Error())
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(exp),
		trace.WithResource(newResource()),
	)

	otel.SetTracerProvider(tp)
	return tp
}

func telemetry(next http.Handler) http.Handler {

	//	return
	fn := func(w http.ResponseWriter, r *http.Request) {
		otelhttp.NewHandler(next, r.RequestURI, otelhttp.WithPropagators(propagation.TraceContext{})).
			ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}

func newExporter() (*jaeger.Exporter, error) {
	return jaeger.New(jaeger.WithAgentEndpoint())
}

// newResource returns a resource describing this application.
func newResource() *resource.Resource {

	def, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(app),
			semconv.ServiceVersion("v0.3.1"),
			attribute.String("environment", "test")),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost())
	if err != nil {
		panic(err.Error())
	}

	return def
}
