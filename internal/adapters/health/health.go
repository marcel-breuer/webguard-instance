package health

import (
	"context"
	"log"
	"net/http"
	"time"
)

func StartWithHandler(ctx context.Context, address string, logger *log.Logger, handler http.Handler) error {
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
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
