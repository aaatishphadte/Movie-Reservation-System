package showtime

import (
	"net/http"

	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

type Handler struct {
	svc   *Service
	store *Store
}

func NewHandler(svc *Service, store *Store) *Handler { return &Handler{svc: svc, store: store} }

// Schedule: GET /showtimes?date=YYYY-MM-DD
func (h *Handler) Schedule(w http.ResponseWriter, r *http.Request) {
	ds, err := h.svc.Schedule(r.Context(), r.URL.Query().Get("date"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ds)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	st, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) SeatMap(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var viewer *auth.Principal
	if p, ok := auth.FromContext(r.Context()); ok {
		viewer = &p
	}
	sm, err := h.svc.SeatMap(r.Context(), id, viewer)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sm)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	st, err := h.svc.Create(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, st)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	n, err := h.svc.Cancel(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"showtime_id": id, "status": "cancelled", "reservations_cancelled": n})
}

func (h *Handler) ListHalls(w http.ResponseWriter, r *http.Request) {
	halls, err := h.store.ListHalls(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": halls})
}

func (h *Handler) CreateHall(w http.ResponseWriter, r *http.Request) {
	var in HallInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	hall, err := h.store.CreateHall(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, hall)
}
