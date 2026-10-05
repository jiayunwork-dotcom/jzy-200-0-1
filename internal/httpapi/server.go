// Package httpapi exposes the crack-station backend over HTTP with the Echo
// framework. It is a thin translation layer: validation and domain logic
// live in the internal packages.
package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"crackstation/internal/geometry"
	"crackstation/internal/recordlog"
	"crackstation/internal/spectrum"
	"crackstation/internal/store"
)

// Server wires the repository and domain service to HTTP routes.
type Server struct {
	echo *echo.Echo
	repo store.Repository
	svc  *recordlog.Service
}

// New builds the Echo server and registers routes.
func New(repo store.Repository, svc *recordlog.Service) *Server {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	s := &Server{echo: e, repo: repo, svc: svc}
	s.routes()
	return s
}

// Handler returns the Echo instance (start/shutdown).
func (s *Server) Handler() *echo.Echo { return s.echo }

func (s *Server) routes() {
	g := s.echo.Group("/api/v1")

	g.GET("/health", s.health)

	// Materials
	g.POST("/materials", s.createMaterial)
	g.GET("/materials", s.listMaterials)
	g.GET("/materials/:id", s.getMaterial)
	g.POST("/materials/:id/versions", s.addMaterialVersion)
	g.GET("/materials/:id/versions", s.listMaterialVersions)

	// Locations
	g.POST("/locations", s.createLocation)
	g.GET("/locations", s.listLocations)
	g.GET("/locations/:id", s.getLocation)
	g.POST("/locations/:id/spectrum", s.addSpectrum)
	g.GET("/locations/:id/spectrum/latest", s.latestSpectrum)

	// Records
	g.POST("/locations/:id/records", s.submitRecord)
	g.POST("/locations/:id/records/:rid/correct", s.correctRecord)
	g.GET("/locations/:id/records", s.listRecords)
	g.GET("/locations/:id/records/history", s.recordHistory)

	// Plans
	g.GET("/locations/:id/plan", s.currentPlan)
	g.GET("/locations/:id/life", s.remainingLife)
	g.GET("/locations/:id/plan/as-of", s.planAsOf)
	g.GET("/locations/:id/plan/compare", s.planCompare)
}

func (s *Server) health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok"})
}

// ---------- materials ----------

type materialReq struct {
	Grade         string  `json:"grade"`
	ParisM        float64 `json:"paris_m"`
	ParisC        float64 `json:"paris_c"`
	FractureKIC   float64 `json:"fracture_kic_mpa_sqrt_m"`
	YieldStrength float64 `json:"yield_strength_mpa"`
	CreatedBy     string  `json:"created_by"`
	Note          string  `json:"note"`
}

func (s *Server) createMaterial(c echo.Context) error {
	var req materialReq
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "body", err.Error())
	}
	v := store.MaterialVersion{
		ParisM: req.ParisM, ParisC: req.ParisC,
		FractureKIC: req.FractureKIC, YieldStrength: req.YieldStrength,
		CreatedBy: req.CreatedBy, Note: req.Note,
	}
	if err := recordlog.ValidateMaterialVersion(v); err != nil {
		return fieldErr(c, err)
	}
	mat, ver, err := s.repo.CreateMaterial(c.Request().Context(), store.Material{Grade: req.Grade}, v)
	if err != nil {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return c.JSON(http.StatusCreated, map[string]any{"material": mat, "version": ver})
}

func (s *Server) listMaterials(c echo.Context) error {
	ms, err := s.repo.ListMaterials(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"materials": ms})
}

func (s *Server) getMaterial(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	mat, err := s.repo.GetMaterial(c.Request().Context(), id)
	if err != nil {
		return notFound(c, err)
	}
	vs, err := s.repo.ListMaterialVersions(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"material": mat, "versions": vs})
}

type addVersionReq struct {
	ParisM        float64 `json:"paris_m"`
	ParisC        float64 `json:"paris_c"`
	FractureKIC   float64 `json:"fracture_kic_mpa_sqrt_m"`
	YieldStrength float64 `json:"yield_strength_mpa"`
	CreatedBy     string  `json:"created_by"`
	Note          string  `json:"note"`
}

