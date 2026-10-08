package membership

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"club-membership-system/internal/resilience"
	pb "club-membership-system/proto"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var studentIDPattern = regexp.MustCompile(`^[0-9]{8}$`)

// Service owns membership data.
// Club information is obtained exclusively through gRPC.
type Service struct {
	pb.UnimplementedMembershipServiceServer
	db    *sql.DB
	clubs *resilience.ClubClient
}

func NewService(
	db *sql.DB,
	clubs *resilience.ClubClient,
) *Service {
	return &Service{db: db, clubs: clubs}
}

func validateMembership(
	name, studentID string,
	clubID int64,
) (string, string, error) {
	name = strings.TrimSpace(name)
	studentID = strings.TrimSpace(studentID)

	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", "", status.Error(
			codes.InvalidArgument,
			"student name is required and must not exceed 100 characters",
		)
	}

	if !studentIDPattern.MatchString(studentID) {
		return "", "", status.Error(
			codes.InvalidArgument,
			"student ID must contain exactly 8 digits",
		)
	}

	if clubID <= 0 {
		return "", "", status.Error(
			codes.InvalidArgument,
			"club ID must be positive",
		)
	}

	return name, studentID, nil
}

func databaseError(
	ctx context.Context,
	operation string,
	err error,
) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}

	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) &&
		postgresError.Code == "23505" {
		return status.Error(
			codes.AlreadyExists,
			"student is already a member of this club",
		)
	}

	log.Printf(
		"membership database operation %s failed: %v",
		operation, err,
	)

	return status.Error(
		codes.Internal,
		"membership database operation failed",
	)
}

// attachClub retrieves current club details without reading its database.
func (s *Service) attachClub(
	ctx context.Context,
	item *pb.Membership,
) error {
	log.Printf(
		"Membership Service calling Club Service: club_id=%d",
		item.ClubId,
	)

	club, err := s.clubs.GetClub(ctx, item.ClubId)
	if err != nil {
		return err
	}

	item.Club = club
	return nil
}

func (s *Service) CreateMembership(
	ctx context.Context,
	req *pb.CreateMembershipRequest,
) (*pb.Membership, error) {
	name, studentID, err := validateMembership(
		req.GetStudentName(),
		req.GetStudentId(),
		req.GetClubId(),
	)
	if err != nil {
		return nil, err
	}

	item := &pb.Membership{
		StudentName: name,
		StudentId:   studentID,
		ClubId:      req.GetClubId(),
	}

	// Verify the club before writing the membership.
	if err := s.attachClub(ctx, item); err != nil {
		return nil, err
	}

	err = s.db.QueryRowContext(
		ctx,
		`INSERT INTO memberships (student_name, student_id, club_id)
		 VALUES ($1, $2, $3)
		 RETURNING id`,
		item.StudentName,
		item.StudentId,
		item.ClubId,
	).Scan(&item.Id)

	if err != nil {
		return nil, databaseError(ctx, "create", err)
	}

	log.Printf("membership created: id=%d", item.Id)
	return item, nil
}

func (s *Service) GetMembership(
	ctx context.Context,
	req *pb.GetMembershipRequest,
) (*pb.Membership, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(
			codes.InvalidArgument,
			"membership ID must be positive",
		)
	}

	item := &pb.Membership{}

	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, student_name, student_id, club_id
		 FROM memberships WHERE id = $1`,
		req.GetId(),
	).Scan(
		&item.Id,
		&item.StudentName,
		&item.StudentId,
		&item.ClubId,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(
			codes.NotFound, "membership not found",
		)
	}
	if err != nil {
		return nil, databaseError(ctx, "get", err)
	}

	if err := s.attachClub(ctx, item); err != nil {
		return nil, err
	}

	return item, nil
}

func (s *Service) ListMemberships(
	ctx context.Context,
	req *pb.ListMembershipsRequest,
) (*pb.ListMembershipsResponse, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, student_name, student_id, club_id
		 FROM memberships ORDER BY id`,
	)
	if err != nil {
		return nil, databaseError(ctx, "list", err)
	}

	result := &pb.ListMembershipsResponse{
		Memberships: make([]*pb.Membership, 0),
	}

	for rows.Next() {
		item := &pb.Membership{}

		if err := rows.Scan(
			&item.Id,
			&item.StudentName,
			&item.StudentId,
			&item.ClubId,
		); err != nil {
			rows.Close()
			return nil, databaseError(ctx, "scan", err)
		}

		result.Memberships = append(result.Memberships, item)
	}

	rowErr := rows.Err()
	rows.Close()

	if rowErr != nil {
		return nil, databaseError(ctx, "list rows", rowErr)
	}

	// Release database rows before making network calls.
	for _, item := range result.Memberships {
		if err := s.attachClub(ctx, item); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (s *Service) UpdateMembership(
	ctx context.Context,
	req *pb.UpdateMembershipRequest,
) (*pb.Membership, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(
			codes.InvalidArgument,
			"membership ID must be positive",
		)
	}

	name, studentID, err := validateMembership(
		req.GetStudentName(),
		req.GetStudentId(),
		req.GetClubId(),
	)
	if err != nil {
		return nil, err
	}

	// Return NotFound first if the membership does not exist.
	var exists bool
	err = s.db.QueryRowContext(
		ctx,
		`SELECT EXISTS (
			SELECT 1 FROM memberships WHERE id = $1
		)`,
		req.GetId(),
	).Scan(&exists)

	if err != nil {
		return nil, databaseError(ctx, "check membership", err)
	}
	if !exists {
		return nil, status.Error(
			codes.NotFound, "membership not found",
		)
	}

	item := &pb.Membership{
		Id:          req.GetId(),
		StudentName: name,
		StudentId:   studentID,
		ClubId:      req.GetClubId(),
	}

	if err := s.attachClub(ctx, item); err != nil {
		return nil, err
	}

	err = s.db.QueryRowContext(
		ctx,
		`UPDATE memberships
		 SET student_name = $1, student_id = $2, club_id = $3
		 WHERE id = $4
		 RETURNING id`,
		item.StudentName,
		item.StudentId,
		item.ClubId,
		item.Id,
	).Scan(&item.Id)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(
			codes.NotFound, "membership not found",
		)
	}
	if err != nil {
		return nil, databaseError(ctx, "update", err)
	}

	log.Printf("membership updated: id=%d", item.Id)
	return item, nil
}

func (s *Service) DeleteMembership(
	ctx context.Context,
	req *pb.DeleteMembershipRequest,
) (*pb.DeleteMembershipResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(
			codes.InvalidArgument,
			"membership ID must be positive",
		)
	}

	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM memberships WHERE id = $1`,
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
		return nil, status.Error(
			codes.NotFound, "membership not found",
		)
	}

	log.Printf("membership deleted: id=%d", req.GetId())

	return &pb.DeleteMembershipResponse{
		Message: "membership deleted successfully",
	}, nil
}
