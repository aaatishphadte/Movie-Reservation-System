package user

import (
	"net/http"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

type Handler struct {
	svc   *Service
	store *Store
}

func NewHandler(svc *Service, store *Store) *Handler { return &Handler{svc: svc, store: store} }

func (h *Handler) Signup(w http.ResponseWriter, r *http.Request) {
	var in SignupInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	u, err := h.svc.Signup(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, u)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var in LoginInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s, err := h.svc.Login(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	u, err := h.store.GetByID(r.Context(), auth.MustPrincipal(r.Context()).UserID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p := httpx.Paging(r)
	users, err := h.store.List(r.Context(), p)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.List[User]{Items: users, Limit: p.Limit, Offset: p.Offset})
}

type roleInput struct {
	Role auth.Role `json:"role"`
}

func (in roleInput) Validate() error {
	if !in.Role.Valid() {
		return apperr.Invalid("role must be 'user' or 'admin'")
	}
	return nil
}

func (h *Handler) SetRole(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in roleInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	u, err := h.svc.SetRole(r.Context(), auth.MustPrincipal(r.Context()), id, in.Role)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}
