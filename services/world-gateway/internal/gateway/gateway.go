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
	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
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

	// How the workplace services describe themselves, if any are wired up.
	//
	// Kept as a function rather than a client so the gateway stays runnable
	// with no workplaces at all: a nil describer means the join response simply
	// carries no payroll view, and the buildings still come through from
	// sim-core.
	describe func(context.Context) []*workplacev1.DescribeResponse

	// Runs a purchase end to end against the bank and sim-core. Nil means no
	// economy is wired up, the same posture `describe` takes.
	buy func(ctx context.Context, pip, workplace uint64, kind workplacev1.ResourceKind, tick uint64) (ok bool, reason string, price int64, err error)
}

func New(sim simv1connect.SimServiceClient, simSeed uint64, tickHz int32) *Server {
	return &Server{sim: sim, simSeed: simSeed, tickHz: tickHz}
}

// WithWorkplaces supplies the payroll view returned by JoinWorld.
func (s *Server) WithWorkplaces(describe func(context.Context) []*workplacev1.DescribeResponse) *Server {
	s.describe = describe
	return s
}

// WithBuy wires a purchase handler, normally economy.Fleet.Buy.
func (s *Server) WithBuy(buy func(ctx context.Context, pip, workplace uint64, kind workplacev1.ResourceKind, tick uint64) (bool, string, int64, error)) *Server {
	s.buy = buy
	return s
}

func (s *Server) JoinWorld(
	ctx context.Context,
	req *connect.Request[worldv1.JoinWorldRequest],
) (*connect.Response[worldv1.JoinWorldResponse], error) {
	snap, err := s.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	var payroll []*workplacev1.DescribeResponse
	if s.describe != nil {
		payroll = s.describe(ctx)
	}

	slog.Info("client joined",
		"client_id", req.Msg.GetClientId(),
		"tick", snap.Msg.GetTick(),
		"pips", len(snap.Msg.GetPips()),
		"buildings", len(snap.Msg.GetWorkplaces()))

	return connect.NewResponse(&worldv1.JoinWorldResponse{
		Tick:    snap.Msg.GetTick(),
		TickHz:  s.tickHz,
		SimSeed: s.simSeed,
		Pips:    snap.Msg.GetPips(),
		// Two views of the same buildings, and the difference is the point:
		// `Workplaces` is who is on the payroll, `Buildings` is who is in the
		// room. A pip hired a second ago appears in the first and not the
		// second, because it is still walking there.
		Workplaces: payroll,
		Buildings:  snap.Msg.GetWorkplaces(),
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

func (s *Server) Buy(
	ctx context.Context,
	req *connect.Request[worldv1.BuyRequest],
) (*connect.Response[worldv1.BuyResponse], error) {
	if s.buy == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("no economy wired up"))
	}

	// Buy is a player action, not part of the tick loop, so there is no tick
	// already in hand the way a cycle carries one — one Snapshot call to
	// learn the current tick is the cost of a purchase being a rare,
	// player-initiated RPC rather than something on the hot path.
	snap, err := s.sim.Snapshot(ctx, connect.NewRequest(&simv1.SnapshotRequest{}))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	ok, reason, price, err := s.buy(ctx, req.Msg.GetPipId(), req.Msg.GetWorkplaceId(), req.Msg.GetKind(), snap.Msg.GetTick())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	return connect.NewResponse(&worldv1.BuyResponse{
		Ok:     ok,
		Reason: reason,
		Price:  price,
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
