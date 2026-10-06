package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	app "crackwatch/internal/app"
	"crackwatch/internal/model"
	"crackwatch/internal/store"
)

func testServer(t *testing.T) (*httptest.Server, *app.Service, func()) {
	t.Helper()
	svc := app.New(store.NewMemory())
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var clock time.Time = now
	svc.SetClock(func() time.Time { return clock })
	advance := func() { clock = clock.Add(time.Minute) }
	svc.SetTicker(advance)
	s := NewServer(svc)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, svc, advance
}

func doJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func mustCreateStack(t *testing.T, srv *httptest.Server) (matID, locID string) {
	t.Helper()
	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/materials", map[string]any{
		"grade": "Q345", "k1c": 330, "sy": 345, "m": 3, "c": 1e-11,
	})
	if status != http.StatusCreated {
		t.Fatalf("create material status %d body %v", status, body)
	}
	matID = body["material"].(map[string]any)["id"].(string)

	status, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/locations", map[string]any{
		"name": "crane-7 boom B", "geometry": "edge_crack", "width_mm": 300,
		"material_id": matID, "safety_factor": 2,
		"commissioned_at": "2024-01-01T00:00:00Z",
		"blocks": []map[string]any{
			{"stress_amp": 100, "stress_max": 150, "cycles_per_day": 500},
		},
	})
	if status != http.StatusCreated {
		t.Fatalf("create location status %d body %v", status, body)
	}
	locID = body["location"].(map[string]any)["id"].(string)
	return matID, locID
}

func TestAPIEndToEnd(t *testing.T) {
	srv, _, advance := testServer(t)
	matID, locID := mustCreateStack(t, srv)
	if matID == "" || locID == "" {
		t.Fatal("missing ids")
	}

	// List / get.
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/locations", nil); status != 200 {
		t.Fatalf("list locations %d", status)
	}
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/locations/"+locID, nil); status != 200 {
		t.Fatalf("get location %d", status)
	}

	// Submit two measured findings.
	submit := func(date string, mm float64) map[string]any {
		status, body := doJSON(t, http.MethodPost,
			fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
			map[string]any{
				"inspected_at": date, "method": "MT", "kind": "found", "length_mm": mm,
				"inspector": "zhang",
			})
		if status != http.StatusCreated {
			t.Fatalf("submit %s status %d body %v", date, status, body)
		}
		return body
	}
	b1 := submit("2026-01-01T00:00:00Z", 5)
	firstEvent := b1["event"].(map[string]any)["id"].(string)
	advance()
	submit("2026-03-02T00:00:00Z", 12) // 60 days later

	// Current plan is calibrated and scheduled.
	status, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/locations/"+locID+"/plan", nil)
	if status != 200 {
		t.Fatalf("plan %d", status)
	}
	plan := body["plan"].(map[string]any)
	if plan["c_was_calibrated"] != true {
		t.Fatalf("plan not calibrated: %v", plan)
	}
	if plan["next_inspection_at"] == nil || plan["next_inspection_at"] == "" {
		t.Fatal("no next inspection date")
	}
	if plan["critical_length_mm"].(float64) <= 12 {
		t.Fatal("critical length should exceed current 12 mm")
	}

	// Historical as-of: at the first record only one finding was known and
	// C was not calibrated.
	at := "2026-01-01T12:00:30Z"
	status, body = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/locations/"+locID+"/plan/at?at="+at, nil)
	if status != 200 {
		t.Fatalf("plan/at %d", status)
	}
	histPlan := body["plan"].(map[string]any)
	if histPlan["c_was_calibrated"] != false {
		t.Fatalf("historical plan must use catalog C: %v", histPlan)
	}
	if body["findings_used"].(float64) != 1 {
		t.Fatalf("historical findings %v", body["findings_used"])
	}

	// Comparison endpoint.
	status, body = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/locations/"+locID+"/plan/compare?at="+at, nil)
	if status != 200 {
		t.Fatalf("compare %d", status)
	}
	if body["historical"] == nil || body["current"] == nil {
		t.Fatal("compare must include both plans")
	}

	// Correction: fix the first record to 4 mm; old event retained.
	status, body = doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/locations/%s/records/%s/correction", srv.URL, locID, firstEvent),
		map[string]any{
			"inspected_at": "2026-01-01T00:00:00Z", "method": "MT",
			"kind": "found", "length_mm": 4,
		})
	if status != http.StatusCreated {
		t.Fatalf("correction %d body %v", status, body)
	}
	status, body = doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID), nil)
	if len(body["events"].([]any)) != 3 {
		t.Fatalf("events = %v, want 3 (old retained)", body["events"])
	}

	// Stored plan history.
	if status, body = doJSON(t, http.MethodGet,
		srv.URL+"/api/v1/locations/"+locID+"/plans", nil); status != 200 ||
		len(body["snapshots"].([]any)) < 2 {
		t.Fatalf("plan history status %d body %v", status, body)
	}

	// Not-detected record.
	status, _ = doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
		map[string]any{
			"inspected_at": "2026-04-01T00:00:00Z", "method": "UT",
			"kind": "not_detected", "detect_limit_mm": 2,
		})
	if status != http.StatusCreated {
		t.Fatalf("NDF %d", status)
	}
}

