// Package httpapi exposes the crackwatch service over HTTP using Echo.
package httpapi

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"crackwatch/internal/app"
	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/spectrum"
	"crackwatch/internal/model"
)

// Server wires the application service to HTTP routes.
type Server struct {
	svc *app.Service
	e   *echo.Echo
}

// NewServer builds the router.
func NewServer(svc *app.Service) *Server {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	s := &Server{svc: svc, e: e}

	e.GET("/healthz", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	api := e.Group("/api/v1")

	api.POST("/materials", s.createMaterial)
	api.GET("/materials", s.listMaterials)
	api.GET("/materials/:id", s.getMaterial)
	api.POST("/materials/:id/versions", s.addMaterialVersion)

	api.POST("/locations", s.createLocation)
	api.GET("/locations", s.listLocations)
	api.GET("/locations/:id", s.getLocation)
	api.PATCH("/locations/:id", s.updateLocation)
	api.PUT("/locations/:id/spectrum", s.replaceSpectrum)
	api.GET("/locations/:id/spectrum", s.getSpectrum)

	api.POST("/locations/:id/records", s.submitRecord)
	api.POST("/locations/:id/records/:eventId/correction", s.correctRecord)
	api.POST("/locations/:id/records/:eventId/void", s.voidRecord)
	api.GET("/locations/:id/records", s.listRecords)

	api.GET("/locations/:id/plan", s.currentPlan)
	api.GET("/locations/:id/plan/at", s.planAt)
	api.GET("/locations/:id/plan/compare", s.comparePlan)
	api.GET("/locations/:id/plans", s.planHistory)

	return s
}

// Handler returns the Echo instance (used by the main package and tests).
func (s *Server) Handler() *echo.Echo { return s.e }

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

type blockDTO struct {
	StressAmp    float64 `json:"stress_amp"`
	StressMax    float64 `json:"stress_max"`
	CyclesPerDay float64 `json:"cycles_per_day"`
}

func blocksToDomain(d []blockDTO) []spectrum.Block {
	out := make([]spectrum.Block, len(d))
	for i, b := range d {
		out[i] = spectrum.Block{StressAmp: b.StressAmp, StressMax: b.StressMax,
			CyclesPerDay: b.CyclesPerDay}
	}
	return out
}

func blocksFromDomain(b []spectrum.Block) []blockDTO {
	out := make([]blockDTO, len(b))
	for i, x := range b {
		out[i] = blockDTO{x.StressAmp, x.StressMax, x.CyclesPerDay}
	}
	return out
}

type createMaterialReq struct {
	Grade       string  `json:"grade"`
	K1c         float64 `json:"k1c"`
	Sy          float64 `json:"sy"`
	M           float64 `json:"m"`
	C           float64 `json:"c"`
	EffectiveAt string  `json:"effective_at"`
}

func (r createMaterialReq) params() material.Params {
	return material.Params{Grade: r.Grade, K1c: r.K1c, Sy: r.Sy, M: r.M, C: r.C}
}

type createLocationReq struct {
	Name           string     `json:"name"`
	Geometry       string     `json:"geometry"`
	WidthMM        float64    `json:"width_mm"`
	MaterialID     string     `json:"material_id"`
	SafetyFactor   float64    `json:"safety_factor"`
	CommissionedAt string     `json:"commissioned_at"`
	Blocks         []blockDTO `json:"blocks"`
}

type updateLocationReq struct {
	Name         *string  `json:"name"`
	SafetyFactor *float64 `json:"safety_factor"`
}

type replaceSpectrumReq struct {
	Blocks      []blockDTO `json:"blocks"`
	EffectiveAt string     `json:"effective_at"`
}

type recordReq struct {
	InspectedAt   string  `json:"inspected_at"`
	Method        string  `json:"method"`
	Kind          string  `json:"kind"`
	LengthMM      float64 `json:"length_mm"`
	DetectLimitMM float64 `json:"detect_limit_mm"`
	Note          string  `json:"note"`
	Inspector     string  `json:"inspector"`
}

func (r recordReq) input() (app.RecordInput, error) {
	t, err := parseTime(r.InspectedAt)
	if err != nil {
		return app.RecordInput{}, err
	}
	return app.RecordInput{
		InspectedAt: t, Method: r.Method, Kind: model.ResultKind(r.Kind),
		LengthMM: r.LengthMM, DetectLimitMM: r.DetectLimitMM,
		Note: r.Note, Inspector: r.Inspector,
	}, nil
}

type voidReq struct {
	Note string `json:"note"`
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}
