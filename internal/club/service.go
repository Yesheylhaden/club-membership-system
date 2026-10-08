package club

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"unicode/utf8"

	pb "club-membership-system/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Service owns club data and implements the generated gRPC interface.
type Service struct {
	pb.UnimplementedClubServiceServer
	db *sql.DB
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// validateClub normalizes and validates data before it reaches the database.
func validateClub(name, description string) (string, string, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)

	if name == "" {
		return "", "", status.Error(
			codes.InvalidArgument, "club name is required",
		)
	}

	if utf8.RuneCountInString(name) > 100 {
		return "", "", status.Error(
			codes.InvalidArgument,
			"club name must not exceed 100 characters",
		)
	}

	if utf8.RuneCountInString(description) > 500 {
		return "", "", status.Error(
			codes.InvalidArgument,
			"description must not exceed 500 characters",
		)
	}

	return name, description, nil
}

// databaseError logs technical details but returns a safe client message.
func databaseError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}

	log.Printf("club database operation %s failed: %v", operation, err)

	return status.Error(codes.Internal, "club database operation failed")
}

func (s *Service) CreateClub(
	ctx context.Context,
	req *pb.CreateClubRequest,
) (*pb.Club, error) {
	name, description, err := validateClub(
		req.GetName(), req.GetDescription(),
	)
	if err != nil {
		return nil, err
	}

	result := &pb.Club{}

	err = s.db.QueryRowContext(
		ctx,
		`INSERT INTO clubs (name, description)
		 VALUES ($1, $2)
		 RETURNING id, name, description`,
		name, description,
	).Scan(&result.Id, &result.Name, &result.Description)

	if err != nil {
		return nil, databaseError(ctx, "create", err)
	}

	log.Printf("club created: id=%d", result.Id)
	return result, nil
}

func (s *Service) GetClub(
	ctx context.Context,
	req *pb.GetClubRequest,
) (*pb.Club, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(
			codes.InvalidArgument, "club ID must be positive",
		)
	}

	result := &pb.Club{}

	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, name, description FROM clubs WHERE id = $1`,
		req.GetId(),
	).Scan(&result.Id, &result.Name, &result.Description)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "club not found")
	}
	if err != nil {
		return nil, databaseError(ctx, "get", err)
	}

	log.Printf("club retrieved through gRPC: id=%d", result.Id)
	return result, nil
}

func (s *Service) ListClubs(
	ctx context.Context,
	req *pb.ListClubsRequest,
) (*pb.ListClubsResponse, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, description FROM clubs ORDER BY id`,
	)
	if err != nil {
		return nil, databaseError(ctx, "list", err)
	}
	defer rows.Close()

	result := &pb.ListClubsResponse{
		Clubs: make([]*pb.Club, 0),
	}

	for rows.Next() {
		item := &pb.Club{}

		if err := rows.Scan(
			&item.Id, &item.Name, &item.Description,
		); err != nil {
			return nil, databaseError(ctx, "scan", err)
		}

		result.Clubs = append(result.Clubs, item)
	}

	if err := rows.Err(); err != nil {
		return nil, databaseError(ctx, "list rows", err)
	}

	return result, nil
}

func (s *Service) UpdateClub(
	ctx context.Context,
	req *pb.UpdateClubRequest,
) (*pb.Club, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(
			codes.InvalidArgument, "club ID must be positive",
		)
	}

	name, description, err := validateClub(
		req.GetName(), req.GetDescription(),
	)
	if err != nil {
		return nil, err
	}

	result := &pb.Club{}

	err = s.db.QueryRowContext(
		ctx,
		`UPDATE clubs
		 SET name = $1, description = $2
		 WHERE id = $3
		 RETURNING id, name, description`,
		name, description, req.GetId(),
	).Scan(&result.Id, &result.Name, &result.Description)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "club not found")
	}
	if err != nil {
		return nil, databaseError(ctx, "update", err)
	}

	log.Printf("club updated: id=%d", result.Id)
	return result, nil
}

func (s *Service) DeleteClub(
	ctx context.Context,
	req *pb.DeleteClubRequest,
) (*pb.DeleteClubResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(
			codes.InvalidArgument, "club ID must be positive",
		)
	}

	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM clubs WHERE id = $1`,
		req.GetId(),
	)
	if err != nil {
		return nil, databaseError(ctx, "delete", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return nil, databaseError(ctx, "delete count", err)
	}

	if count == 0 {
		return nil, status.Error(codes.NotFound, "club not found")
	}

	log.Printf("club deleted: id=%d", req.GetId())

	return &pb.DeleteClubResponse{
		Message: "club deleted successfully",
	}, nil
}