func (s *Server) addMaterialVersion(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	var req addVersionReq
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "body", err.Error())
	}
	v := store.MaterialVersion{
		MaterialID: id, ParisM: req.ParisM, ParisC: req.ParisC,
		FractureKIC: req.FractureKIC, YieldStrength: req.YieldStrength,
		CreatedBy: req.CreatedBy, Note: req.Note,
	}
	nv, impacts, err := s.svc.AddMaterialVersion(c.Request().Context(), v)
	if err != nil {
		return fieldErr(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"version": nv, "earlier_inspections": impacts})
}

func (s *Server) listMaterialVersions(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	vs, err := s.repo.ListMaterialVersions(c.Request().Context(), id)
	if err != nil {
		return notFound(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"versions": vs})
}

// ---------- locations ----------

type locationReq struct {
	Name             string  `json:"name"`
	Crane            string  `json:"crane"`
	Geometry         string  `json:"geometry"`
	WidthM           float64 `json:"width_m"`
	MaterialID       int64   `json:"material_id"`
	CommissionedDate string  `json:"commissioned_date"` // YYYY-MM-DD
	SafetyFactor     float64 `json:"safety_factor"`
}

func (s *Server) createLocation(c echo.Context) error {
	var req locationReq
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "body", err.Error())
	}
	gt, err := geometry.ParseType(req.Geometry)
	if err != nil {
		return badRequest(c, "geometry", err.Error())
	}
	comm, err := parseDate(req.CommissionedDate)
	if err != nil {
		return badRequest(c, "commissioned_date", "expected YYYY-MM-DD")
	}
	if err := recordlog.ValidateLocation(gt, req.WidthM); err != nil {
		return fieldErr(c, err)
	}
	if _, err := s.repo.GetMaterial(c.Request().Context(), req.MaterialID); err != nil {
		return badRequest(c, "material_id", "unknown material")
	}
	sf := req.SafetyFactor
	if sf <= 0 {
		sf = 2
	}
	loc, err := s.repo.CreateLocation(c.Request().Context(), store.Location{
		Name: req.Name, Crane: req.Crane, Geometry: gt, WidthM: req.WidthM,
		MaterialID: req.MaterialID, CommissionedDate: comm, SafetyFactor: sf,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{"location": loc})
}

func (s *Server) listLocations(c echo.Context) error {
	ls, err := s.repo.ListLocations(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"locations": ls})
}

func (s *Server) getLocation(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	loc, err := s.repo.GetLocation(c.Request().Context(), id)
	if err != nil {
		return notFound(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"location": loc})
}

type spectrumReq struct {
	Blocks    []spectrum.Block `json:"blocks"`
	CreatedBy string           `json:"created_by"`
}

func (s *Server) addSpectrum(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	if _, err := s.repo.GetLocation(c.Request().Context(), id); err != nil {
		return notFound(c, err)
	}
	var req spectrumReq
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "body", err.Error())
	}
	if len(req.Blocks) == 0 {
		return badRequest(c, "blocks", "at least one load block required")
	}
	if err := recordlog.ValidateBlocks(req.Blocks); err != nil {
		return fieldErr(c, err)
	}
	spec, err := s.repo.AddSpectrumRevision(c.Request().Context(), store.SpectrumRevision{
		LocationID: id, Blocks: req.Blocks, CreatedBy: req.CreatedBy,
	})
	if err != nil {
		return err
	}
	// Recompute plan against the new spectrum (no-op if there are no records).
	res, _ := s.svc.RecomputeSpectrumChange(c.Request().Context(), id)
	return c.JSON(http.StatusCreated, map[string]any{"spectrum": spec, "plan": resultOrNil(res)})
}

func (s *Server) latestSpectrum(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	spec, err := s.repo.LatestSpectrum(c.Request().Context(), id)
	if err != nil {
		return notFound(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"spectrum": spec})
}

// ---------- records ----------

type recordReq struct {
	InspectDate     string   `json:"inspect_date"`
	Method          string   `json:"method"`
	Found           bool     `json:"found"`
	CrackLengthM    *float64 `json:"crack_length_m"`
	DetectionLimitM *float64 `json:"detection_limit_m"`
	Inspector       string   `json:"inspector"`
}

func (s *Server) submitRecord(c echo.Context) error {
	return s.appendRecord(c, nil)
}

func (s *Server) correctRecord(c echo.Context) error {
	rid, err := idParam(c, "rid")
	if err != nil {
		return err
	}
	return s.appendRecord(c, &rid)
}

