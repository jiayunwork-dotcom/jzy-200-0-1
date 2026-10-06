package httpapi

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"crackwatch/internal/app"
	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/model"
	"crackwatch/internal/store"
)

type errorBody struct {
	Error  string           `json:"error"`
	Fields []app.FieldError `json:"fields,omitempty"`
}

func fail(c echo.Context, status int, err error) error {
	body := errorBody{Error: err.Error()}
	var fe app.FieldErrors
	if errors.As(err, &fe) {
		status = http.StatusUnprocessableEntity
		body.Fields = fe
	}
	var nf store.ErrNotFound
	if errors.As(err, &nf) {
		status = http.StatusNotFound
	}
	return c.JSON(status, body)
}

// ---------------------------------------------------------------------------
// Materials
// ---------------------------------------------------------------------------

func (s *Server) createMaterial(c echo.Context) error {
	var req createMaterialReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	eff, err := parseTime(req.EffectiveAt)
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	m, v, err := s.svc.CreateMaterial(c.Request().Context(), app.CreateMaterialInput{
		Grade: req.Grade, Params: req.params(), EffectiveAt: eff,
	})
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"material": m, "version": v})
}

func (s *Server) listMaterials(c echo.Context) error {
	ms, err := s.svc.ListMaterials(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"materials": ms})
}

func (s *Server) getMaterial(c echo.Context) error {
	m, err := s.svc.GetMaterial(c.Request().Context(), c.Param("id"))
	if err != nil {
		return fail(c, http.StatusNotFound, err)
	}
	return c.JSON(http.StatusOK, m)
}

func (s *Server) addMaterialVersion(c echo.Context) error {
	var req createMaterialReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	eff, err := parseTime(req.EffectiveAt)
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	v, impact, err := s.svc.AddMaterialVersion(c.Request().Context(),
		c.Param("id"), req.params(), eff)
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"version": v, "earlier_inspections": impact})
}

// ---------------------------------------------------------------------------
// Locations / spectra
// ---------------------------------------------------------------------------

func (s *Server) createLocation(c echo.Context) error {
	var req createLocationReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	comm, err := parseTime(req.CommissionedAt)
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	loc, sp, err := s.svc.CreateLocation(c.Request().Context(), app.LocationInput{
		Name: req.Name, Geometry: geometryKind(req.Geometry), WidthMM: req.WidthMM,
		MaterialID: req.MaterialID, SafetyFactor: req.SafetyFactor,
		CommissionedAt: comm, Blocks: blocksToDomain(req.Blocks),
	})
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"location": loc, "spectrum": spectrumResp(sp)})
}

func (s *Server) listLocations(c echo.Context) error {
	ls, err := s.svc.ListLocations(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"locations": ls})
}

func (s *Server) getLocation(c echo.Context) error {
	l, err := s.svc.GetLocation(c.Request().Context(), c.Param("id"))
	if err != nil {
		return fail(c, http.StatusNotFound, err)
	}
	return c.JSON(http.StatusOK, l)
}

func (s *Server) updateLocation(c echo.Context) error {
	var req updateLocationReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	l, err := s.svc.UpdateLocation(c.Request().Context(), c.Param("id"), req.Name, req.SafetyFactor)
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusOK, l)
}

func (s *Server) replaceSpectrum(c echo.Context) error {
	var req replaceSpectrumReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	eff, err := parseTime(req.EffectiveAt)
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	sp, err := s.svc.ReplaceSpectrum(c.Request().Context(), c.Param("id"),
		blocksToDomain(req.Blocks), eff)
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, spectrumResp(sp))
}

func (s *Server) getSpectrum(c echo.Context) error {
	sp, err := s.svc.GetSpectrum(c.Request().Context(), c.Param("id"))
	if err != nil {
		return fail(c, http.StatusNotFound, err)
	}
	return c.JSON(http.StatusOK, spectrumResp(sp))
}

type spectrumRespBody struct {
	ID          string     `json:"id"`
	Version     int        `json:"version"`
	Blocks      []blockDTO `json:"blocks"`
	EffectiveAt string     `json:"effective_at"`
}

func spectrumResp(sp *model.SpectrumVersion) spectrumRespBody {
	return spectrumRespBody{
		ID: sp.ID, Version: sp.Version, Blocks: blocksFromDomain(sp.Blocks),
		EffectiveAt: sp.EffectiveAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

func (s *Server) submitRecord(c echo.Context) error {
	var req recordReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	in, err := req.input()
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	ev, proj, err := s.svc.SubmitResult(c.Request().Context(), c.Param("id"), in)
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"event": ev, "plan": proj.Plan})
}

func (s *Server) correctRecord(c echo.Context) error {
	var req recordReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	in, err := req.input()
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	ev, proj, err := s.svc.CorrectResult(c.Request().Context(),
		c.Param("id"), c.Param("eventId"), in)
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"event": ev, "plan": proj.Plan})
}

func (s *Server) voidRecord(c echo.Context) error {
	var req voidReq
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	ev, proj, err := s.svc.VoidResult(c.Request().Context(),
		c.Param("id"), c.Param("eventId"), req.Note)
	if err != nil {
		return fail(c, http.StatusUnprocessableEntity, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"event": ev, "plan": proj.Plan})
}

func (s *Server) listRecords(c echo.Context) error {
	evs, err := s.svc.ListEvents(c.Request().Context(), c.Param("id"))
	if err != nil {
		return fail(c, http.StatusInternalServerError, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"events": evs})
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (s *Server) currentPlan(c echo.Context) error {
	p, err := s.svc.CurrentPlan(c.Request().Context(), c.Param("id"))
	if err != nil {
		return fail(c, http.StatusNotFound, err)
	}
	return c.JSON(http.StatusOK, planBody(p.Plan, p.Findings))
}

func (s *Server) planAt(c echo.Context) error {
	at, err := parseTime(c.QueryParam("at"))
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	p, err := s.svc.PlanAt(c.Request().Context(), c.Param("id"), at)
	if err != nil {
		return fail(c, http.StatusNotFound, err)
	}
	return c.JSON(http.StatusOK, planBody(p.Plan, p.Findings))
}

func (s *Server) comparePlan(c echo.Context) error {
	at, err := parseTime(c.QueryParam("at"))
	if err != nil {
		return fail(c, http.StatusBadRequest, err)
	}
	cmp, err := s.svc.ComparePlan(c.Request().Context(), c.Param("id"), at)
	if err != nil {
		return fail(c, http.StatusNotFound, err)
	}
	return c.JSON(http.StatusOK, cmp)
}

func (s *Server) planHistory(c echo.Context) error {
	snaps, err := s.svc.History(c.Request().Context(), c.Param("id"))
	if err != nil {
		return fail(c, http.StatusInternalServerError, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"snapshots": snaps})
}

func planBody(p model.Plan, findings []model.Finding) map[string]any {
	return map[string]any{"plan": p, "findings_used": len(findings)}
}

func geometryKind(s string) geometry.Kind { return geometry.Kind(s) }
