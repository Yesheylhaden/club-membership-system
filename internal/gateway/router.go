package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	pb "club-membership-system/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewRouter exposes REST endpoints backed by gRPC calls.
func NewRouter(
	clubs pb.ClubServiceClient,
	memberships pb.MembershipServiceClient,
	timeout time.Duration,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": "api-gateway",
		})
	})

	mux.HandleFunc("POST /clubs", unary(
		timeout, http.StatusCreated, true, nil,
		clubs.CreateClub,
	))

	mux.HandleFunc("GET /clubs", unary(
		timeout, http.StatusOK, false, nil,
		clubs.ListClubs,
	))

	mux.HandleFunc("GET /clubs/{id}", unary(
		timeout, http.StatusOK, false,
		func(req *pb.GetClubRequest, id int64) {
			req.Id = id
		},
		clubs.GetClub,
	))

	mux.HandleFunc("PUT /clubs/{id}", unary(
		timeout, http.StatusOK, true,
		func(req *pb.UpdateClubRequest, id int64) {
			req.Id = id
		},
		clubs.UpdateClub,
	))

	mux.HandleFunc("DELETE /clubs/{id}", unary(
		timeout, http.StatusOK, false,
		func(req *pb.DeleteClubRequest, id int64) {
			req.Id = id
		},
		clubs.DeleteClub,
	))

	mux.HandleFunc("POST /memberships", unary(
		timeout, http.StatusCreated, true, nil,
		memberships.CreateMembership,
	))

	mux.HandleFunc("GET /memberships", unary(
		timeout, http.StatusOK, false, nil,
		memberships.ListMemberships,
	))

	mux.HandleFunc("GET /memberships/{id}", unary(
		timeout, http.StatusOK, false,
		func(req *pb.GetMembershipRequest, id int64) {
			req.Id = id
		},
		memberships.GetMembership,
	))

	mux.HandleFunc("PUT /memberships/{id}", unary(
		timeout, http.StatusOK, true,
		func(req *pb.UpdateMembershipRequest, id int64) {
			req.Id = id
		},
		memberships.UpdateMembership,
	))

	mux.HandleFunc("DELETE /memberships/{id}", unary(
		timeout, http.StatusOK, false,
		func(req *pb.DeleteMembershipRequest, id int64) {
			req.Id = id
		},
		memberships.DeleteMembership,
	))

	return mux
}

// unary handles JSON, path IDs, deadlines, and gRPC error mapping.
// Resource validation also runs inside the owning service.
func unary[Request any, Response any](
	timeout time.Duration,
	successStatus int,
	hasBody bool,
	setID func(*Request, int64),
	call func(
		context.Context, *Request, ...grpc.CallOption,
	) (Response, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(Request)

		if hasBody {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()

			if err := decoder.Decode(req); err != nil {
				var sizeError *http.MaxBytesError
				if errors.As(err, &sizeError) {
					writeError(
						w, http.StatusRequestEntityTooLarge,
						"request body exceeds 1 MB",
					)
					return
				}

				writeError(
					w, http.StatusBadRequest,
					"invalid JSON body or unknown field",
				)
				return
			}

			// Reject multiple JSON values or trailing invalid content.
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				writeError(
					w, http.StatusBadRequest,
					"body must contain exactly one JSON object",
				)
				return
			}
		}

		if setID != nil {
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil || id <= 0 {
				writeError(
					w, http.StatusBadRequest,
					"ID must be a positive integer",
				)
				return
			}

			// The URL determines the ID for update requests.
			setID(req, id)
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		log.Printf("gateway request: %s %s", r.Method, r.URL.Path)

		result, err := call(ctx, req)
		if err != nil {
			writeGRPCError(w, err)
			return
		}

		writeJSON(w, successStatus, result)
	}
}

func writeGRPCError(w http.ResponseWriter, err error) {
	grpcStatus := status.Convert(err)

	var httpStatus int

	switch grpcStatus.Code() {
	case codes.InvalidArgument, codes.OutOfRange:
		httpStatus = http.StatusBadRequest
	case codes.NotFound:
		httpStatus = http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		httpStatus = http.StatusConflict
	case codes.Unauthenticated:
		httpStatus = http.StatusUnauthorized
	case codes.PermissionDenied:
		httpStatus = http.StatusForbidden
	case codes.ResourceExhausted:
		httpStatus = http.StatusTooManyRequests
	case codes.Unavailable:
		httpStatus = http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		httpStatus = http.StatusGatewayTimeout
	case codes.Canceled:
		httpStatus = http.StatusRequestTimeout
	default:
		httpStatus = http.StatusInternalServerError
	}

	message := grpcStatus.Message()
	if httpStatus == http.StatusInternalServerError {
		log.Printf("gateway upstream error: %v", err)
		message = "internal service error"
	}

	writeJSON(w, httpStatus, map[string]string{
		"error": message,
		"code":  grpcStatus.Code().String(),
	})
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{
		"error": message,
	})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("failed to encode gateway response: %v", err)
	}
}