func (s *Server) appendRecord(c echo.Context, supersedes *int64) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	var req recordReq
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "body", err.Error())
	}
	d, err := parseDate(req.InspectDate)
	if err != nil {
		return badRequest(c, "inspect_date", "expected YYYY-MM-DD")
	}
	in := store.NewRecordInput{
		LocationID: id, InspectDate: d, Method: req.Method, Found: req.Found,
		CrackLengthM: req.CrackLengthM, DetectionLimitM: req.DetectionLimitM,
		Inspector: req.Inspector, SupersedesID: supersedes,
	}
	var rec store.Record
	var res recordlog.ReplayResult
	if supersedes == nil {
		rec, res, err = s.svc.Submit(c.Request().Context(), in)
	} else {
		rec, res, err = s.svc.Correct(c.Request().Context(), in)
	}
	if err != nil {
		return fieldErr(c, err)
	}
	body := resultJSON(res)
	body["record"] = rec
	return c.JSON(http.StatusCreated, body)
}

func (s *Server) listRecords(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	rs, err := s.repo.ListActiveRecords(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"records": rs})
}

func (s *Server) recordHistory(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	rs, err := s.repo.ListRecordHistory(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"history": rs})
}

// ---------- plans ----------

func (s *Server) currentPlan(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	res, err := s.svc.Current(c.Request().Context(), id)
	if err != nil {
		return notFound(c, err)
	}
	return c.JSON(http.StatusOK, resultJSON(res))
}

func (s *Server) remainingLife(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	rl, err := s.svc.RemainingLifeOf(c.Request().Context(), id)
	if err != nil {
		return notFound(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"remaining_life": rl})
}

func (s *Server) planAsOf(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	t, err := asOfTime(c)
	if err != nil {
		return err
	}
	res, err := s.svc.AsOf(c.Request().Context(), id, t)
	if err != nil {
		return notFound(c, err)
	}
	out := resultJSON(res)
	out["as_of"] = t
	return c.JSON(http.StatusOK, out)
}

func (s *Server) planCompare(c echo.Context) error {
	id, err := idParam(c, "id")
	if err != nil {
		return err
	}
	t, err := asOfTime(c)
	if err != nil {
		return err
	}
	cmp, err := s.svc.CompareAt(c.Request().Context(), id, t)
	if err != nil {
		return notFound(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"comparison": cmp})
}

// ---------- helpers ----------

func resultJSON(res recordlog.ReplayResult) map[string]any {
	return map[string]any{
		"plan":                res.Plan,
		"fitted_c":            res.FittedC,
		"calibrated":          res.CoefficientCalibrated,
		"record_version_id":   res.RecordVersionID,
		"record_seq":          res.RecordVersionSeq,
		"material_version_id": res.MaterialVersionID,
		"spectrum_id":         res.SpectrumID,
		"found_records":       res.FoundCount,
		"not_found_records":   res.NFCount,
	}
}

func resultOrNil(res recordlog.ReplayResult) any {
	if res.RecordVersionID == 0 && res.FoundCount == 0 && res.NFCount == 0 {
		return nil
	}
	return resultJSON(res)
}

func idParam(c echo.Context, name string) (int64, error) {
	v, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || v <= 0 {
		return 0, badRequest(c, name, "must be a positive integer")
	}
	return v, nil
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

func asOfTime(c echo.Context) (time.Time, error) {
	v := c.QueryParam("date")
	if v == "" {
		return time.Time{}, badRequest(c, "date", "query parameter required (YYYY-MM-DD or RFC3339)")
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		// End of that day so records created on the date are included.
		return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, badRequest(c, "date", "expected YYYY-MM-DD or RFC3339")
}

func badRequest(c echo.Context, field, reason string) error {
	return c.JSON(http.StatusBadRequest, map[string]any{
		"error": "validation_failed", "field": field, "reason": reason,
	})
}

func fieldErr(c echo.Context, err error) error {
	if fe, ok := err.(recordlog.FieldError); ok {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error": "validation_failed", "field": fe.Field, "reason": fe.Reason,
		})
	}
	if _, ok := err.(store.ErrNotFound); ok {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "not_found", "reason": err.Error()})
	}
	return err
}

func notFound(c echo.Context, err error) error {
	if _, ok := err.(store.ErrNotFound); ok {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "not_found", "reason": err.Error()})
	}
	return err
}

// Compile-time guard.
var _ context.Context
