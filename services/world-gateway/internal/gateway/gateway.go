// Package gateway implements pips.world.v1.WorldService.
//
// It is the only service browsers talk to. It aggregates sim-core and (later)
// the workplaces, and holds no domain logic of its own: every rule about pips
// lives in sim-core, every rule about a building in that building's service.
//
// The protocol asymmetry is the interesting part. sim-core serves plain gRPC
// via tonic; browsers cannot speak gRPC at all. Connect handles both from one
// .proto — this package is a Connect *handler* facing the browser and a Connect
// *client in gRPC mode* facing sim-core. That is what removes the Envoy proxy a
// gRPC-Web setup would otherwise need.
package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"connectrpc.com/connect"

	simv1 "github.com/teceer/pipsim/gen/go/pips/sim/v1"
	"github.com/teceer/pipsim/gen/go/pips/sim/v1/simv1connect"
	worldv1 "github.com/teceer/pipsim/gen/go/pips/world/v1"
)

// Server implements worldv1connect.WorldServiceHandler.
type Server struct {
	sim simv1connect.SimServiceClient

	// Reported to clients so their local WASM prediction starts from the same
	// world the server is running. A mismatch here makes prediction meaningless
	// in a way that looks like a rendering bug.
	simSeed uint64
	tickHz  int32
}

func New(sim simv1connect.SimServiceClient, simSeed uint64, tickHz int32) *Server {
	return &Server{sim: sim, simSeed: simSeed, tickHz: tickHz}
}

func (s *Server) JoinWorld(
	ctx context.Context,
	req *connect.Request[worldv1.JoinWorldRequest],
) (*connect.Response[worldv1.JoinWorldResponse], error) {
	snap, err := s.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	slog.Info("client joined",
		"client_id", req.Msg.GetClientId(),
		"tick", snap.Msg.GetTick(),
		"pips", len(snap.Msg.GetPips()))

	return connect.NewResponse(&worldv1.JoinWorldResponse{
		Tick:    snap.Msg.GetTick(),
		TickHz:  s.tickHz,
		SimSeed: s.simSeed,
		Pips:    snap.Msg.GetPips(),
		// Workplaces arrive once those services exist; the field is already in
		// the contract so adding them changes nothing here.
	}), nil
}

func (s *Server) StreamWorld(
	ctx context.Context,
	req *connect.Request[worldv1.StreamWorldRequest],
	stream *connect.ServerStream[worldv1.StreamWorldResponse],
) error {
	upstream, err := s.sim.WatchDeltas(ctx, connect.NewRequest(&simv1.WatchDeltasRequest{
		FromTick: req.Msg.GetFromTick(),
	}))
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	defer upstream.Close()

	clientID := req.Msg.GetClientId()
	slog.Info("stream opened", "client_id", clientID)
	sent := 0

	for upstream.Receive() {
		if err := stream.Send(&worldv1.StreamWorldResponse{
			Delta: upstream.Msg().GetDelta(),
		}); err != nil {
			// A client that hangs up mid-stream is routine, not an error worth
			// alerting on.
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				slog.Info("stream closed by client", "client_id", clientID, "sent", sent)
				return nil
			}
			return err
		}
		sent++
	}

	if err := upstream.Err(); err != nil && ctx.Err() == nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	slog.Info("stream ended", "client_id", clientID, "sent", sent)
	return nil
}

func (s *Server) AssignWork(
	ctx context.Context,
	req *connect.Request[worldv1.AssignWorkRequest],
) (*connect.Response[worldv1.AssignWorkResponse], error) {
	// A player action becomes an intent applied on the next tick boundary.
	// The gateway does not decide whether it is legal — sim-core does.
	res, err := s.sim.SubmitIntent(ctx, connect.NewRequest(&simv1.SubmitIntentRequest{
		Intent: &simv1.SubmitIntentRequest_Hire{
			Hire: &simv1.HireIntent{
				PipId:       req.Msg.GetPipId(),
				WorkplaceId: req.Msg.GetWorkplaceId(),
			},
		},
	}))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	return connect.NewResponse(&worldv1.AssignWorkResponse{
		Accepted: res.Msg.GetAccepted(),
		Reason:   res.Msg.GetRejectionReason(),
	}), nil
}

func (s *Server) BuildWorkplace(
	ctx context.Context,
	req *connect.Request[worldv1.BuildWorkplaceRequest],
) (*connect.Response[worldv1.BuildWorkplaceResponse], error) {
	// Construction is a delayed job owned by the BFF (BullMQ), and no workplace
	// service exists yet. Failing loudly beats returning a plausible id for a
	// building that will never appear.
	return nil, connect.NewError(connect.CodeUnimplemented,
		errors.New("no workplace services deployed yet"))
}
