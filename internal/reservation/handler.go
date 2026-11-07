package reservation

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Hold: POST /showtimes/{id}/holds {"seat_ids":[...]}
func (h *Handler) Hold(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in SeatsInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	hold, err := h.svc.Hold(r.Context(), auth.MustPrincipal(r.Context()), id, in.SeatIDs)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, hold)
}

// ReleaseHold: POST /showtimes/{id}/holds/release {"seat_ids":[...]}
func (h *Handler) ReleaseHold(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in SeatsInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.ReleaseHold(r.Context(), auth.MustPrincipal(r.Context()), id, in.SeatIDs); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Create: POST /reservations  (header Idempotency-Key recommended)
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in ReserveInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, replayed, err := h.svc.Reserve(r.Context(), auth.MustPrincipal(r.Context()), in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	httpx.JSON(w, status, res)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Get(r.Context(), auth.MustPrincipal(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Cancel(r.Context(), auth.MustPrincipal(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// ListMine: GET /reservations?scope=upcoming|past
func (h *Handler) ListMine(w http.ResponseWriter, r *http.Request) {
	p := httpx.Paging(r)
	items, err := h.svc.ListMine(r.Context(), auth.MustPrincipal(r.Context()), r.URL.Query().Get("scope"), p)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.List[Reservation]{Items: items, Limit: p.Limit, Offset: p.Offset})
}

// ListAll (admin): GET /admin/reservations?showtime_id=&user_id=&status=&scope=
func (h *Handler) ListAll(w http.ResponseWriter, r *http.Request) {
	p := httpx.Paging(r)
	showtimeID, err := httpx.QueryID(r, "showtime_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	userID, err := httpx.QueryID(r, "user_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	q := r.URL.Query()
	items, err := h.svc.ListAll(r.Context(), ListFilter{
		UserID: userID, ShowtimeID: showtimeID, Status: q.Get("status"), Scope: q.Get("scope"),
		Limit: p.Limit, Offset: p.Offset,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.List[Reservation]{Items: items, Limit: p.Limit, Offset: p.Offset})
}
