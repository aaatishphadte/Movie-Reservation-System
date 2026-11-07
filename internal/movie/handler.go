package movie

import (
	"net/http"

	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

// Handler exposes the catalogue. Movie CRUD has no cross-entity business rules beyond what
// the store enforces transactionally, so there is deliberately no extra service layer here.
type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p := httpx.Paging(r)
	q := r.URL.Query()
	movies, err := h.store.List(r.Context(), q.Get("genre"), q.Get("q"), p)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.List[Movie]{Items: movies, Limit: p.Limit, Offset: p.Offset})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	m, err := h.store.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	m, err := h.store.Create(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, m)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in Input
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	m, err := h.store.Update(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.store.Delete(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListGenres(w http.ResponseWriter, r *http.Request) {
	g, err := h.store.Genres(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": g})
}
