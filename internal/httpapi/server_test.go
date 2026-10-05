package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"crackstation/internal/httpapi"
	"crackstation/internal/recordlog"
	"crackstation/internal/store"
)

type api struct {
	t    *testing.T
	srv  *httptest.Server
	repo *store.Memory
	now  *time.Time
}

func newAPI(t *testing.T) *api {
	repo := store.NewMemory()
	now := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	a := &api{t: t, repo: repo, now: &now}
	repo.Clock = func() time.Time { return *a.now }
	svc := recordlog.NewService(repo)
	svc.SetClock(func() time.Time { return *a.now })
	h := httpapi.New(repo, svc)
	ts := httptest.NewServer(h.Handler())
	t.Cleanup(ts.Close)
	a.srv = ts
	return a
}

func (a *api) advance(d time.Duration) { *a.now = a.now.Add(d) }

func (a *api) do(method, path string, body any) (int, map[string]any) {
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	_ = dec.Decode(&out)
	return resp.StatusCode, out
}

func createMaterialAndLocation(a *api) (matID, locID int64) {
	st, out := a.do("POST", "/api/v1/materials", map[string]any{
		"grade": "Q345", "paris_m": 3, "paris_c": 1e-11,
		"fracture_kic_mpa_sqrt_m": 80, "yield_strength_mpa": 345,
	})
	if st != 201 {
		a.t.Fatalf("material status %d %v", st, out)
	}
	matID = int64(out["material"].(map[string]any)["id"].(float64))

	st, out = a.do("POST", "/api/v1/locations", map[string]any{
		"name": "jib #1", "crane": "MQ-01", "geometry": "edge_crack",
		"width_m": 0.3, "material_id": matID,
		"commissioned_date": "2024-01-01", "safety_factor": 2,
	})
	if st != 201 {
		a.t.Fatalf("location status %d %v", st, out)
	}
	locID = int64(out["location"].(map[string]any)["id"].(float64))

	st, out = a.do("POST", "/api/v1/locations/"+itoa(locID)+"/spectrum", map[string]any{
		"blocks": []map[string]any{
			{"stress_amp_mpa": 70, "max_stress_mpa": 100, "cycles_per_day": 300},
		},
	})
	if st != 201 {
		a.t.Fatalf("spectrum status %d %v", st, out)
	}
	return matID, locID
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func TestFullWorkflow(t *testing.T) {
	a := newAPI(t)
	_, locID := createMaterialAndLocation(a)
	p := "/api/v1/locations/" + itoa(locID)

	// First found record -> uncalibrated (prior C).
	st, out := a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-02-01", "method": "MT", "found": true,
		"crack_length_m": 0.003, "inspector": "A",
	})
	if st != 201 {
		t.Fatalf("record1 %d %v", st, out)
	}
	if out["calibrated"].(bool) {
		t.Fatal("first record must not calibrate C")
	}

	// Second found record submitted months later -> calibrated.
	a.advance(150 * 24 * time.Hour)
	st, out = a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-06-01", "method": "MT", "found": true,
		"crack_length_m": 0.005, "inspector": "A",
	})
	if st != 201 || !out["calibrated"].(bool) {
		t.Fatalf("record2 %d %v", st, out)
	}

	// Current plan & life.
	if st, out = a.do("GET", p+"/plan", nil); st != 200 {
		t.Fatalf("plan %d %v", st, out)
	}
	if st, out = a.do("GET", p+"/life", nil); st != 200 {
		t.Fatalf("life %d %v", st, out)
	}
	if out["remaining_life"].(map[string]any)["days_to_critical"].(float64) <= 0 {
		t.Fatal("non-positive life")
	}

	// As-of and compare.
	if st, out = a.do("GET", p+"/plan/as-of?date=2024-03-01", nil); st != 200 {
		t.Fatalf("as-of %d %v", st, out)
	}
	if out["calibrated"].(bool) {
		t.Fatal("as-of March should be uncalibrated")
	}
	if st, out = a.do("GET", p+"/plan/compare?date=2024-03-01", nil); st != 200 {
		t.Fatalf("compare %d %v", st, out)
	}
}

