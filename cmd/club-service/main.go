package main

import (
	"context"
	"database/sql"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"club-membership-system/internal/club"
	"club-membership-system/internal/config"
	pb "club-membership-system/proto"

	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		log.Printf("club service stopped with error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL, err := config.Required("DATABASE_URL")
	if err != nil {
		return err
	}

	address, err := config.Required("GRPC_ADDRESS")
	if err != nil {
		return err
	}

	startupTimeout, err := config.PositiveDuration("DB_STARTUP_TIMEOUT")
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(
		context.Background(), startupTimeout,
	)
	err = db.PingContext(ctx)
	cancel()

	if err != nil {
		return err
	}

	log.Println("connected to Club database")

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := grpc.NewServer()
	pb.RegisterClubServiceServer(server, club.NewService(db))

	// Allow Docker to stop the service gracefully.
	stopContext, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	go func() {
		<-stopContext.Done()
		log.Println("shutting down Club Service")

		// Force shutdown if active requests do not finish promptly.
		timer := time.AfterFunc(5*time.Second, server.Stop)
		server.GracefulStop()
		timer.Stop()
	}()

	log.Printf("Club Service listening on %s", address)
	return server.Serve(listener)
}
