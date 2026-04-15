package worker

import (
	"log"

	forgev1 "github.com/castwell/forge/api/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Heartbeat implements the WorkerService Heartbeat RPC.
// The coordinator opens a bidirectional stream, sends Pings every 10s,
// and the worker responds with Pongs containing current status.
func (w *Worker) Heartbeat(stream grpc.BidiStreamingServer[forgev1.HeartbeatPing, forgev1.HeartbeatPong]) error {
	log.Printf("INFO: heartbeat stream opened for worker %s", w.id)

	for {
		ping, err := stream.Recv()
		if err != nil {
			log.Printf("INFO: heartbeat stream closed for worker %s: %v", w.id, err)
			return err
		}

		_ = ping // We acknowledge the ping timestamp but don't need to use it.

		pong := &forgev1.HeartbeatPong{
			WorkerId:    w.id,
			ActiveTasks: w.active.Load(),
			Capacity:    int32(w.capacity),
			Timestamp:   timestamppb.Now(),
		}

		if err := stream.Send(pong); err != nil {
			log.Printf("ERROR: send heartbeat pong for worker %s: %v", w.id, err)
			return err
		}
	}
}
