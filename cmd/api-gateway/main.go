package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"club-membership-system/internal/config"
	"club-membership-system/internal/gateway"
	pb "club-membership-system/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if err := run(); err != nil {
		log.Printf("API Gateway stopped with error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	address, err := config.Required("HTTP_ADDRESS")
	if err != nil {
		return err
	}

	clubAddress, err := config.Required("CLUB_SERVICE_ADDRESS")
	if err != nil {
		return err
	}

	membershipAddress, err := config.Required("MEMBERSHIP_SERVICE_ADDRESS")
	if err != nil {
		return err
	}

	requestTimeout, err := config.PositiveDuration("GATEWAY_REQUEST_TIMEOUT")
	if err != nil {
		return err
	}

	clubConnection, err := grpc.NewClient(
		clubAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	defer clubConnection.Close()

	membershipConnection, err := grpc.NewClient(
		membershipAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	defer membershipConnection.Close()

	server := &http.Server{
		Addr: address,
		Handler: gateway.NewRouter(
			pb.NewClubServiceClient(clubConnection),
			pb.NewMembershipServiceClient(membershipConnection),
			requestTimeout,
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      requestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stopContext, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// Return server errors to the main goroutine.
	serverErrors := make(chan error, 1)

	go func() {
		log.Printf("API Gateway listening on %s", address)
		log.Printf(
			"gRPC targets: clubs=%s memberships=%s",
			clubAddress,
			membershipAddress,
		)

		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err

	case <-stopContext.Done():
		log.Println("shutting down API Gateway")

		shutdownContext, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		if err := server.Shutdown(shutdownContext); err != nil {
			server.Close()
			return err
		}

		return nil
	}
}