func TestHTTPValidation(t *testing.T) {
	a := newAPI(t)
	_, locID := createMaterialAndLocation(a)
	p := "/api/v1/locations/" + itoa(locID)

	st, out := a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-02-01", "found": true, "crack_length_m": 0.4,
	})
	if st != 422 || out["field"] != "crack_length_m" {
		t.Fatalf("got %d %v", st, out)
	}
	st, out = a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-02-15", "found": false,
	})
	if st != 422 || !strings.Contains(out["field"].(string), "detection_limit") {
		t.Fatalf("nf missing limit got %d %v", st, out)
	}
	st, out = a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2023-12-01", "found": false, "detection_limit_m": 0.002,
	})
	if st != 422 || out["field"] != "inspect_date" {
		t.Fatalf("date rule got %d %v", st, out)
	}
	st, out = a.do("POST", "/api/v1/locations", map[string]any{
		"name": "x", "geometry": "bogus", "width_m": 0.3,
		"material_id": 1, "commissioned_date": "2024-01-01",
	})
	if st != 400 {
		t.Fatalf("geometry got %d %v", st, out)
	}
	st, out = a.do("POST", "/api/v1/locations/"+itoa(locID)+"/spectrum", map[string]any{
		"blocks": []map[string]any{{"stress_amp_mpa": 300, "max_stress_mpa": 100, "cycles_per_day": 1}},
	})
	if st != 422 {
		t.Fatalf("spectrum got %d %v", st, out)
	}
}

func TestCorrectionAndHistory(t *testing.T) {
	a := newAPI(t)
	_, locID := createMaterialAndLocation(a)
	p := "/api/v1/locations/" + itoa(locID)

	_, out := a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-02-01", "found": true, "crack_length_m": 0.003,
	})
	rid := int64(out["record"].(map[string]any)["id"].(float64))

	st, out := a.do("POST", p+"/records/"+itoa(rid)+"/correct", map[string]any{
		"inspect_date": "2024-02-01", "found": true, "crack_length_m": 0.0035,
	})
	if st != 201 {
		t.Fatalf("correct %d %v", st, out)
	}
	st, out = a.do("GET", p+"/records/history", nil)
	if st != 200 {
		t.Fatalf("history %d", st)
	}
	if len(out["history"].([]any)) != 2 {
		t.Fatalf("history rows=%v", out["history"])
	}
	st, out = a.do("GET", p+"/records", nil)
	if len(out["records"].([]any)) != 1 {
		t.Fatalf("active rows=%v", out["records"])
	}
}

func TestMaterialImpactEndpoint(t *testing.T) {
	a := newAPI(t)
	matID, locID := createMaterialAndLocation(a)
	p := "/api/v1/locations/" + itoa(locID)
	a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-02-01", "found": true, "crack_length_m": 0.003})
	a.advance(120 * 24 * time.Hour)
	a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-05-01", "found": true, "crack_length_m": 0.004})
	st, out := a.do("POST", "/api/v1/materials/"+itoa(matID)+"/versions", map[string]any{
		"paris_m": 3, "paris_c": 1e-11,
		"fracture_kic_mpa_sqrt_m": 40, "yield_strength_mpa": 172,
	})
	if st != 201 {
		t.Fatalf("version %d %v", st, out)
	}
	imp := out["earlier_inspections"].([]any)
	if len(imp) == 0 {
		t.Fatal("expected earlier inspection impact")
	}
}

// Two simultaneous POSTs for the same location both persist.
func TestHTTPSubmitConcurrency(t *testing.T) {
	a := newAPI(t)
	_, locID := createMaterialAndLocation(a)
	p := "/api/v1/locations/" + itoa(locID)
	a.do("POST", p+"/records", map[string]any{
		"inspect_date": "2024-02-01", "found": true, "crack_length_m": 0.003})

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i, body := range []map[string]any{
		{"inspect_date": "2024-04-01", "found": true, "crack_length_m": 0.004, "inspector": "A"},
		{"inspect_date": "2024-07-01", "found": true, "crack_length_m": 0.0055, "inspector": "B"},
	} {
		wg.Add(1)
		go func(body map[string]any) {
			defer wg.Done()
			st, _ := a.do("POST", p+"/records", body)
			statuses <- st
		}(body)
		_ = i
	}
	wg.Wait()
	close(statuses)
	for st := range statuses {
		if st != 201 {
			t.Fatalf("concurrent submit status %d", st)
		}
	}
	_, out := a.do("GET", p+"/records", nil)
	if len(out["records"].([]any)) != 3 {
		t.Fatalf("records=%v", out["records"])
	}
}
