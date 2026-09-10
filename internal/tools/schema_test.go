package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPageQueryValidation(t *testing.T) {
	if _, err := pageQuery(pageTicketArgs{TicketID: 1, AfterCursor: "a", BeforeCursor: "b"}); err == nil {
		t.Fatal("expected mutually exclusive cursor error")
	}
	if _, err := pageQuery(pageTicketArgs{TicketID: 1, PageSize: 101}); err == nil {
		t.Fatal("expected page size error")
	}
}

func TestDecodeArgsRejectsUnknownFields(t *testing.T) {
	var target struct {
		ID int64 `json:"id"`
	}
	if err := decodeArgs(json.RawMessage(`{"id":1,"arbitrary_path":"/admin"}`), &target); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error=%v", err)
	}
}
