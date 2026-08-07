package economy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	workplacev1 "github.com/teceer/pipsim/gen/go/pips/workplace/v1"
	"github.com/teceer/pipsim/gen/go/pips/workplace/v1/workplacev1connect"
)

// stubWorkplace answers only what Discover asks. Embedding the generated
// handler means every RPC it does not implement returns Unimplemented, which is
// exactly the behaviour under test for the older-workplace path.
type stubWorkplace struct {
	workplacev1connect.UnimplementedWorkplaceServiceHandler
	buildings []*workplacev1.DescribeResponse
	listErr   error
}

func (s *stubWorkplace) List(
	_ context.Context,
	_ *connect.Request[workplacev1.ListRequest],
) (*connect.Response[workplacev1.ListResponse], error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return connect.NewResponse(&workplacev1.ListResponse{Workplaces: s.buildings}), nil
}

func serve(t *testing.T, h workplacev1connect.WorkplaceServiceHandler) workplacev1connect.WorkplaceServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(workplacev1connect.NewWorkplaceServiceHandler(h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return workplacev1connect.NewWorkplaceServiceClient(srv.Client(), srv.URL)
}

func TestDiscoverBuildsADriverPerBuilding(t *testing.T) {
	client := serve(t, &stubWorkplace{buildings: []*workplacev1.DescribeResponse{
		{WorkplaceId: 1, Kind: "farm"},
		{WorkplaceId: 3, Kind: "farm"},
	}})

	drivers, err := Discover(context.Background(), nil, client, "farm:8090", nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(drivers) != 2 {
		t.Fatalf("want a driver per building, got %d", len(drivers))
	}
	// Known up front, so the fleet never has to skip them waiting for an id.
	if drivers[0].ID() != 1 || drivers[1].ID() != 3 {
		t.Errorf("want ids 1,3, got %d,%d", drivers[0].ID(), drivers[1].ID())
	}
	if drivers[0].Kind() != "farm" {
		t.Errorf("want kind farm, got %q", drivers[0].Kind())
	}
}

// A workplace written before List — the Elixir tavern today — must keep working
// untouched. It answers Unimplemented, and one driver is built that learns who
// it is on its first Register, exactly as it always did.
func TestDiscoverFallsBackForAWorkplaceWithoutList(t *testing.T) {
	client := serve(t, &stubWorkplace{
		listErr: connect.NewError(connect.CodeUnimplemented, nil),
	})

	drivers, err := Discover(context.Background(), nil, client, "tavern:8090", nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(drivers) != 1 {
		t.Fatalf("want one driver, got %d", len(drivers))
	}
	if drivers[0].ID() != 0 {
		t.Errorf("want an unknown id until Register, got %d", drivers[0].ID())
	}
}

// Distinguished from Unimplemented on purpose: a workplace that is merely down
// must not be mistaken for one that predates List and silently driven as a
// single building whose id nobody knows.
func TestDiscoverReportsARealFailure(t *testing.T) {
	client := serve(t, &stubWorkplace{
		listErr: connect.NewError(connect.CodeUnavailable, nil),
	})

	if _, err := Discover(context.Background(), nil, client, "farm:8090", nil, nil); err == nil {
		t.Fatal("want an error when the workplace is unreachable")
	}
}

func TestDiscoverRejectsAHostWithNoBuildings(t *testing.T) {
	client := serve(t, &stubWorkplace{})

	if _, err := Discover(context.Background(), nil, client, "farm:8090", nil, nil); err == nil {
		t.Fatal("want an error: a workplace service hosts at least one building")
	}
}
