package tools

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

func TestSanitizedTicketContextFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/ticket_context.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Ticket   zendesk.Ticket        `json:"ticket"`
		Comments []zendesk.Comment     `json:"comments"`
		Fields   []zendesk.TicketField `json:"ticket_fields"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Ticket.ID != 12345 || len(fixture.Comments) != 2 || len(fixture.Fields) != 1 {
		t.Fatalf("fixture shape invalid: %+v", fixture)
	}
	decoded := decodeCustomFields(fixture.Ticket.CustomFields, fixture.Fields)
	if len(decoded) != 1 || decoded[0].Label != "Current Release" {
		t.Fatalf("decoded=%+v", decoded)
	}
}
