package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"club-membership-system/internal/config"
	"club-membership-system/internal/membership"
	"club-membership-system/internal/resilience"
	pb "club-membership-system/proto"

	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// positiveInt reads a positive integer from an environment variable.
func positiveInt(name string) (int, error) {
	value, err := config.Required(name)
	if err != nil {
		return 0, err
	}

	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}

	return number, nil
}

func main() {
	if err := run(); err != nil {
		log.Printf("membership service stopped with error: %v", err)
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

	clubAddress, err := config.Required("CLUB_SERVICE_ADDRESS")
	if err != nil {
		return err
	}

	startupTimeout, err := config.PositiveDuration("DB_STARTUP_TIMEOUT")
	if err != nil {
		return err
	}

	callTimeout, err := config.PositiveDuration("CLUB_CALL_TIMEOUT")
	if err != nil {
		return err
	}

	backoff, err := config.PositiveDuration("CLUB_RETRY_BACKOFF")
	if err != nil {
		return err
	}

	openDuration, err := config.PositiveDuration("CLUB_CIRCUIT_OPEN_DURATION")
	if err != nil {
		return err
	}

	attempts, err := positiveInt("CLUB_MAX_ATTEMPTS")
	if err != nil {
		return err
	}

	threshold, err := positiveInt("CLUB_CIRCUIT_THRESHOLD")
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

	log.Println("connected to Membership database")

	// The client connects over Docker's internal network.
	// NewClient creates the client; RPCs establish the connection.
	connection, err := grpc.NewClient(
		clubAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	defer connection.Close()

	clubClient, err := resilience.NewClubClient(
		pb.NewClubServiceClient(connection),
		callTimeout,
		attempts,
		backoff,
		threshold,
		openDuration,
	)
	if err != nil {
		return err
	}

	log.Printf(
		"Club client configured: target=%s timeout=%s attempts=%d "+
			"backoff=%s circuit_threshold=%d open_duration=%s",
		clubAddress,
		callTimeout,
		attempts,
		backoff,
		threshold,
		openDuration,
	)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := grpc.NewServer()
	pb.RegisterMembershipServiceServer(
		server,
		membership.NewService(db, clubClient),
	)

	stopContext, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	go func() {
		<-stopContext.Done()
		log.Println("shutting down Membership Service")

		timer := time.AfterFunc(5*time.Second, server.Stop)
		server.GracefulStop()
		timer.Stop()
	}()

	log.Printf("Membership Service listening on %s", address)
	return server.Serve(listener)
}