func TestAPIValidationErrors(t *testing.T) {
	srv, _, _ := testServer(t)
	_, locID := mustCreateStack(t, srv)

	// Crack length not positive.
	status, body := doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
		map[string]any{
			"inspected_at": "2026-01-01T00:00:00Z", "method": "MT",
			"kind": "found", "length_mm": 0,
		})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	fields := body["fields"].([]any)
	if !fieldNamed(fields, "length_mm") {
		t.Fatalf("fields %v do not name length_mm", fields)
	}

	// Not-detected without detection limit.
	status, body = doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
		map[string]any{
			"inspected_at": "2026-01-01T00:00:00Z", "method": "UT",
			"kind": "not_detected",
		})
	if status != 422 || !fieldNamed(body["fields"].([]any), "detect_limit_mm") {
		t.Fatalf("NDF validation: status %d body %v", status, body)
	}

	// Inspection before commissioning.
	status, body = doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
		map[string]any{
			"inspected_at": "2023-01-01T00:00:00Z", "method": "MT",
			"kind": "found", "length_mm": 5,
		})
	if status != 422 || !fieldNamed(body["fields"].([]any), "inspected_at") {
		t.Fatalf("date validation: status %d body %v", status, body)
	}

	// Material K1c must be positive.
	status, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/materials", map[string]any{
		"grade": "bad", "k1c": 0, "sy": 300, "m": 3, "c": 1e-11,
	})
	if status != 422 || !fieldNamed(body["fields"].([]any), "k1c") {
		t.Fatalf("material validation: status %d body %v", status, body)
	}
}

func TestAPIMaterialImpact(t *testing.T) {
	srv, _, advance := testServer(t)
	matID, locID := mustCreateStack(t, srv)
	doJSON(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
		map[string]any{
			"inspected_at": "2026-01-01T00:00:00Z", "method": "MT",
			"kind": "found", "length_mm": 5,
		})
	advance()
	status, body := doJSON(t, http.MethodPost,
		srv.URL+"/api/v1/materials/"+matID+"/versions",
		map[string]any{"grade": "Q345", "k1c": 330, "sy": 345, "m": 3, "c": 1e-10})
	if status != 201 {
		t.Fatalf("add version %d body %v", status, body)
	}
	impacts := body["earlier_inspections"].([]any)
	if len(impacts) != 1 {
		t.Fatalf("impacts = %v, want 1", impacts)
	}
	entry := impacts[0].(map[string]any)
	if entry["location_id"] != locID || entry["earlier_days"].(float64) <= 0 {
		t.Fatalf("impact entry %v", entry)
	}
}

func TestAPIConcurrentSubmissions(t *testing.T) {
	srv, _, _ := testServer(t)
	_, locID := mustCreateStack(t, srv)

	var wg sync.WaitGroup
	errs := make([]int, 2)
	for i, date := range []string{"2026-01-10T00:00:00Z", "2026-01-20T00:00:00Z"} {
		wg.Add(1)
		go func(i int, date string) {
			defer wg.Done()
			body := fmt.Sprintf(
				`{"inspected_at":%q,"method":"MT","kind":"found","length_mm":%d}`,
				date, 6+i)
			req, _ := http.NewRequest(http.MethodPost,
				fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID),
				bytes.NewReader([]byte(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			errs[i] = resp.StatusCode
		}(i, date)
	}
	wg.Wait()
	for i, code := range errs {
		if code != 201 {
			t.Fatalf("concurrent submit %d status %d", i, code)
		}
	}
	status, body := doJSON(t, http.MethodGet,
		fmt.Sprintf("%s/api/v1/locations/%s/records", srv.URL, locID), nil)
	if status != 200 || len(body["events"].([]any)) != 2 {
		t.Fatalf("events = %v, want both retained", body["events"])
	}
}

func TestAPIHealth(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health %d", resp.StatusCode)
	}
}

func fieldNamed(fields []any, name string) bool {
	for _, f := range fields {
		m := f.(map[string]any)
		if m["field"] == name {
			return true
		}
	}
	return false
}

var _ = model.ResultFound
var _ = context.Background
