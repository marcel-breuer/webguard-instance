package health

import (
	"context"
	"log"
	"net/http"
	"time"
)

func Start(ctx context.Context, address string, logger *log.Logger) error {
	server := &http.Server{
		Addr:              address,
		Handler:           HealthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	if logger != nil {
		logger.Printf("Health server listening on %s", address)
	}

	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func HealthHandler() http.Handler {
	mux := http.NewServeMux()
	healthHandler := func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}

	mux.HandleFunc("/", healthHandler)
	mux.HandleFunc("/health", healthHandler)

	return mux
}
