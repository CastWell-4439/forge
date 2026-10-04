package coordinator

import (
	"context"
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	forgev1 "github.com/castwell/forge/api/proto/gen"
)

// Register implements the WorkerService Register RPC on the coordinator's
// own server, which is how an out-of-process worker announces itself:
// "worker-N at addr, handlers [...], capacity N".
//
// Before this existed the worker dialed WorkerService/Register on the
// coordinator and got "unknown service" — the registration RPC had no
// server-side at all (tests only ever called this package's RegisterWorker
// method in-process), so a production worker died the moment it started.
// The worker-side handler keeps its own Register for the symmetric case;
// Heartbeat and ExecuteTask stay Unimplemented here by design — a
// coordinator does not execute tasks or answer pings.
func (c *Coordinator) Register(ctx context.Context, req *forgev1.RegisterRequest) (*forgev1.RegisterResponse, error) {
	reg := req.GetRegistration()
	if reg == nil || reg.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "registration with an id is required")
	}
	if reg.GetAddr() == "" {
		return nil, status.Error(codes.InvalidArgument, "registration address is required")
	}

	if err := c.RegisterWorker(ctx, reg.GetId(), reg.GetAddr(), reg.GetHandlers(), int(reg.GetCapacity())); err != nil {
		return nil, status.Errorf(codes.Internal, "register worker %s: %v", reg.GetId(), err)
	}
	log.Printf("INFO: worker %s registered at %s (capacity=%d, handlers=%v)",
		reg.GetId(), reg.GetAddr(), reg.GetCapacity(), reg.GetHandlers())
	return &forgev1.RegisterResponse{Accepted: true}, nil
}
