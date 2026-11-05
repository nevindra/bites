package bites_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nevindra/bites"
)

type (
	wireAddress struct {
		City    string
		ZipCode bites.Opt[string]
	}
	wireLine struct {
		SKUCode  string
		Quantity int
	}
	wireNode struct {
		NodeID   string
		Children []wireNode
	}
	wireOrder struct {
		Path struct {
			OrgUnitID string
		}
		Query struct {
			PageSize bites.Opt[int]
		}
		Header struct {
			RequestID bites.Opt[string]
		}
		Body struct {
			ShipTo wireAddress
			Lines  []wireLine
			Tree   wireNode
		}
	}
	wireOrderOut struct{ OrderID string }
)

func wireApp(t *testing.T, pattern string, opts ...bites.Option) *bites.App {
	t.Helper()
	app := bites.New(opts...)
	app.Post(pattern, func(context.Context, wireOrder) (bites.Created[wireOrderOut], error) {
		return bites.Created[wireOrderOut]{}, nil
	})
	return app
}

func wireTable(t *testing.T, app *bites.App) string {
	t.Helper()
	var b bytes.Buffer
	if err := app.WriteWireTable(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestTheWireTableShowsTheNamesACustomPolicyProducedForEverySection(t *testing.T) {
	got := wireTable(t, wireApp(t, "/units/{orgUnitId}/orders", bites.WithNaming(bites.CamelCase)))
	for _, want := range []string{
		"POST /units/{orgUnitId}/orders",
		"path          wireOrder.Path.OrgUnitID",
		"orgUnitId",
		"pageSize",
		"Request-Id",
		"body          wireOrder.Body.ShipTo.ZipCode",
		"shipTo.zipCode",
		"lines[].skuCode",
		"wireOrder.Body.Tree.Children[]",
		"(recursive: wireNode)",
		"wireOrder.Body.Lines[].SKUCode",
		"lines[].skuCode",
		"response 201",
		"orderId",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
}

func TestTheWireTableFollowsThePolicyTheAppWasBuiltWith(t *testing.T) {
	got := wireTable(t, wireApp(t, "/units/{org_unit_id}/orders"))
	if !strings.Contains(got, "org_unit_id") || !strings.Contains(got, "ship_to.zip_code") || strings.Contains(got, "shipTo") {
		t.Errorf("the default policy is not snake_case in\n%s", got)
	}
}

func TestARecursiveTypeIsListedOnceInTheWireTableAndThenNamed(t *testing.T) {
	got := wireTable(t, wireApp(t, "/units/{org_unit_id}/orders"))
	if !strings.Contains(got, "(recursive: wireNode)") {
		t.Errorf("a recursive type is not named in\n%s", got)
	}
}

func TestTheWireTableListsARouteTheDocumentLeavesOut(t *testing.T) {
	app := bites.New()
	app.Get("/hidden/{id}", func(context.Context, struct{ Path struct{ ID string } }) (wireOrderOut, error) {
		return wireOrderOut{}, nil
	}, bites.Undocumented())
	if got := wireTable(t, app); !strings.Contains(got, "GET /hidden/{id}") || !strings.Contains(got, "order_id") {
		t.Errorf("an undocumented route is missing in\n%s", got)
	}
}

func TestTheWireTableSaysAnOptBodyMayBeAbsentAndListsItsFields(t *testing.T) {
	app := bites.New()
	app.Post("/r/{id}", func(context.Context, struct {
		Path struct{ ID int64 }
		Body bites.Opt[struct{ Name string }]
	}) (bites.NoContent, error) {
		return bites.NoContent{}, nil
	})
	got := wireTable(t, app)
	if !strings.Contains(got, "(may be absent)") || !strings.Contains(got, ".Body.Name") {
		t.Fatalf("%s", got)
	}
}
