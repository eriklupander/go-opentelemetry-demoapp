package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"

	"go.opentelemetry.io/contrib/instrumentation/go.mongodb.org/mongo-driver/mongo/otelmongo"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
	"io"
	"net/http"
)

const app = "inventory-service"

func main() {

	tp := setupTracing()
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			panic(err.Error())
		}
	}()
	inventory := setupMongo()

	cl := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(telemetry)

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {

		// use Mongo client to look up something
		sr := inventory.FindOne(r.Context(), bson.D{
			{Key: "item", Value: "canvas"},
		})
		raw, _ := sr.DecodeBytes()
		fmt.Printf("SR: %v\n", raw)

		// use a HTTP client to call something else with instrumentation
		newReq, _ := http.NewRequest("GET", "http://localhost:4444/supplier?id=4", nil)
		newReq = newReq.WithContext(r.Context())
		resp, err := cl.Do(newReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = io.ReadAll(resp.Body)
		defer resp.Body.Close()

		_, _ = w.Write([]byte(`{"some":"data"}`))
	})

	if err := http.ListenAndServe(":3333", r); err != nil {
		panic(err.Error())
	}
}

func setupMongo() *mongo.Collection {
	opts := options.Client()
	opts.Monitor = otelmongo.NewMonitor()
	opts.ApplyURI("mongodb://localhost:27017")
	client, err := mongo.Connect(context.Background(), opts)
	if err != nil {
		panic(err)
	}
	db := client.Database("example")
	inventory := db.Collection("inventory")
	sr := inventory.FindOne(context.Background(), bson.D{
		{Key: "item", Value: "canvas"},
	})
	if sr != nil && sr.Err() != nil && errors.Is(sr.Err(), mongo.ErrNoDocuments) {
		_, err = inventory.InsertOne(context.Background(), bson.D{
			{Key: "item", Value: "canvas"},
			{Key: "qty", Value: 100},
			{Key: "attributes", Value: bson.A{"cotton"}},
			{Key: "size", Value: bson.D{
				{Key: "h", Value: 28},
				{Key: "w", Value: 35.5},
				{Key: "uom", Value: "cm"},
			}},
		})
		if err != nil {
			panic(err)
		}
		fmt.Println("Inserted into Mongo")
	} else if sr != nil && sr.Err() != nil {
		panic(sr.Err())
	}
	return inventory
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
			semconv.ServiceVersion("v0.0.1"),
			attribute.String("environment", "test")),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost())
	if err != nil {
		panic(err.Error())
	}

	return def
}
