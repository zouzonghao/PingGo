package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"ping-go/config"
	"ping-go/db"
	"ping-go/monitor"
	"ping-go/server"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata"
)

//go:embed dist/*
var distFS embed.FS

func main() {
	log.Println("Starting ping-go...")

	// Load Config
	if err := config.LoadConfig("config.yaml"); err != nil {
		log.Printf("Failed to load config.yaml: %v. Using defaults/env vars if available.", err)
	}

	// Initialize Database
	if err := db.Init("pinggo.db"); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize Monitor Service
	monitorService := monitor.NewService()

	// Static Files (Embedded)
	distRoot, _ := fs.Sub(distFS, "dist")
	staticFS := http.FS(distRoot)

	// Initialize Web Server
	srv := server.NewServer(monitorService, staticFS)
	srv.SetStatic(staticFS)

	// Start Monitoring AFTER server initialization to ensure OnStatusChange is set
	monitorService.Start()

	// Check for RESEND_API_KEY
	if config.GlobalConfig.Notification.ResendAPIKey == "" {
		log.Println("Warning: RESEND_API_KEY is not set in config.yaml. Email notifications will fail.")
	}

	// Run Server
	port := ":3001"
	if config.GlobalConfig.Server.Port != 0 {
		port = ":" + strconv.Itoa(config.GlobalConfig.Server.Port)
	}

	httpSrv := &http.Server{
		Addr:    port,
		Handler: srv.Router(), // Use Getter for router
	}

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		log.Printf("Server listening on %s", port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with
	// a timeout of 5 seconds.
	quit := make(chan os.Signal, 1)
	// kill (no param) default send syscall.SIGTERM
	// kill -2 is syscall.SIGINT
	// kill -9 is syscall.SIGKILL but can't be caught, so no need to add it
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	// Stop Monitor Service
	log.Println("Stopping monitor service...")
	monitorService.StopAll()

	// Close Database (includes flushing buffer)
	db.Close()

	log.Println("Server exiting")
}
